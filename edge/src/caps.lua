-- Custom capability ids, in one place.
--
-- The namespace below is a PLACEHOLDER. SmartThings assigns the real namespace
-- when the account owner runs `smartthings capabilities:create` (design doc
-- §11.1); #74 ships `tools/apply-namespace.js` to rewrite this constant and the
-- profile YAML in one go. Until then the ids do not resolve on a hub, which is
-- why `caps.load` degrades gracefully instead of erroring at driver start.

local NAMESPACE = "pccontrol00000"

local caps = {}

caps.NAMESPACE = NAMESPACE

caps.POWER_STATE = NAMESPACE .. ".pcPowerState"
caps.COMMAND = NAMESPACE .. ".pcCommand"
caps.SCHEDULE = NAMESPACE .. ".pcSchedule"
caps.STATUS = NAMESPACE .. ".pcStatus"
caps.SESSION = NAMESPACE .. ".pcSession"

-- Stable short keys -> capability id. `caps.load` returns the same keys.
caps.ids = {
  power_state = caps.POWER_STATE,
  command = caps.COMMAND,
  schedule = caps.SCHEDULE,
  status = caps.STATUS,
  session = caps.SESSION,
}

--- Resolve the custom capability objects from `st.capabilities`.
-- Indexing `st.capabilities` with an unknown id raises, so each lookup is
-- guarded: a missing capability (placeholder namespace, capability not yet
-- created on the account) yields nil for that key instead of taking the whole
-- driver down. Returns the table plus a list of the ids that failed to load.
-- @param capabilities the `st.capabilities` module
function caps.load(capabilities)
  local loaded, missing = {}, {}
  for key, id in pairs(caps.ids) do
    local ok, cap = pcall(function() return capabilities[id] end)
    if ok and cap then
      loaded[key] = cap
    else
      missing[#missing + 1] = id
    end
  end
  table.sort(missing)
  return loaded, missing
end

return caps
