-- Device discovery.
--
-- Today this is manual add only: tapping "Scan nearby devices" creates one
-- placeholder device and the user fills in IP / secret / MAC in its settings.
-- SSDP (design doc §4.6) lands in #73 behind `discovery.ssdp_search`, which is
-- already called from the discovery handler so only the stub has to change.

local discovery = {}

discovery.PROFILE = "pc.v1"
discovery.PLACEHOLDER_LABEL = "PC Control (set IP in settings)"
discovery.DNI_PREFIX = "pc-control-"
discovery.MANUFACTURER = "Protomothis"
discovery.MODEL = "PC Control"
-- ST for the M-SEARCH in #73 (§4.6).
discovery.SSDP_ST = "urn:smartthings-pc-control:device:pc:1"
discovery.SSDP_TIMEOUT = 4

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

--- #73: SSDP M-SEARCH on 239.255.255.250:1900, returning a list of
--- `{ ip = ..., port = ..., machine_id = ..., hostname = ... }` from
--- `GET /st/v1/description`. Stubbed to "found nothing" so manual add works now.
function discovery.ssdp_search(timeout) -- luacheck: ignore timeout
  return {}
end

-- Bumped per manually created device so two devices added within the same
-- second cannot share an id (math.random is not seeded on a fresh Lua state).
local manual_seq = 0

--- Device network id for a manually added device. A machine_id-derived id is
--- used once SSDP can supply one (#73), so devices keep their identity when the
--- PC's IP changes.
function discovery.network_id(machine_id)
  if type(machine_id) == "string" and machine_id ~= "" then
    return discovery.DNI_PREFIX .. machine_id
  end
  manual_seq = manual_seq + 1
  return string.format("%smanual-%x-%d", discovery.DNI_PREFIX, os.time(), manual_seq)
end

--- True when the hub already has a PC Control device with no IP configured.
--- Without this, every tap on "Scan" would leave another blank device behind.
function discovery.has_unconfigured(driver)
  local ok, devices = pcall(function() return driver:get_devices() end)
  if not ok or type(devices) ~= "table" then
    return false
  end
  for _, device in ipairs(devices) do
    local ip = ((device or {}).preferences or {}).ipAddress
    if ip == nil or ip == "" then
      return true
    end
  end
  return false
end

--- Create one device for `found` (nil = manual placeholder).
function discovery.create(driver, found)
  found = found or {}
  local label = discovery.PLACEHOLDER_LABEL
  if found.hostname and found.hostname ~= "" then
    label = found.hostname
  end
  return driver:try_create_device({
    type = "LAN",
    device_network_id = discovery.network_id(found.machine_id),
    label = label,
    profile_reference = discovery.PROFILE,
    manufacturer = discovery.MANUFACTURER,
    model = discovery.MODEL,
    vendor_provided_label = discovery.MODEL,
  })
end

--- Driver discovery handler.
function discovery.handle(driver, _opts, should_continue)
  local log = logger()

  -- #73 will return real hits here; until then the loop is a no-op.
  for _, found in ipairs(discovery.ssdp_search(discovery.SSDP_TIMEOUT) or {}) do
    if should_continue and not should_continue() then
      return
    end
    log.info("discovered PC Control service at " .. tostring(found.ip))
    discovery.create(driver, found)
  end

  if discovery.has_unconfigured(driver) then
    log.info("a PC Control device without an IP address already exists, not adding another")
    return
  end

  log.info("no SSDP responder yet, adding a device for manual setup")
  discovery.create(driver, nil)
end

return discovery
