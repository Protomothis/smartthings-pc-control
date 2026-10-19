-- The PC app v1.3.0 cards (docs/design/v1.3-sensors.md, edge-driver.md §4.3):
-- what a status body says about the person at the PC (#151), the camera
-- and the microphone in use (#152), and the speaker and the microphone
-- (#160), as `{ cap, attr, value, component }` records.
--
-- Pure, like features.lua: nothing here touches st.* or cosock. All of it is
-- state for routine conditions; no card here takes a command (#160 removed
-- the monitor card of #153 and the device switching of #154).

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

-- The card of the PC's `presence` component ("사용 상태"): `pcPresence`, one
-- row and two routine conditions. `pcUser` keeps its rows (published, a
-- routine may read `locked`; on this card since v15); its presentation has
-- no idle condition and is frozen, hence a new capability.
sensors.PRESENCE_COMPONENT = features.PRESENCE_COMPONENT
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
-- #160: the speaker and the microphone
--------------------------------------------------------------------------------

-- The PC's `sound` component ("소리", v16), right after the media card:
-- `pcSpeaker` and `pcMic`, state only - what a routine reads as a condition.
-- Each has a summary row ("헤드셋 · 재생 중"), the names row of its device
-- slots ("1 스피커 · 3 헤드셋") and the slot of the default device (`output`
-- / `input`); the speaker also whether sound comes out of it (`playing`),
-- both whether they are muted. Device names differ on every PC and a
-- presentation is static, so a condition names slots, like the presets and
-- the watch list; the PC app hands them out by itself (#159), up to five per
-- kind. Switching the device from SmartThings (#154, `pcAudioOut` /
-- `pcAudioIn`) went with the PC app's side of it, and so did the monitor
-- card (#153, `pcMonitor`).
sensors.SOUND_COMPONENT = features.SOUND_COMPONENT
sensors.CAP_SPEAKER = caps.SPEAKER
sensors.CAP_MIC = caps.MIC
sensors.SLOT_NONE = "none"
sensors.AUDIO_SLOTS = 5

-- `pcSpeaker.playing` (`idle` and `unknown` are the ones above) and the
-- `muted` of both. `unknown`: the PC app did not say (null or missing -
-- nobody logged in, a device it could not read, an app older than #159) or
-- the PC is not there. Not a routine condition, so none of that fires one.
sensors.PLAYING = "playing"
sensors.MUTED = "muted"
sensors.UNMUTED = "unmuted"

-- `status.audio.outputs` / `inputs` -> the capability, its slot row and the
-- `audio` keys of the default device's name and mute.
sensors.AUDIO_KINDS = {
  output = { cap = caps.SPEAKER, attr = "output", list = "outputs", device = "device", muted = "muted" },
  input = { cap = caps.MIC, attr = "input", list = "inputs", device = "input_device", muted = "input_muted" },
}

-- The names row, like the watch list's (definition: 120), and the summary
-- (definition: 60). The device name in the summary is cut like a slot name
-- (`features.SLOT_NAME_MAX_CHARS`): the row is a cell of the card's preview.
sensors.AUDIO_NAMES_MAX_CHARS = 120
sensors.SOUND_SUMMARY_MAX_CHARS = 60

local function clean(text)
  if type(text) ~= "string" then
    return ""
  end
  return (text:gsub("%c", " "):gsub("^%s+", ""):gsub("%s+$", ""))
end

--- The filled slots of `kind` ("output"/"input"), by slot: `{ slot, name,
--- present, default }`. Anything outside 1-5, repeated or nameless is left out.
function sensors.audio_slots(status, kind)
  local out, seen = {}, {}
  local audio = (status or {}).audio
  local list = type(audio) == "table" and audio[sensors.AUDIO_KINDS[kind].list] or nil
  for _, entry in ipairs(type(list) == "table" and list or {}) do
    local slot = type(entry) == "table" and tonumber(entry.slot) or nil
    local name = type(entry) == "table" and clean(entry.name) or ""
    if slot and slot == math.floor(slot) and slot >= 1 and slot <= sensors.AUDIO_SLOTS
        and not seen[slot] and name ~= "" then
      seen[slot] = true
      out[#out + 1] = { slot = math.floor(slot), name = name,
        present = entry.present ~= false, default = entry.default == true }
    end
  end
  table.sort(out, function(a, b) return a.slot < b.slot end)
  return out
end

--- True when the PC app is older than the release that brought the slots:
--- no `features` at all, or a version below it without the slot list.
local function audio_old(status, kind)
  if features.parse(status) == false then
    return true
  end
  local audio = (status or {}).audio
  return features.version_below((status or {}).service_version, features.AUDIO_DEVICES_SINCE) == true
    and type((type(audio) == "table" and audio or {})[sensors.AUDIO_KINDS[kind].list]) ~= "table"
end

--- The `audio` block when it carries a reading (`features.has_reading`, the
--- rule of the media card's volume and mute), else nil.
local function reading(status)
  local audio = (status or {}).audio
  if features.has_reading(audio) then
    return audio
  end
  return nil
end

--- The slot of the default device, `none` when it is in none.
function sensors.audio_current(status, kind)
  for _, entry in ipairs(sensors.audio_slots(status, kind)) do
    if entry.default then
      return tostring(entry.slot)
    end
  end
  return sensors.SLOT_NONE
end

--- A Windows endpoint name is "<device> (<adapter>)": the device alone, or
--- the whole name when nothing is left.
local function short_name(name)
  local short = name:gsub("%s*%b()$", "")
  if short == "" then
    return name
  end
  return short
end

--- The short names of `slots`, the whole name where two would read the same.
local function short_names(slots)
  local count = {}
  local short = {}
  for i, entry in ipairs(slots) do
    short[i] = short_name(entry.name)
    count[short[i]] = (count[short[i]] or 0) + 1
  end
  for i, entry in ipairs(slots) do
    if count[short[i]] > 1 then
      short[i] = entry.name
    end
  end
  return short
end

--- The names row: "1 스피커 · 3 헤드셋 · 4 LG TV" (each name to
--- `features.SLOT_NAME_MAX_CHARS`), "없음", or "PC 앱 v1.3.0 필요". Never "".
function sensors.audio_names(status, kind, lang)
  if status ~= nil and audio_old(status, kind) then
    return features.note_text(lang, "needs_service")
  end
  local slots = sensors.audio_slots(status, kind)
  if #slots == 0 then
    return i18n.t(lang, "presets_none")
  end
  local parts = {}
  for i, name in ipairs(short_names(slots)) do
    parts[i] = tostring(slots[i].slot) .. " " .. features.truncate(name, features.SLOT_NAME_MAX_CHARS)
  end
  return features.truncate(table.concat(parts, " · "), sensors.AUDIO_NAMES_MAX_CHARS)
end

--- `pcSpeaker.playing`: `audio.playing` true/false -> `playing`/`idle`,
--- anything else `unknown`.
function sensors.speaker_playing(status)
  local audio = reading(status)
  if audio and type(audio.playing) == "boolean" then
    return audio.playing and sensors.PLAYING or sensors.IDLE
  end
  return sensors.UNKNOWN
end

--- `muted` of `kind`: `audio.muted` / `audio.input_muted` true/false ->
--- `muted`/`unmuted`, anything else `unknown`.
function sensors.audio_muted(status, kind)
  local audio = reading(status)
  local muted = audio and audio[sensors.AUDIO_KINDS[kind].muted]
  if type(muted) == "boolean" then
    return muted and sensors.MUTED or sensors.UNMUTED
  end
  return sensors.UNKNOWN
end

--- The summary row of `kind`: the default device's short name and one word -
--- "헤드셋 · 재생 중" / "스피커 · 조용함", "마이크 · 켜짐" / "마이크 · 음소거" -
--- or whichever half is known; "사용자 없음" while nobody is logged in,
--- "알 수 없음", or "PC 앱 v1.3.0 필요". Never "". The speaker's word is
--- `playing` alone: its mute is the media card's toggle right above, and a
--- mute in this row too would cost every mute command one more event
--- (platform notes "이벤트 예산").
function sensors.audio_summary(status, kind, lang)
  if status == nil then
    return i18n.t(lang, "privacy_unknown")
  end
  if audio_old(status, kind) then
    return features.note_text(lang, "needs_service")
  end
  if (type(status.audio) == "table" and status.audio or {}).available == false then
    return i18n.t(lang, "no_user")
  end
  local parts = {}
  local audio = reading(status)
  local device = audio and clean(audio[sensors.AUDIO_KINDS[kind].device]) or ""
  if device ~= "" then
    parts[1] = features.truncate(short_name(device), features.SLOT_NAME_MAX_CHARS)
  end
  local word
  if kind == "input" then
    local muted = sensors.audio_muted(status, kind)
    word = (muted == sensors.MUTED and "sound_muted") or (muted == sensors.UNMUTED and "sound_unmuted") or nil
  else
    local playing = sensors.speaker_playing(status)
    word = (playing == sensors.PLAYING and "sound_playing") or (playing == sensors.IDLE and "sound_idle") or nil
  end
  if word then
    parts[#parts + 1] = i18n.t(lang, word)
  end
  if #parts == 0 then
    return i18n.t(lang, "privacy_unknown")
  end
  return features.truncate(table.concat(parts, " · "), sensors.SOUND_SUMMARY_MAX_CHARS)
end

--- #160: one kind's rows, on the `sound` component - all of them, always
--- (`none` and `unknown` are not routine conditions).
function sensors.audio_events(status, kind, lang)
  local k = sensors.AUDIO_KINDS[kind]
  local c = sensors.SOUND_COMPONENT
  local events = {}
  ev(events, k.cap, "summary", sensors.audio_summary(status, kind, lang), c)
  ev(events, k.cap, "names", sensors.audio_names(status, kind, lang), c)
  ev(events, k.cap, k.attr, sensors.audio_current(status, kind), c)
  if kind == "output" then
    ev(events, k.cap, "playing", sensors.speaker_playing(status), c)
  end
  ev(events, k.cap, "muted", sensors.audio_muted(status, kind), c)
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
  append(events, sensors.privacy_events(status, opts.lang))
  append(events, sensors.audio_events(status, "output", opts.lang))
  append(events, sensors.audio_events(status, "input", opts.lang))
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
  -- #160: nothing plays on a PC that is not there either ("재생 중" must not
  -- outlive it). The slots, the names and the mutes keep their values - which
  -- device is the default, and whether it is muted, is still so when the PC
  -- comes back; only the summaries say why nothing moves.
  ev(records, sensors.CAP_SPEAKER, "summary", text, sensors.SOUND_COMPONENT)
  ev(records, sensors.CAP_SPEAKER, "playing", sensors.UNKNOWN, sensors.SOUND_COMPONENT)
  ev(records, sensors.CAP_MIC, "summary", text, sensors.SOUND_COMPONENT)
  return records
end

return sensors
