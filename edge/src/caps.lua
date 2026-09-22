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
-- #84: same rule once more. `execute` gained a `none` argument (a dismissed
-- list sends the row's current value, §14.5) and the definition gained
-- `planCommand`, so `pcAction` became `pcRun`.
-- #85: and again. `planCommand`/`setPlanCommand` moved to the schedule
-- capability - the app groups detail rows by the capability that owns them, so
-- "what a schedule runs" has to live in the schedule card - which made this a
-- different definition: `pcRun` became `pcExec`.
caps.COMMAND = NAMESPACE .. ".pcexec"
-- #83: same rule again - the definition gained a `status` enum (a detailView
-- list cannot read a boolean, §14.5), so `pcTimer` became `pcPlan`.
-- #85: `schedule(minutes)` now accepts 0 (the list's Cancel entry; the cloud
-- validates arguments against the definition and rejected `minimum: 1`, so the
-- entry never reached the hub) and the definition gained `planCommand` /
-- `setPlanCommand`, so `pcPlan` became `pcCountdown`.
caps.SCHEDULE = NAMESPACE .. ".pccountdown"
-- #85: the definition gained a `versions` attribute (the row that says which
-- service, driver and screen template a device is actually running), so by the
-- same rule it needed a new id: `pcHealth` became `pcInfo`. The Lua constant
-- keeps its name - what it points at is "the status capability".
caps.STATUS = NAMESPACE .. ".pcinfo"
caps.SESSION = NAMESPACE .. ".pcuser"
-- #86: the version row is a capability of its own. Two `state` rows of the SAME
-- capability are drawn side by side in two narrow columns and both are cut off
-- (measured on the phone 2026-09-22, §14.5), so "상태" and "버전" - which sat on
-- pcInfo together - had to be split. A capability with one state row renders
-- full width. `pcInfo.versions` keeps its definition and is still emitted: the
-- definition cannot change without another rename (§14.4), and an attribute
-- that is never emitted makes the app say the state was not fully reported.
caps.VERSION = NAMESPACE .. ".pcversion"

-- Stable short keys -> capability id. `caps.load` returns the same keys.
caps.ids = {
  power_state = caps.POWER_STATE,
  command = caps.COMMAND,
  schedule = caps.SCHEDULE,
  status = caps.STATUS,
  session = caps.SESSION,
  version = caps.VERSION,
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
