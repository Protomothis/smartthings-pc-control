-- The PC app v1.3.0 cards (docs/design/v1.3-sensors.md, edge-driver.md §4.3):
-- what a status body says about the person at the PC (#151), the camera
-- and the microphone (#152), the primary monitor (#153) and the audio
-- device slots (#154), as `{ cap, attr, value, component }` records.
--
-- Pure, like features.lua: nothing here touches st.* or cosock. The command
-- side is handlers/monitor.lua and handlers/audio.lua.

local caps = require "caps"
local features = require "features"
local i18n = require "i18n"

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
-- #152: the camera and the microphone
--------------------------------------------------------------------------------

-- `pcPrivacy`, on the same `presence` card: one summary row, two routine
-- conditions ("카메라", "마이크": 사용 중 / 사용 안 함).
sensors.CAP_PRIVACY = caps.PRIVACY

-- The service's words (`status.privacy.camera`) -> the enum. `unknown`:
-- nobody logged in, the store unreadable, the opt-in off, or a service
-- without the block.
sensors.IN_USE = "inUse"
local PRIVACY_VALUES = { in_use = sensors.IN_USE, idle = sensors.IDLE, unknown = sensors.UNKNOWN }

-- What `privacy_mode` answers, like `features.apps_mode`.
sensors.OLD = "old"
sensors.OFF = "off"
sensors.ON = "on"

--- "old" for a service older than the release that brought `privacy`, "off"
--- for one with the opt-in off (no block, no feature; `privacy.changed` with
--- `data: null`), else "on".
function sensors.privacy_mode(status)
  local set = features.parse(status)
  if set == false or features.version_below((status or {}).service_version,
      features.SINCE[features.PRIVACY]) == true and not set[features.PRIVACY] then
    return sensors.OLD
  end
  if not set[features.PRIVACY] or type(status.privacy) ~= "table" then
    return sensors.OFF
  end
  return sensors.ON
end

--- `camera`, `microphone`: the enum values of a status, `unknown` unless on.
function sensors.privacy_values(status)
  if sensors.privacy_mode(status) ~= sensors.ON then
    return sensors.UNKNOWN, sensors.UNKNOWN
  end
  local block = status.privacy
  return PRIVACY_VALUES[tostring(block.camera)] or sensors.UNKNOWN,
    PRIVACY_VALUES[tostring(block.microphone)] or sensors.UNKNOWN
end

--- "카메라", "마이크" or "카메라·마이크" for what is in use (`dev_*`).
function sensors.devices_text(lang, camera, microphone)
  if camera and microphone then
    return i18n.t(lang, "dev_both")
  end
  return i18n.t(lang, camera and "dev_camera" or "dev_microphone")
end

--- `pcPrivacy.summary`: "카메라 사용 중", "카메라·마이크 사용 중", "사용 안 함",
--- "알 수 없음", "감지 꺼짐", or "PC 앱 v1.3.0 필요". Short: it is the second
--- cell of the card's preview. Never "".
function sensors.privacy_summary(status, lang)
  if status == nil then
    return i18n.t(lang, "privacy_unknown")
  end
  local mode = sensors.privacy_mode(status)
  if mode == sensors.OLD then
    return features.note_text(lang, "needs_service")
  end
  if mode == sensors.OFF then
    return i18n.t(lang, "privacy_off")
  end
  local camera, microphone = sensors.privacy_values(status)
  local cam, mic = camera == sensors.IN_USE, microphone == sensors.IN_USE
  if cam or mic then
    return i18n.t(lang, "privacy_in_use", sensors.devices_text(lang, cam, mic))
  end
  if camera == sensors.IDLE and microphone == sensors.IDLE then
    return i18n.t(lang, "privacy_idle")
  end
  return i18n.t(lang, "privacy_unknown")
end

--- #152: the privacy rows. Always all three: `unknown` is not a routine
--- condition, so an opt-in switched off fires nothing.
function sensors.privacy_events(status, lang)
  local events = {}
  local camera, microphone = sensors.privacy_values(status)
  ev(events, sensors.CAP_PRIVACY, "summary", sensors.privacy_summary(status, lang), sensors.PRESENCE_COMPONENT)
  ev(events, sensors.CAP_PRIVACY, "camera", camera, sensors.PRESENCE_COMPONENT)
  ev(events, sensors.CAP_PRIVACY, "microphone", microphone, sensors.PRESENCE_COMPONENT)
  return events
end

--- #152: the note for a power command the PC app held back (`409
--- device_in_use`, guard "block"): "카메라 사용 중 · 실행 안 함".
function sensors.in_use_note(lang, devices)
  local cam, mic = false, false
  for _, name in ipairs(type(devices) == "table" and devices or {}) do
    cam = cam or name == "camera"
    mic = mic or name == "microphone"
  end
  if not cam and not mic then
    cam, mic = true, true
  end
  return i18n.t(lang, "device_in_use", sensors.devices_text(lang, cam, mic))
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
  append(events, sensors.privacy_events(status, opts.lang))
  return events
end

--- Every v1.3.0 row a device that has never been polled paints.
function sensors.initial_rows(lang)
  return sensors.apply_status(nil, { lang = lang, fill = true })
end

--- While the PC is not there (`state.offline_mode`): nobody is at it, so the
--- activity, the camera and the microphone are `unknown` - not a condition,
--- so no routine fires on it, and "사용 중" does not outlive the PC - and
--- the summary rows say `text` ("PC 꺼짐").
function sensors.offline_rows(text)
  local records = {}
  ev(records, sensors.CAP_PRESENCE, "activity", sensors.UNKNOWN, sensors.PRESENCE_COMPONENT)
  ev(records, sensors.CAP_PRIVACY, "summary", text, sensors.PRESENCE_COMPONENT)
  ev(records, sensors.CAP_PRIVACY, "camera", sensors.UNKNOWN, sensors.PRESENCE_COMPONENT)
  ev(records, sensors.CAP_PRIVACY, "microphone", sensors.UNKNOWN, sensors.PRESENCE_COMPONENT)
  return records
end

return sensors
