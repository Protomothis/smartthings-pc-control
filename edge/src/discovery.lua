-- Device discovery: SSDP search (design doc §4.6) plus the identity and
-- duplicate rules of §13.1/§13.2, and manual add for a PC that does not answer.
--
-- The parsing (`msearch`, `parse_response`) and every decision (`plan`,
-- `should_search`) are pure; only `ssdp_search` and the `apply*` helpers touch a
-- socket or a device, and both take injectable `deps`.

local client = require "client"
local i18n = require "i18n"

local discovery = {}

discovery.PROFILE = "pc.v1"
discovery.PLACEHOLDER_LABEL = "PC Control (set IP in settings)"
discovery.DNI_PREFIX = "pc-control-"
discovery.MANUFACTURER = "Protomothis"
discovery.MODEL = "PC Control"
-- §4.6: the search target the service's responder answers.
discovery.SSDP_ST = "urn:smartthings-pc-control:device:pc:1"
discovery.SSDP_GROUP = "239.255.255.250"
discovery.SSDP_PORT = 1900
-- MX is the longest the responder may wait before answering; the service
-- clamps it to 3s anyway.
discovery.SSDP_MX = 2
discovery.SSDP_TIMEOUT = 4
-- §13.2: at most one targeted re-search per device per five minutes, so an
-- unreachable PC cannot turn into a multicast storm.
discovery.SEARCH_COOLDOWN = 300

-- Device fields. The machine_id is the identity (§13.1); hostname is kept to
-- notice two PCs sharing one MachineGuid, and the last search time enforces
-- the cooldown above.
discovery.MACHINE_FIELD = "machine_id"
discovery.HOSTNAME_FIELD = "hostname"
discovery.LAST_SEARCH_FIELD = "last_ssdp"

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

--- The M-SEARCH datagram (§4.6). `MAN` is quoted, as the spec requires and as
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
-- `USN: uuid:<machine_id>::urn:smartthings-pc-control:device:pc:1` (§4.6) is
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
-- pure: identity and duplicate rules (§13.1)
--------------------------------------------------------------------------------

-- Bumped per manually created device so two devices added within the same
-- second cannot share an id (math.random is not seeded on a fresh Lua state).
local manual_seq = 0

--- Device network id. §13.1 identifies a device by its `machine_id`; the
--- prefix keeps the id recognisable in the IDE and is what #71 shipped, so
--- existing devices keep working.
function discovery.network_id(machine_id)
  if type(machine_id) == "string" and machine_id ~= "" then
    return discovery.DNI_PREFIX .. machine_id
  end
  manual_seq = manual_seq + 1
  return string.format("%smanual-%x-%d", discovery.DNI_PREFIX, os.time(), manual_seq)
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
--- one on its first successful status, §13.1), then the DNI for a device that
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

--- The device in `devices` that owns `machine_id`, or nil (§13.1: never create
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

--- Merge SSDP hits by machine_id (§13.1). A second response for a machine_id
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
      label = (found.hostname ~= nil and found.hostname ~= "") and found.hostname
        or discovery.PLACEHOLDER_LABEL,
      ip = found.ip,
      port = found.port,
      hostname = found.hostname,
      machine_id = found.machine_id,
    }
  end

  local prefs = device.preferences or {}
  -- §13.2: default true. Turning it off pins the device to whatever address it
  -- has now.
  local follow = prefs.followDiscovery ~= false
  local pinned = type(prefs.ipAddress) == "string" and prefs.ipAddress ~= ""
  local out = { action = "update", repoll = false }

  local known_host = get_field(device, discovery.HOSTNAME_FIELD)
  if found.hostname and found.hostname ~= "" then
    if type(known_host) == "string" and known_host ~= "" and known_host ~= found.hostname then
      -- §13.1: same machine_id, different hostname.
      out.warning = "hostname_mismatch"
      out.conflict = found.hostname
    end
    out.hostname = found.hostname
  end
  if found.hostname_conflict then
    out.warning = "hostname_mismatch"
    out.conflict = found.hostname_conflict
  end

  -- §13.1: a manual device adopts the machine_id it was matched on.
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

--- §13.2: may this device do a targeted SSDP search now?
function discovery.should_search(last, now)
  now = tonumber(now) or 0
  last = tonumber(last)
  if not last then
    return true
  end
  return (now - last) >= discovery.SEARCH_COOLDOWN
end

--------------------------------------------------------------------------------
-- the search itself (§4.6)
--------------------------------------------------------------------------------

--- `GET LOCATION` -> the description document (§4.6), or nil.
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

--- §4.6: M-SEARCH, then `GET LOCATION` for every responder.
--
-- Returns a list of `{ ip, port, machine_id, hostname, service_version,
-- secret_set, location }`, merged by machine_id (§13.1). Any socket failure
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
        -- §13.2: the LOCATION host is the interface the hub can reach, so it
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

--- Apply a plan to an existing device: fields, warning, immediate poll (§13.2).
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

--- Create one device for `found` (nil = manual placeholder).
function discovery.create(driver, found)
  found = found or {}
  local plan = discovery.plan(nil, found)
  local created = driver:try_create_device({
    type = "LAN",
    device_network_id = plan.device_network_id,
    label = plan.label,
    profile = discovery.PROFILE,
    manufacturer = discovery.MANUFACTURER,
    model = discovery.MODEL,
    vendor_provided_label = discovery.MODEL,
  })
  -- The device object does not exist yet, so the address travels in a pending
  -- table that `added`/`init` picks up by DNI (§5.4: an SSDP device arrives
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
  return true
end

--- §13.2: one targeted search for a device that went unreachable, rate limited
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

--- True when the hub already has a PC Control device with no IP configured.
--- Without this, every tap on "Scan" would leave another blank device behind.
function discovery.has_unconfigured(driver)
  local ok, devices = pcall(function() return driver:get_devices() end)
  if not ok or type(devices) ~= "table" then
    return false
  end
  for _, device in ipairs(devices) do
    if not client.device_base_url(device) then
      return true
    end
  end
  return false
end

--- Driver discovery handler: SSDP first, manual placeholder as the fallback.
function discovery.handle(driver, _opts, should_continue, deps)
  deps = deps or {}
  local log = logger()

  local found = discovery.ssdp_search(discovery.SSDP_TIMEOUT, deps) or {}
  if #found > 0 then
    log.info(i18n.t(nil, "discovery_found", #found))
  end

  local ok, devices = pcall(function() return driver:get_devices() end)
  devices = ok and devices or {}

  for _, hit in ipairs(found) do
    if should_continue and not should_continue() then
      return
    end
    local existing = discovery.find(devices, hit.machine_id)
    if existing then
      -- §13.1: never a second device for a machine_id we already own.
      log.info(string.format("%s is already added, updating its address", tostring(hit.hostname)))
      discovery.apply(driver, existing, hit, deps)
    else
      log.info(string.format("discovered PC Control service at %s", tostring(hit.ip)))
      discovery.create(driver, hit)
    end
  end

  if #found > 0 then
    return
  end

  if discovery.has_unconfigured(driver) then
    log.info("a PC Control device without an IP address already exists, not adding another")
    return
  end

  log.info("no SSDP responder answered, adding a device for manual setup")
  discovery.create(driver, nil)
end

return discovery
