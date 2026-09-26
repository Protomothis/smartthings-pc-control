-- Device discovery: SSDP search (design doc §3.6) plus the identity and
-- duplicate rules of §6.5.
--
-- #94: SSDP is the only way a device is added. A search that nobody answers
-- creates nothing at all any more - the PC is off, the service is not running
-- or UDP 1900 is closed, and a blank device named after the problem only got
-- in the way of the next scan.
--
-- The parsing (`msearch`, `parse_response`) and every decision (`plan`,
-- `should_search`) are pure; only `ssdp_search` and the `apply*` helpers touch a
-- socket or a device, and both take injectable `deps`.

local client = require "client"
local i18n = require "i18n"
local profiles = require "profiles"

local discovery = {}

-- The profile new devices are created with; src/profiles.lua is the single
-- source of truth for the version (§6.6).
discovery.PROFILE = profiles.PC
discovery.DNI_PREFIX = "pc-control-"
discovery.MANUFACTURER = "Protomothis"
discovery.MODEL = "PC Control"
-- #94: the device's `model` carries the PC's id, because the app's device
-- information screen is the only place a user can read it and the same eight
-- characters are what the Windows app's SmartThings section shows (#95).
-- The label stays "<호스트> 컴퓨터" - a name, not an id.
discovery.MODEL_SEPARATOR = " · "
discovery.MODEL_ID_LENGTH = 8
-- §3.6: the search target the service's responder answers.
discovery.SSDP_ST = "urn:smartthings-pc-control:device:pc:1"
discovery.SSDP_GROUP = "239.255.255.250"
discovery.SSDP_PORT = 1900
-- MX is the longest the responder may wait before answering; the service
-- clamps it to 3s anyway.
discovery.SSDP_MX = 2
discovery.SSDP_TIMEOUT = 4
-- §6.5: at most one targeted re-search per device per five minutes, so an
-- unreachable PC cannot turn into a multicast storm.
discovery.SEARCH_COOLDOWN = 300

-- Device fields. The machine_id is the identity (§6.5); hostname is kept to
-- notice two PCs sharing one MachineGuid, and the last search time enforces
-- the cooldown above.
discovery.MACHINE_FIELD = "machine_id"
discovery.HOSTNAME_FIELD = "hostname"
discovery.LAST_SEARCH_FIELD = "last_ssdp"
-- #94: the short id already written into this device's `model`. A device
-- created before #94 has "PC Control" there, so the update runs once per
-- device and the field stops it from running on every poll afterwards.
discovery.MODEL_FIELD = "model_id"

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

--------------------------------------------------------------------------------
-- pure: SSDP text
--------------------------------------------------------------------------------

--- The M-SEARCH datagram (§3.6). `MAN` is quoted, as the spec requires and as
--- `parseMSearch` in service/st_ssdp.go checks.
function discovery.msearch(mx)
  return table.concat({
    "M-SEARCH * HTTP/1.1",
    string.format("HOST: %s:%d", discovery.SSDP_GROUP, discovery.SSDP_PORT),
    'MAN: "ssdp:discover"',
    "MX: " .. tostring(math.floor(tonumber(mx) or discovery.SSDP_MX)),
    "ST: " .. discovery.SSDP_ST,
    "", "",
  }, "\r\n")
end

--- Parse one SSDP 200 OK: `{ location, usn, st, machine_id }`, or nil.
--
-- `USN: uuid:<machine_id>::urn:smartthings-pc-control:device:pc:1` (§3.6) is
-- where the machine id comes from before the description is fetched.
function discovery.parse_response(text)
  if type(text) ~= "string" or text == "" then
    return nil
  end
  local lines = {}
  for line in (text:gsub("\r\n", "\n") .. "\n"):gmatch("([^\n]*)\n") do
    lines[#lines + 1] = line
  end
  if not (lines[1] or ""):match("^HTTP/1%.%d%s+200") then
    return nil
  end

  local headers = {}
  for i = 2, #lines do
    local name, value = lines[i]:match("^([^:]+):%s*(.-)%s*$")
    if name then
      headers[name:lower()] = value
    end
  end

  local location = headers["location"]
  if type(location) ~= "string" or location == "" then
    return nil
  end
  local usn = headers["usn"] or ""
  local machine_id = usn:match("^uuid:(.-)::") or usn:match("^uuid:(.+)$")
  if machine_id == "" then
    machine_id = nil
  end
  return {
    location = location,
    usn = usn ~= "" and usn or nil,
    st = headers["st"],
    machine_id = machine_id,
  }
end

--- Split `http://192.168.1.20:5001/st/v1/description` into ip and port.
function discovery.location_address(location)
  if type(location) ~= "string" then
    return nil
  end
  local host, port = location:match("^https?://([^/:]+):(%d+)")
  if host then
    return host, tonumber(port)
  end
  host = location:match("^https?://([^/:]+)")
  return host, nil
end

--------------------------------------------------------------------------------
-- pure: identity and duplicate rules (§6.5)
--------------------------------------------------------------------------------

-- Bumped per manually created device so two devices added within the same
-- second cannot share an id (math.random is not seeded on a fresh Lua state).
local manual_seq = 0

--- Device network id. §6.5 identifies a device by its `machine_id`; the
--- prefix keeps the id recognisable in the IDE and is what #71 shipped, so
--- existing devices keep working.
function discovery.network_id(machine_id)
  if type(machine_id) == "string" and machine_id ~= "" then
    return discovery.DNI_PREFIX .. machine_id
  end
  manual_seq = manual_seq + 1
  return string.format("%smanual-%x-%d", discovery.DNI_PREFIX, os.time(), manual_seq)
end

--- The first `MODEL_ID_LENGTH` characters of a machine_id, or nil (#94).
--- Eight characters of a MachineGuid ("58bff996") are enough to tell two PCs
--- apart at a glance and short enough for a device-information row.
function discovery.short_id(machine_id)
  if type(machine_id) ~= "string" or machine_id == "" then
    return nil
  end
  return machine_id:sub(1, discovery.MODEL_ID_LENGTH)
end

--- The `model` a device with this machine_id carries: `PC Control · 58bff996`
--- (#94). Without an id it is the bare model name, which is what a device
--- created before #94 already has.
function discovery.model_for(machine_id)
  local short = discovery.short_id(machine_id)
  if not short then
    return discovery.MODEL
  end
  return discovery.MODEL .. discovery.MODEL_SEPARATOR .. short
end

local function get_field(device, name)
  if type(device) ~= "table" or type(device.get_field) ~= "function" then
    return nil
  end
  local ok, value = pcall(function() return device:get_field(name) end)
  if ok then
    return value
  end
  return nil
end

--- The machine_id of a device: the stored field first (a manual device adopts
--- one on its first successful status, §6.5), then the DNI for a device that
--- SSDP created.
function discovery.machine_id_of(device)
  local field = get_field(device, discovery.MACHINE_FIELD)
  if type(field) == "string" and field ~= "" then
    return field
  end
  local dni = (device or {}).device_network_id
  if type(dni) ~= "string" then
    return nil
  end
  local id = dni:sub(1, #discovery.DNI_PREFIX) == discovery.DNI_PREFIX
    and dni:sub(#discovery.DNI_PREFIX + 1) or nil
  if not id or id == "" or id:match("^manual%-") then
    return nil
  end
  return id
end

--- The device in `devices` that owns `machine_id`, or nil (§6.5: never create
--- a second device for a machine_id that is already here).
function discovery.find(devices, machine_id)
  if type(devices) ~= "table" or type(machine_id) ~= "string" or machine_id == "" then
    return nil
  end
  for _, device in ipairs(devices) do
    if discovery.machine_id_of(device) == machine_id then
      return device
    end
  end
  return nil
end

--- Merge SSDP hits by machine_id (§6.5). A second response for a machine_id
--- that reports a different hostname is two cloned PCs, not two interfaces of
--- one: the survivor carries `hostname_conflict` so the driver can warn.
function discovery.merge(found)
  local out, index = {}, {}
  for _, hit in ipairs(found or {}) do
    local id = hit.machine_id
    if type(id) == "string" and id ~= "" then
      local seen = index[id]
      if not seen then
        index[id] = hit
        out[#out + 1] = hit
      elseif hit.hostname and seen.hostname and hit.hostname ~= seen.hostname then
        seen.hostname_conflict = hit.hostname
      end
    end
  end
  return out
end

--- What to do about one SSDP hit. `device` is nil when nothing owns the
--- machine_id yet. Pure: it reads preferences and fields, and decides.
--
-- Returns `{ action, label, device_network_id, ip, port, hostname, machine_id,
-- repoll, warning }`. `ip`/`port`/`hostname`/`machine_id` are the device fields
-- to write, absent when they must not change.
function discovery.plan(device, found)
  found = found or {}
  if device == nil then
    return {
      action = "create",
      device_network_id = discovery.network_id(found.machine_id),
      label = (found.hostname ~= nil and found.hostname ~= "")
        and i18n.t(nil, "pc_label", found.hostname)
        -- A responder that gives no hostname is still a PC we found, so it is
        -- named after the id its model carries rather than after a problem.
        or discovery.model_for(found.machine_id),
      model = discovery.model_for(found.machine_id),
      ip = found.ip,
      port = found.port,
      hostname = found.hostname,
      machine_id = found.machine_id,
    }
  end

  local prefs = device.preferences or {}
  -- §6.5: default true. Turning it off pins the device to whatever address it
  -- has now.
  local follow = prefs.followDiscovery ~= false
  local pinned = type(prefs.ipAddress) == "string" and prefs.ipAddress ~= ""
  local out = { action = "update", repoll = false }

  local known_host = get_field(device, discovery.HOSTNAME_FIELD)
  if found.hostname and found.hostname ~= "" then
    if type(known_host) == "string" and known_host ~= "" and known_host ~= found.hostname then
      -- §6.5: same machine_id, different hostname.
      out.warning = "hostname_mismatch"
      out.conflict = found.hostname
    end
    out.hostname = found.hostname
  end
  if found.hostname_conflict then
    out.warning = "hostname_mismatch"
    out.conflict = found.hostname_conflict
  end

  -- §6.5: a manual device adopts the machine_id it was matched on.
  if get_field(device, discovery.MACHINE_FIELD) == nil and found.machine_id then
    out.machine_id = found.machine_id
  end

  if follow and not pinned then
    local current = get_field(device, client.IP_FIELD)
    if found.ip and found.ip ~= "" and found.ip ~= current then
      out.ip = found.ip
      out.repoll = true
    end
    if found.port and found.port ~= get_field(device, client.PORT_FIELD) then
      out.port = found.port
    end
  end
  return out
end

--- §6.5: may this device do a targeted SSDP search now?
function discovery.should_search(last, now)
  now = tonumber(now) or 0
  last = tonumber(last)
  if not last then
    return true
  end
  return (now - last) >= discovery.SEARCH_COOLDOWN
end

--------------------------------------------------------------------------------
-- the search itself (§3.6)
--------------------------------------------------------------------------------

--- `GET LOCATION` -> the description document (§3.6), or nil.
function discovery.fetch_description(location, deps)
  local body, err = client.fetch(location, deps)
  if not body then
    return nil, err
  end
  if body.protocol ~= nil and body.protocol ~= client.PROTOCOL then
    return nil, "incompatible"
  end
  return body
end

--- One M-SEARCH round: the parsed responses, before the descriptions.
function discovery.collect(socket, timeout, deps)
  deps = deps or {}
  local now = deps.now or os.time
  local udp = assert(socket.udp())
  pcall(function() udp:setsockname("0.0.0.0", 0) end)
  pcall(function() udp:settimeout(1) end)

  local message = discovery.msearch(discovery.SSDP_MX)
  udp:sendto(message, discovery.SSDP_GROUP, discovery.SSDP_PORT)

  local deadline = now() + (tonumber(timeout) or discovery.SSDP_TIMEOUT)
  local seen, responses = {}, {}
  while now() < deadline do
    local data, from = udp:receivefrom()
    if data then
      local parsed = discovery.parse_response(data)
      if parsed and not seen[parsed.location] then
        seen[parsed.location] = true
        parsed.source_ip = from
        responses[#responses + 1] = parsed
      end
    elseif from ~= "timeout" and from ~= "wantread" then
      break
    end
  end
  pcall(function() udp:close() end)
  return responses
end

--- §3.6: M-SEARCH, then `GET LOCATION` for every responder.
--
-- Returns a list of `{ ip, port, machine_id, hostname, service_version,
-- secret_set, location }`, merged by machine_id (§6.5). Any socket failure
-- yields an empty list: discovery is best effort, manual add is the fallback.
function discovery.ssdp_search(timeout, deps)
  deps = deps or {}
  if deps.search then
    return deps.search(timeout) or {}
  end
  local socket = deps.socket
  if not socket then
    local loaded, cosock = pcall(require, "cosock")
    if not loaded then
      return {}
    end
    socket = cosock.socket
  end

  local ok, responses = pcall(discovery.collect, socket, timeout, deps)
  if not ok then
    logger().warn("SSDP search failed: " .. tostring(responses))
    return {}
  end

  local found = {}
  for _, response in ipairs(responses) do
    local ip, port = discovery.location_address(response.location)
    local description = discovery.fetch_description(response.location, deps)
    if description then
      found[#found + 1] = {
        -- §3.6: the LOCATION host is the interface the hub can reach, so it
        -- beats anything the description could say about the address.
        ip = ip or response.source_ip,
        port = tonumber(description.port) or port,
        machine_id = description.machine_id or response.machine_id,
        hostname = description.hostname,
        service_version = description.service_version,
        secret_set = description.secret_set,
        location = response.location,
      }
    else
      logger().debug("no description at " .. tostring(response.location))
    end
  end
  return discovery.merge(found)
end

--------------------------------------------------------------------------------
-- applying a hit to the driver
--------------------------------------------------------------------------------

local function set_field(device, name, value)
  if value ~= nil then
    pcall(function() device:set_field(name, value, { persist = true }) end)
  end
end

--- Apply a plan to an existing device: fields, warning, immediate poll (§6.5).
function discovery.apply(driver, device, found, deps)
  deps = deps or {}
  local plan = discovery.plan(device, found)
  local log = logger()

  set_field(device, discovery.HOSTNAME_FIELD, plan.hostname)
  set_field(device, discovery.MACHINE_FIELD, plan.machine_id)
  set_field(device, client.IP_FIELD, plan.ip)
  set_field(device, client.PORT_FIELD, plan.port)

  local poll = deps.poll or require "poll"
  local lang = poll.lang(device)
  if plan.warning == "hostname_mismatch" then
    log.warn(string.format("machine_id %s answers as two hostnames", tostring(found.machine_id)))
    pcall(function()
      poll.emit_message(device, i18n.t(lang, "hostname_mismatch", tostring(plan.conflict)))
    end)
  end

  if plan.ip then
    log.info(string.format("device %s moved to %s", tostring(device.id), plan.ip))
    pcall(function() poll.emit_message(device, i18n.t(lang, "ip_updated", plan.ip)) end)
  end
  if plan.repoll then
    pcall(function() poll.once(driver, device, { deps = deps }) end)
  end
  return plan
end

--- Create one device for an SSDP hit (#94: there is no other way in).
function discovery.create(driver, found)
  found = found or {}
  local plan = discovery.plan(nil, found)
  local created = driver:try_create_device({
    type = "LAN",
    device_network_id = plan.device_network_id,
    label = plan.label,
    profile = discovery.PROFILE,
    manufacturer = discovery.MANUFACTURER,
    -- #94: `PC Control · <id 8자리>`, so the app's device information screen
    -- shows the same id the Windows app does.
    model = plan.model,
    vendor_provided_label = discovery.MODEL,
  })
  -- The device object does not exist yet, so the address travels in a pending
  -- table that `added`/`init` picks up by DNI (§7: an SSDP device arrives
  -- with its IP filled in).
  if plan.ip or plan.machine_id then
    discovery.remember(plan.device_network_id, {
      ip = plan.ip, port = plan.port,
      machine_id = plan.machine_id, hostname = plan.hostname,
    })
  end
  return created
end

-- DNI -> address of a device created by SSDP but not yet initialised.
local pending = {}

function discovery.remember(dni, info)
  if type(dni) == "string" and dni ~= "" then
    pending[dni] = info
  end
end

--- Take the remembered address for a DNI (init.lua calls this once).
function discovery.take(dni)
  local info = pending[dni or ""]
  pending[dni or ""] = nil
  return info
end

--- Write the remembered address onto a freshly created device.
function discovery.adopt(device)
  local info = discovery.take((device or {}).device_network_id)
  if not info then
    return false
  end
  set_field(device, client.IP_FIELD, info.ip)
  set_field(device, client.PORT_FIELD, info.port)
  set_field(device, discovery.MACHINE_FIELD, info.machine_id)
  set_field(device, discovery.HOSTNAME_FIELD, info.hostname)
  -- #94: this device was created with the id already in its model, so the
  -- one-time update has nothing to do.
  set_field(device, discovery.MODEL_FIELD, discovery.short_id(info.machine_id))
  return true
end

--- #94: put the PC id in `model` on a device that was created without it.
--
-- Runs once per device: the short id is persisted, so the poll that calls this
-- on every successful status only reads a field afterwards. A hub that refuses
-- `try_update_metadata` simply keeps the old model (the same rule
-- `profiles.ensure` follows) - nothing else depends on it.
function discovery.ensure_model(device)
  local short = discovery.short_id(discovery.machine_id_of(device))
  if not short then
    -- No identity yet: the first successful poll learns it (§6.5).
    return false
  end
  if get_field(device, discovery.MODEL_FIELD) == short then
    return false
  end
  local current = (device or {}).model
  if type(current) == "string" and current:find(short, 1, true) then
    -- Created with it (or updated by an earlier driver run).
    set_field(device, discovery.MODEL_FIELD, short)
    return false
  end
  local model = discovery.model_for(discovery.machine_id_of(device))
  local ok = pcall(function() device:try_update_metadata({ model = model }) end)
  if not ok then
    logger().warn("could not put the PC id in the device model: " .. tostring(model))
    return false
  end
  set_field(device, discovery.MODEL_FIELD, short)
  logger().info("device model is now " .. model)
  return true
end

--- §6.5: one targeted search for a device that went unreachable, rate limited
--- to `SEARCH_COOLDOWN` per device.
function discovery.refresh(driver, device, deps)
  deps = deps or {}
  local machine_id = discovery.machine_id_of(device)
  if not machine_id then
    -- A manual device that never completed a status has no identity to match
    -- an SSDP response against.
    return false, "no machine id"
  end
  local now = (deps.now or os.time)()
  if not discovery.should_search(get_field(device, discovery.LAST_SEARCH_FIELD), now) then
    return false, "cooldown"
  end
  device:set_field(discovery.LAST_SEARCH_FIELD, now)

  for _, found in ipairs(discovery.ssdp_search(discovery.SSDP_TIMEOUT, deps) or {}) do
    if found.machine_id == machine_id then
      discovery.apply(driver, device, found, deps)
      return true
    end
  end
  return false, "not found"
end

--------------------------------------------------------------------------------
-- the discovery handler
--------------------------------------------------------------------------------

--- Driver discovery handler: SSDP is the only way a device is added (#94).
function discovery.handle(driver, _opts, should_continue, deps)
  deps = deps or {}
  local log = logger()

  local found = discovery.ssdp_search(discovery.SSDP_TIMEOUT, deps) or {}
  if #found == 0 then
    -- #94: nothing is created. The one line the user needs is why, and what
    -- to check, in the log the driver writes (ko + en, like every sentence
    -- this driver produces).
    log.info(i18n.t(nil, "discovery_none"))
    return
  end
  log.info(i18n.t(nil, "discovery_found", #found))

  local ok, devices = pcall(function() return driver:get_devices() end)
  devices = ok and devices or {}

  for _, hit in ipairs(found) do
    if should_continue and not should_continue() then
      return
    end
    local existing = discovery.find(devices, hit.machine_id)
    if existing then
      -- §6.5: never a second device for a machine_id we already own.
      log.info(string.format("%s is already added, updating its address", tostring(hit.hostname)))
      discovery.apply(driver, existing, hit, deps)
    else
      log.info(string.format("discovered PC Control service at %s", tostring(hit.ip)))
      discovery.create(driver, hit)
    end
  end
end

return discovery
