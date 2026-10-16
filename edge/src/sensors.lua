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

-- What `privacy_mode` and `monitor_mode` answer, like `features.apps_mode`.
sensors.OLD = "old"
sensors.OFF = "off"
sensors.NONE = "none"
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
-- #153: the primary monitor (DDC/CI)
--------------------------------------------------------------------------------

-- The PC's `monitor` component ("모니터"): `pcMonitor` (a summary row and the
-- input list), the standard `switch` (the monitor's power, not the PC's) and
-- `switchLevel` (brightness). The input is ours, not the standard
-- `mediaInputSource`: its enum has HDMI but no DisplayPort, DVI or VGA, and
-- the cloud checks a command's argument against the enum (platform notes
-- "상세 화면(detailView) 위젯"), so `setInputSource("dp1")` would never reach
-- the hub. The service's words are MCCS names (`hdmi1`, `dp1`, …) or
-- `vcp:0xNN` for a code MCCS does not name (USB-C on many monitors).
sensors.MONITOR_COMPONENT = "monitor"
sensors.CAP_MONITOR = caps.MONITOR
sensors.CAP_SWITCH = "switch"
sensors.CAP_LEVEL = "switchLevel"
-- `switchLevel.levelRange`: brightness is always 0-100 (the service scales
-- the monitor's VCP maximum).
sensors.LEVEL_RANGE = { minimum = 0, maximum = 100 }

-- `pcMonitor.input`: the PC monitor inputs by name, then three places for the
-- rest (`vcp:0xNN`, composite, component, …) in the monitor's own order -
-- "기타 입력 1" is the first of those the monitor lists. `unknown`: nothing
-- read, or no monitor to read; as a list argument it does nothing.
sensors.INPUT_UNKNOWN = "unknown"
sensors.NAMED_INPUTS = { "hdmi1", "hdmi2", "dp1", "dp2", "dvi1", "dvi2", "vga1", "vga2" }
sensors.OTHER_INPUTS = { "other1", "other2", "other3" }
local NAMED = {}
for _, name in ipairs(sensors.NAMED_INPUTS) do
  NAMED[name] = true
end

-- How the summary row writes a named input.
local INPUT_LABELS = {
  hdmi1 = "HDMI 1", hdmi2 = "HDMI 2", dp1 = "DP 1", dp2 = "DP 2",
  dvi1 = "DVI 1", dvi2 = "DVI 2", vga1 = "VGA 1", vga2 = "VGA 2",
}

-- The monitor name inside the summary, and the whole row (definition: 80).
sensors.MONITOR_NAME_MAX_CHARS = 24
sensors.MONITOR_SUMMARY_MAX_CHARS = 80

local function clean(text)
  if type(text) ~= "string" then
    return ""
  end
  return (text:gsub("%c", " "):gsub("^%s+", ""):gsub("%s+$", ""))
end

--- The monitor commands go to: the one marked `primary`, else the first.
function sensors.primary_monitor(status)
  local list = (status or {}).monitors
  if type(list) ~= "table" then
    return nil
  end
  local first
  for _, monitor in ipairs(list) do
    if type(monitor) == "table" then
      if monitor.primary == true then
        return monitor
      end
      first = first or monitor
    end
  end
  return first
end

--- "old" (no such feature yet), "off" (the opt-in), "none" (nothing read
--- yet, or no monitor), "on".
function sensors.monitor_mode(status)
  local set = features.parse(status)
  if set == false or features.version_below((status or {}).service_version,
      features.SINCE[features.MONITOR]) == true and not set[features.MONITOR] then
    return sensors.OLD
  end
  if not set[features.MONITOR] or type(status.monitors) ~= "table" then
    return sensors.OFF
  end
  if not sensors.primary_monitor(status) then
    return sensors.NONE
  end
  return sensors.ON
end

--- The primary monitor's inputs as list keys: `{ keys, raw = key -> service
--- word, key = service word -> key }`. The current input is in it even when
--- the capability string could not be read (`inputs` is then just it).
function sensors.input_map(monitor)
  local map = { keys = {}, raw = {}, key = {} }
  if type(monitor) ~= "table" or monitor.ddc ~= true then
    return map
  end
  local words = {}
  for _, word in ipairs(type(monitor.inputs) == "table" and monitor.inputs or {}) do
    words[#words + 1] = clean(word):lower()
  end
  local current = clean(monitor.input):lower()
  if current ~= "" then
    words[#words + 1] = current
  end
  local others = 0
  for _, word in ipairs(words) do
    if word ~= "" and not map.key[word] then
      local key
      if NAMED[word] then
        key = word
      elseif others < #sensors.OTHER_INPUTS then
        others = others + 1
        key = sensors.OTHER_INPUTS[others]
      end
      if key then
        map.key[word] = key
        map.raw[key] = word
        map.keys[#map.keys + 1] = key
      end
    end
  end
  return map
end

--- `pcMonitor.input`: the current input's key, `unknown` when not read.
function sensors.monitor_input(status)
  if sensors.monitor_mode(status) ~= sensors.ON then
    return sensors.INPUT_UNKNOWN
  end
  local monitor = sensors.primary_monitor(status)
  local current = clean(monitor.input):lower()
  return sensors.input_map(monitor).key[current] or sensors.INPUT_UNKNOWN
end

--- `pcMonitor.supportedInputs`: what the list may offer (#93's
--- `supportedValues` experiment). Never empty.
function sensors.supported_inputs(status)
  local keys = {}
  if sensors.monitor_mode(status) == sensors.ON then
    keys = sensors.input_map(sensors.primary_monitor(status)).keys
  end
  if #keys == 0 then
    keys = { sensors.INPUT_UNKNOWN }
  end
  return keys
end

--- How the summary writes an input: "HDMI 1", else the service's word.
function sensors.input_label(word)
  word = clean(word):lower()
  return INPUT_LABELS[word] or word
end

--- `pcMonitor.summary`: "HDMI 1 · 40% · DELL U2720Q", "대기 · DELL U2720Q",
--- "지원 안 함 (DDC/CI) · LG TV", "모니터 정보 없음", "모니터 제어 꺼짐", or
--- "PC 앱 v1.3.0 필요". The input first: the row is the first cell of the
--- card's preview. Never "".
function sensors.monitor_summary(status, lang)
  if status == nil then
    return i18n.t(lang, "monitor_none")
  end
  local mode = sensors.monitor_mode(status)
  if mode == sensors.OLD then
    return features.note_text(lang, "needs_service")
  end
  if mode == sensors.OFF then
    return i18n.t(lang, "monitor_disabled")
  end
  if mode == sensors.NONE then
    return i18n.t(lang, "monitor_none")
  end
  local monitor = sensors.primary_monitor(status)
  local parts = {}
  if monitor.ddc ~= true then
    parts[1] = i18n.t(lang, "monitor_no_ddc")
  elseif monitor.power == "standby" then
    parts[1] = i18n.t(lang, "monitor_standby")
  else
    if clean(monitor.input) ~= "" then
      parts[#parts + 1] = sensors.input_label(monitor.input)
    end
    local brightness = tonumber(monitor.brightness)
    if brightness then
      parts[#parts + 1] = string.format("%d%%", math.floor(brightness + 0.5))
    end
  end
  local name = clean(monitor.name)
  if name ~= "" then
    parts[#parts + 1] = features.truncate(name, sensors.MONITOR_NAME_MAX_CHARS)
  end
  if #parts == 0 then
    return i18n.t(lang, "monitor_none")
  end
  return features.truncate(table.concat(parts, " · "), sensors.MONITOR_SUMMARY_MAX_CHARS)
end

--- The monitor's power as the `switch` value, nil when not read.
function sensors.monitor_switch(status)
  if sensors.monitor_mode(status) ~= sensors.ON then
    return nil
  end
  local monitor = sensors.primary_monitor(status)
  if monitor.ddc ~= true then
    return nil
  end
  if monitor.power == "on" then
    return "on"
  end
  if monitor.power == "standby" then
    return "off"
  end
  return nil
end

--- The brightness, 0-100, nil when not read.
function sensors.monitor_level(status)
  if sensors.monitor_mode(status) ~= sensors.ON then
    return nil
  end
  local monitor = sensors.primary_monitor(status)
  local brightness = monitor.ddc == true and tonumber(monitor.brightness) or nil
  if not brightness then
    return nil
  end
  return math.max(0, math.min(100, math.floor(brightness + 0.5)))
end

--- #153: the monitor card. The switch and the slider move only on a reading
--- - an opt-in switched off is not a monitor switched off (a routine on
--- "꺼지면" would fire); a repaint (`fill`) paints "off" and 0 where there is
--- none, since a row never sent reads "-".
function sensors.monitor_events(status, lang, fill)
  local events = {}
  local c = sensors.MONITOR_COMPONENT
  ev(events, sensors.CAP_MONITOR, "summary", sensors.monitor_summary(status, lang), c)
  ev(events, sensors.CAP_MONITOR, "input", sensors.monitor_input(status), c)
  ev(events, sensors.CAP_MONITOR, "supportedInputs", sensors.supported_inputs(status), c)
  local switch = sensors.monitor_switch(status)
  if switch == nil and fill then
    switch = "off"
  end
  if switch then
    ev(events, sensors.CAP_SWITCH, "switch", switch, c)
  end
  local level = sensors.monitor_level(status)
  if level == nil and fill then
    level = 0
  end
  if level then
    ev(events, sensors.CAP_LEVEL, "level", level, c)
    -- The slider's range: optional in the definition, but a row the device
    -- never sends stays null in the cloud record (platform notes "null 속성").
    ev(events, sensors.CAP_LEVEL, "levelRange", sensors.LEVEL_RANGE, c)
  end
  return events
end

--------------------------------------------------------------------------------
-- #154: the audio device slots
--------------------------------------------------------------------------------

-- The PC's `audio` component ("오디오 장치"): `pcAudioOut` and `pcAudioIn`,
-- each a names row ("1 스피커 · 3 헤드셋"), a list of slots 1-5 resting on
-- the slot of the default device, and a routine condition and action on the
-- slot. Device names differ on every PC and a presentation is static, so the
-- list names slots, like the presets (`pcPreset`) and the watch list. Two
-- capabilities, not one: one state row each is drawn full width, two of one
-- capability half width and cut (platform notes "화면 배치").
sensors.AUDIO_COMPONENT = "audio"
sensors.SLOT_NONE = "none"
sensors.AUDIO_SLOTS = 5

-- `status.audio.outputs` / `inputs` -> the capability, its rows, the
-- feature and the word for the notes ("출력 2 비어 있음").
sensors.AUDIO_KINDS = {
  output = {
    cap = caps.AUDIO_OUT, attr = "output", supported = "supportedOutputs", list = "outputs",
    feature = features.AUDIO_OUTPUT, command = "audio_output", word = "audio_out",
  },
  input = {
    cap = caps.AUDIO_IN, attr = "input", supported = "supportedInputs", list = "inputs",
    feature = features.AUDIO_INPUT, command = "audio_input", word = "audio_in",
  },
}

-- The names row, like the watch list's (definition: 120).
sensors.AUDIO_NAMES_MAX_CHARS = 120

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

--- True when the PC app is older than the release that brought the slots.
local function audio_old(status, kind)
  local set = features.parse(status)
  return set == false or (features.version_below((status or {}).service_version,
    features.SINCE[sensors.AUDIO_KINDS[kind].feature]) == true
    and not set[sensors.AUDIO_KINDS[kind].feature])
end

--- The slot of the default device as a list key, `none` when it is in none.
function sensors.audio_current(status, kind)
  for _, entry in ipairs(sensors.audio_slots(status, kind)) do
    if entry.default then
      return tostring(entry.slot)
    end
  end
  return sensors.SLOT_NONE
end

--- What the slot list may offer (#93's `supportedValues`). Never empty.
function sensors.audio_supported(status, kind)
  local out = {}
  for _, entry in ipairs(sensors.audio_slots(status, kind)) do
    out[#out + 1] = tostring(entry.slot)
  end
  if #out == 0 then
    out[1] = sensors.SLOT_NONE
  end
  return out
end

--- A Windows endpoint name is "<device> (<adapter>)": the device alone when
--- that tells the slots apart, else the whole name.
local function short_names(slots)
  local count = {}
  local short = {}
  for i, entry in ipairs(slots) do
    short[i] = entry.name:gsub("%s*%b()$", "")
    if short[i] == "" then
      short[i] = entry.name
    end
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

--- #154: one kind's rows. All three, always: `none` is not a routine
--- condition.
function sensors.audio_events(status, kind, lang)
  local k = sensors.AUDIO_KINDS[kind]
  local events = {}
  ev(events, k.cap, "names", sensors.audio_names(status, kind, lang), sensors.AUDIO_COMPONENT)
  ev(events, k.cap, k.attr, sensors.audio_current(status, kind), sensors.AUDIO_COMPONENT)
  ev(events, k.cap, k.supported, sensors.audio_supported(status, kind), sensors.AUDIO_COMPONENT)
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
  append(events, sensors.monitor_events(status, opts.lang, opts.fill))
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
  -- #153: the monitor's switch and slider keep their values (a routine may
  -- read them); only the summary says why nothing moves.
  ev(records, sensors.CAP_MONITOR, "summary", text, sensors.MONITOR_COMPONENT)
  return records
end

return sensors
