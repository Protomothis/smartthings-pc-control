-- Custom capability ids, in one place.
--
-- The namespace is the one SmartThings assigned to the owner's account when
-- the capabilities were created (design doc §11.1). Note that SmartThings
-- lower-cases the name part of a capability id ("pcPowerState" becomes
-- ".pcpowerstate"), so the ids below are lower-case while the definition
-- files keep the camelCase `name`. `tools/apply-namespace.js` rewrites the
-- namespace here, in the profile YAML and in capabilities/*.json in one go.
-- `caps.load` degrades gracefully if an id does not resolve on a hub.

local NAMESPACE = "numbersystem53811"

local caps = {}

caps.NAMESPACE = NAMESPACE

caps.POWER_STATE = NAMESPACE .. ".pcpowerstate"
caps.COMMAND = NAMESPACE .. ".pccommand"
caps.SCHEDULE = NAMESPACE .. ".pcschedule"
caps.STATUS = NAMESPACE .. ".pcstatus"
caps.SESSION = NAMESPACE .. ".pcsession"

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
