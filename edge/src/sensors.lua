-- The PC app v1.3.0 cards (docs/design/v1.3-sensors.md, edge-driver.md §4.3):
-- what a status body says about the person at the PC (#151), the camera
-- and the microphone (#152), the primary monitor (#153) and the audio
-- device slots (#154), as `{ cap, attr, value, component }` records.
--
-- Pure, like features.lua: nothing here touches st.* or cosock. The command
-- side is handlers/monitor.lua and handlers/audio.lua.

local caps = require "caps"

local sensors = {}

local function ev(list, cap, attr, value, component)
  list[#list + 1] = { cap = cap, attr = attr, value = value, component = component }
end

local function append(into, list)
  for _, e in ipairs(list) do
    into[#into + 1] = e
  end
  return into
end

--------------------------------------------------------------------------------
-- #151: in use, idle, locked - a routine condition
--------------------------------------------------------------------------------

-- The card of the PC's `presence` component ("사용 감지"): `pcPresence`, one
-- row and two routine conditions. `pcUser` keeps its rows (published, a
-- routine may read `locked`); its presentation has no idle condition and is
-- frozen, hence a new capability.
sensors.PRESENCE_COMPONENT = "presence"
sensors.CAP_PRESENCE = caps.PRESENCE

-- `pcPresence.activity`. `unknown`: the session is not shared
-- (`smartthings.expose_session` off), the tray app sends no heartbeat (no
-- `idle_seconds`), or the PC is not there. Not a routine condition.
sensors.ACTIVE = "active"
sensors.IDLE = "idle"
sensors.LOCKED = "locked"
sensors.UNKNOWN = "unknown"

-- The `idleThreshold` preference: minutes without input that make "idle".
sensors.IDLE_DEFAULT_MINUTES = 10
sensors.IDLE_MAX_MINUTES = 1440

--- The `idleThreshold` preference, 1 to 1440 minutes, default 10.
function sensors.idle_threshold(preferences)
  local minutes = tonumber((preferences or {}).idleThreshold)
  if not minutes then
    return sensors.IDLE_DEFAULT_MINUTES
  end
  minutes = math.floor(minutes)
  if minutes < 1 then
    return 1
  end
  if minutes > sensors.IDLE_MAX_MINUTES then
    return sensors.IDLE_MAX_MINUTES
  end
  return minutes
end

--- Whole minutes without input, or nil when the status does not say (§3.2:
--- `idle_seconds` only rides with a tray heartbeat of the last 90 seconds).
function sensors.idle_minutes(status)
  local session = (status or {}).session
  if type(session) ~= "table" or session.exposed ~= true then
    return nil
  end
  local seconds = tonumber(session.idle_seconds)
  if not seconds or seconds < 0 then
    return nil
  end
  return math.floor(seconds / 60)
end

--- `pcPresence.activity` for a status: `locked` while the session is locked,
--- else `idle` from `threshold` minutes without input on, else `active`;
--- `unknown` when the session is not shared or has no idle reading.
function sensors.activity(status, threshold)
  local session = (status or {}).session
  if type(session) ~= "table" or session.exposed ~= true then
    return sensors.UNKNOWN
  end
  if session.locked == true then
    return sensors.LOCKED
  end
  local minutes = sensors.idle_minutes(status)
  if minutes == nil then
    return sensors.UNKNOWN
  end
  if minutes >= (threshold or sensors.IDLE_DEFAULT_MINUTES) then
    return sensors.IDLE
  end
  return sensors.ACTIVE
end

--- #151: the presence rows. `idleMinutes` follows `pcUser.idleMinutes`: 0
--- while the session is shared without a reading, left alone while it is not
--- shared (a repaint, `fill`, paints 0 - a row never sent reads "-").
function sensors.presence_events(status, opts)
  opts = opts or {}
  local events = {}
  ev(events, sensors.CAP_PRESENCE, "activity", sensors.activity(status, opts.idle_threshold),
    sensors.PRESENCE_COMPONENT)
  local session = (status or {}).session
  if type(session) == "table" and session.exposed == true then
    ev(events, sensors.CAP_PRESENCE, "idleMinutes", sensors.idle_minutes(status) or 0,
      sensors.PRESENCE_COMPONENT)
  elseif opts.fill then
    ev(events, sensors.CAP_PRESENCE, "idleMinutes", 0, sensors.PRESENCE_COMPONENT)
  end
  return events
end

--------------------------------------------------------------------------------
-- every card
--------------------------------------------------------------------------------

--- Every v1.3.0 row a status body paints. Called by state.apply_status.
-- @param opts `lang`, `idle_threshold` (minutes), `fill` (a repaint)
function sensors.apply_status(status, opts)
  opts = opts or {}
  local events = {}
  append(events, sensors.presence_events(status, opts))
  return events
end

--- Every v1.3.0 row a device that has never been polled paints.
function sensors.initial_rows(lang)
  return sensors.apply_status(nil, { lang = lang, fill = true })
end

--- While the PC is not there (`state.offline_mode`): nobody is at it, so the
--- activity is `unknown` - not a condition, so no routine fires on it, and
--- "사용 중" does not outlive the PC. `text` is the offline wording.
function sensors.offline_rows(_text)
  local records = {}
  ev(records, sensors.CAP_PRESENCE, "activity", sensors.UNKNOWN, sensors.PRESENCE_COMPONENT)
  return records
end

return sensors
