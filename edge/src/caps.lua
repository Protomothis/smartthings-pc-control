-- Custom capability ids, in one place.
--
-- The namespace is the one SmartThings assigned to the owner's account when
-- the capabilities were created (platform notes "capability id와 네임스페이스"). Note that SmartThings
-- lower-cases the name part of a capability id ("pcPower" becomes
-- ".pcpower"), so the ids below are lower-case while the definition
-- files keep the camelCase `name`. `tools/apply-namespace.js` rewrites the
-- namespace here, in the profile YAML and in capabilities/*.json in one go.
-- `caps.load` degrades gracefully if an id does not resolve on a hub.

local NAMESPACE = "numbersystem53811"

local caps = {}

caps.NAMESPACE = NAMESPACE

-- The hub caches a capability definition by id, so every change to one (an
-- attribute, an argument, an enum value, a range) needs a new id (platform
-- notes "허브의 정의 캐시"). The constants keep their role names; the ids are
-- the latest of each line (git log has the renames).
caps.POWER_STATE = NAMESPACE .. ".pcpower"
-- The command list (`execute`, `lastAction`, `supportedCommands`).
caps.COMMAND = NAMESPACE .. ".pcremote"
-- The schedule card. A list argument is a string enum: a list closed without a
-- pick sends its value unconverted (platform notes "상세 화면(detailView) 위젯").
caps.SCHEDULE = NAMESPACE .. ".pcdefer"
caps.STATUS = NAMESPACE .. ".pcinfo"
caps.SESSION = NAMESPACE .. ".pcuser"
-- The version row: two state rows of one capability are drawn half-width and
-- cut off (platform notes "화면 배치"). pcInfo still defines `versions`.
caps.VERSION = NAMESPACE .. ".pcversion"
-- The presets the PC app defines (media-notify.md §10).
caps.PRESET = NAMESPACE .. ".pcpreset"
-- The watch card (media-notify.md §11, #123): on the PC's `apps` component,
-- a summary row, the names row and one state per slot ("감시 1".."감시 5"),
-- which is what a routine reads. It replaced `pcapps` (a summary row on main)
-- and `pcapp` (one child device per app), and before them `pcactivity`.
-- `pcwatchlist`, not `pcwatch`: the cloud builds a device presentation from
-- a capability's FIRST presentation, whatever `presentation:update` stored
-- later (platform notes "프로필과 화면 생성", 2026-10-02), so the card's
-- current layout needed a new id.
caps.WATCH = NAMESPACE .. ".pcwatchlist"
-- "PC에 메시지 보내기": our own capability, because the app labels a standard
-- one with Samsung's words (platform notes "표준 capability"), bound to
-- `lastMessage` because a row bound to no attribute never gets its event.
caps.TOAST = NAMESPACE .. ".pctoast"

-- Stable short keys -> capability id. `caps.load` returns the same keys.
caps.ids = {
  power_state = caps.POWER_STATE,
  command = caps.COMMAND,
  schedule = caps.SCHEDULE,
  status = caps.STATUS,
  session = caps.SESSION,
  version = caps.VERSION,
  preset = caps.PRESET,
  watch = caps.WATCH,
  toast = caps.TOAST,
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
