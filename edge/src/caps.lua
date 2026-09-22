-- Custom capability ids, in one place.
--
-- The namespace is the one SmartThings assigned to the owner's account when
-- the capabilities were created (design doc §11.1). Note that SmartThings
-- lower-cases the name part of a capability id ("pcPower" becomes
-- ".pcpower"), so the ids below are lower-case while the definition
-- files keep the camelCase `name`. `tools/apply-namespace.js` rewrites the
-- namespace here, in the profile YAML and in capabilities/*.json in one go.
-- `caps.load` degrades gracefully if an id does not resolve on a hub.

local NAMESPACE = "numbersystem53811"

local caps = {}

caps.NAMESPACE = NAMESPACE

caps.POWER_STATE = NAMESPACE .. ".pcpower"
-- #82: the definition gained `lastAction`, and the hub caches capability
-- definitions by id for the whole hub (§14.4), so the new definition needed a
-- new id: `pcControl` became `pcAction`. The Lua constant keeps its name -
-- what it points at is "the command capability", whatever it is called.
caps.COMMAND = NAMESPACE .. ".pcaction"
caps.SCHEDULE = NAMESPACE .. ".pctimer"
caps.STATUS = NAMESPACE .. ".pchealth"
caps.SESSION = NAMESPACE .. ".pcuser"

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
