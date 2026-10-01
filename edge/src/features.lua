-- The v1.2.0 additions of the service (docs/design/media-notify.md): which of
-- them a PC offers, what their status blocks become on screen, and whether a
-- command that needs one may go out at all.
--
-- Pure, like state.lua: nothing here touches st.* or cosock. `apply_status`
-- returns the same `{ cap, attr, value }` records state.apply_status does (and
-- is called from it), `refusal` / `error_note` return i18n keys, and init.lua
-- does the sending.
--
-- #107: volume, mute and the media keys, on the STANDARD capabilities
-- `audioVolume`, `audioMute`, `mediaPlayback` and `mediaTrackControl`. A
-- standard capability has no definition cache problem (platform notes "허브의
-- 정의 캐시") and brings the app's own slider, toggle and buttons.

local caps = require "caps"
local i18n = require "i18n"

local features = {}

-- The names `status.features` carries (media-notify.md §3). An older service
-- sends no `features` key at all, which is how the driver tells "too old" from
-- "this PC does not offer it".
features.AUDIO = "audio"
features.MEDIA = "media"
-- #118: listed while the PC's "재생 정보 공유" opt-in is on (§15).
features.NOWPLAYING = "nowplaying"
features.PRESETS = "presets"
-- #114: listed only while the PC's opt-in is on (service #110).
features.ACTIVITY = "activity"
-- #115: keep-awake, always listed by a v1.2.0 service.
features.AWAKE = "awake"

-- #115: the component the keep-awake switch lives on, and its row key.
features.AWAKE_COMPONENT = "awake"
features.CAP_SWITCH = "switch"
-- The `awakeMinutes` preference: default and the service's bounds (§12).
features.AWAKE_DEFAULT_MINUTES = 60
features.AWAKE_MAX_MINUTES = 1440

-- #108: a toast on the PC, `POST /st/v1/notify`.
features.NOTIFY = "notify"
-- Our own `pcNotify` (`send(text)`) since the standard `notification` showed
-- Samsung's label ("텍스트 표시") that a device configuration cannot override
-- (platform notes "표준 capability").
features.CAP_NOTIFY = caps.NOTIFY
-- The service takes 1-200 characters (§3). `pcNotify` declares the same 200
-- (`maxLength`, so the cloud stops a longer text before the hub), and the
-- driver cuts anyway, after cleaning.
features.NOTIFY_MAX_CHARS = 200

-- #116: the laptop battery, on a component only the `-battery` profiles have.
features.BATTERY = "battery"
features.BATTERY_COMPONENT = "battery"
features.CAP_BATTERY = "battery"
features.CAP_POWER_SOURCE = "powerSource"

-- Standard capability ids.
features.CAP_VOLUME = "audioVolume"
features.CAP_MUTE = "audioMute"
features.CAP_PLAYBACK = "mediaPlayback"
features.CAP_TRACK = "mediaTrackControl"

-- #118: what is playing (title/artist/album).
features.CAP_TRACK_DATA = "audioTrackData"

-- #118: the media group, and the component it moves to in the alternative
-- profile layout (`gen-profiles.js --media-component`). poll.emit sends these
-- capabilities' events to that component when the device's profile has it.
features.MEDIA_COMPONENT = "media"
features.MEDIA_CAPS = {
  [features.CAP_TRACK_DATA] = true, [features.CAP_PLAYBACK] = true,
  [features.CAP_TRACK] = true, [features.CAP_VOLUME] = true, [features.CAP_MUTE] = true,
}

-- What the media rows offer. Constant: the service handles each of them
-- whatever is playing.
features.PLAYBACK_COMMANDS = { "play", "pause", "stop" }
features.TRACK_COMMANDS = { "nextTrack", "previousTrack" }

-- #107, 실측 대기: what `mediaPlayback.playbackStatus` rests on while the
-- service does not say (no `media` block: a service without #117, or media
-- control off). nil = not emitted at all, as media-notify.md §5 planned. If the
-- Dev channel shows the media row as "-", or play/pause spinning into an
-- error, set this to "stopped": the driver then emits it in the initial rows
-- and as the answer to a media command when nothing better is known.
-- #118: with `status.media.status` the real value goes out instead.
features.PLAYBACK_RESTING = nil

-- #118: `status.media.status` -> `playbackStatus`. "none" (no media session
-- at all) reads as stopped.
local PLAYBACK_STATUS = { playing = "playing", paused = "paused", stopped = "stopped", none = "stopped" }

-- service command -> the feature it needs (media-notify.md §3).
features.COMMAND_FEATURE = {
  volume = features.AUDIO, volumeup = features.AUDIO, volumedown = features.AUDIO,
  mute = features.AUDIO, unmute = features.AUDIO,
  play = features.MEDIA, pause = features.MEDIA, playpause = features.MEDIA,
  stop = features.MEDIA, next = features.MEDIA, prev = features.MEDIA,
  preset = features.PRESETS,
  awake = features.AWAKE, awakeoff = features.AWAKE,
}

-- #113: the slots a preset list can offer, and the value it rests on. Strings,
-- because the list's argument is a string enum (platform notes "상세
-- 화면(detailView) 위젯": a dismissed list sends the row's current value
-- without the presentation's integer conversion).
features.PRESET_NONE = "none"
features.PRESET_SLOTS = { "1", "2", "3", "4", "5", "6", "7", "8", "9", "10" }

-- Features that act in the logged-in user's session (§2). `audio.available`
-- is the service's word on whether there is one.
local NEEDS_USER = { [features.AUDIO] = true, [features.MEDIA] = true }

local function ev(list, cap, attr, value, component)
  list[#list + 1] = { cap = cap, attr = attr, value = value, component = component }
end

local function copy_list(list)
  local out = {}
  for i, v in ipairs(list) do
    out[i] = v
  end
  return out
end

--------------------------------------------------------------------------------
-- what the PC offers
--------------------------------------------------------------------------------

--- `status.features` as a set, or `false` for a service that sends no such key
--- (older than v1.2.0).
function features.parse(status)
  local list = (status or {}).features
  if type(list) ~= "table" then
    return false
  end
  local set = {}
  for _, name in ipairs(list) do
    if type(name) == "string" and name ~= "" then
      set[name] = true
    end
  end
  return set
end

--- Remember what a status body says about the v1.2.0 features on the runtime
--- state, as `device_state.extras`. Mutates and returns `device_state`, the
--- freshly copied one `state.transition` handed back.
--
-- A command handler reads this back: the command it is about to send needs a
-- feature, and the answer is in the last status, not in a second request.
-- Nothing is remembered from a body that is not a table (a push without a
-- status block), so the last answer stands.
function features.remember(device_state, status)
  device_state = device_state or {}
  if type(status) ~= "table" then
    return device_state
  end
  local audio = type(status.audio) == "table" and status.audio or {}
  local muted
  if type(audio.muted) == "boolean" then
    muted = audio.muted
  end
  local slots = {}
  for _, preset in ipairs(features.presets_of(status)) do
    slots[tostring(preset.slot)] = true
  end
  device_state.extras = {
    features = features.parse(status),
    audio = {
      available = audio.available,
      volume = features.volume_of(audio),
      muted = muted,
    },
    -- #113: which slots the PC has a preset in, so a routine that runs an
    -- empty one is told so instead of being sent to the service.
    preset_slots = slots,
    -- #115: what the keep-awake switch should spring back to when a command
    -- is not sent.
    awake_on = features.awake_on(status),
    -- #118: and what the play/pause row does.
    playback = features.playback_status(status),
    -- The body itself, so a repaint (poll.repaint) paints these rows with
    -- what the PC last said instead of the never-polled defaults - a forced
    -- "off" on the keep-awake switch would fire every routine that watches it.
    last_status = status,
  }
  return device_state
end

--- True when the remembered status says the PC offers `feature`.
function features.has(extras, feature)
  local set = (extras or {}).features
  return type(set) == "table" and set[feature] == true
end

--------------------------------------------------------------------------------
-- may a command go out
--------------------------------------------------------------------------------

--- nil when `service_command` may be sent, else the i18n key of the note the
--- user is shown instead (media-notify.md §5).
--
--   unreachable      no status has been read in this driver run at all
--   needs_service    the service sends no `features` - older than v1.2.0
--   feature_missing  a v1.2.0 service that does not list this feature
--   no_user          `audio.available = false`: nobody is logged in, and
--                    volume and media keys only mean something in a session
--
-- A command that needs no feature (anything not in COMMAND_FEATURE) is
-- always allowed here.
function features.refusal(extras, service_command, feature)
  feature = feature or features.COMMAND_FEATURE[tostring(service_command or "")]
  if not feature then
    return nil
  end
  if extras == nil then
    return "unreachable"
  end
  if extras.features == false or extras.features == nil then
    return "needs_service"
  end
  if not features.has(extras, feature) then
    -- A v1.2.0 service lists "audio" and "media" only while `media.enabled`
    -- is on (service #104/#105), so for those two a missing entry IS the
    -- setting.
    if feature == features.AUDIO or feature == features.MEDIA then
      return "media_disabled"
    end
    return "feature_missing"
  end
  if NEEDS_USER[feature] and ((extras.audio or {}).available == false) then
    return "no_user"
  end
  return nil
end

--- The note for a v1.2.0 command the service refused, or nil when the failure
--- is an ordinary one (unreachable, secret, …) that `report_error` handles.
--
-- The service answers `{"error": "<code>"}`: `409 no_user_session` when
-- nobody is logged in, `403 media_disabled` / `403 notify_disabled` when the
-- PC's settings turned the feature off. A 403 WITHOUT one of those codes is
-- still the hub allow-list (§3.1) and stays with `report_error`.
function features.error_note(kind, body)
  local code = type(body) == "table" and body.error or nil
  if code == "no_user_session" or kind == "conflict" then
    return "no_user"
  end
  if code == "media_disabled" then
    return "media_disabled"
  end
  if code == "notify_disabled" then
    return "notify_disabled"
  end
  -- Service #104/#105: the action ran in the user's session and did not work.
  -- `501 unsupported` (no playback device), `502 failed`, `504 timeout` - the
  -- PC answered, so none of them is an unreachable PC, which is what
  -- `client.classify` would make of a 5xx.
  if code == "unsupported" then
    return "feature_missing"
  end
  if code == "failed" or code == "timeout" then
    return "action_failed"
  end
  return nil
end

--------------------------------------------------------------------------------
-- status -> rows
--------------------------------------------------------------------------------

--- The volume in an `audio` block, 0-100, or nil.
function features.volume_of(audio)
  local volume = tonumber((audio or {}).volume)
  if not volume then
    return nil
  end
  volume = math.floor(volume + 0.5)
  if volume < 0 then
    volume = 0
  elseif volume > 100 then
    volume = 100
  end
  return volume
end

--- True when an `audio` block carries a reading rather than placeholders:
--- either the service says there is a session to read from, or it has a time
--- for its last reading (a heartbeat or a command result, §3).
local function has_reading(audio)
  if type(audio) ~= "table" then
    return false
  end
  if audio.available == true then
    return true
  end
  return type(audio.updated_at) == "string" and audio.updated_at ~= ""
end

--- #107: `audio` -> `audioVolume.volume` and `audioMute.mute`.
--
-- Nothing is emitted from a block without a reading: a volume of 0 that the
-- service never measured would move the slider to the bottom for no reason,
-- and a PC nobody has logged in to since boot has nothing to say yet.
function features.audio_events(status)
  local events = {}
  local audio = (status or {}).audio
  if not has_reading(audio) then
    return events
  end
  local volume = features.volume_of(audio)
  if volume then
    ev(events, features.CAP_VOLUME, "volume", volume)
  end
  if type(audio.muted) == "boolean" then
    ev(events, features.CAP_MUTE, "mute", audio.muted and "muted" or "unmuted")
  end
  return events
end

--- #118: `playbackStatus` from `status.media.status`, or nil when the status
--- does not say (no `media` block, or a value this driver does not know).
function features.playback_status(status)
  local media = (status or {}).media
  if type(media) ~= "table" then
    return nil
  end
  return PLAYBACK_STATUS[tostring(media.status or "")]
end

--- #107: the media rows' constant attributes, and #118 the playback status.
--- Emitted with every status (the hub drops an unchanged value) and in the
--- initial rows, so a device that was just migrated has its buttons before the
--- first poll.
-- @param playback a `playbackStatus` value, or nil for "not known" (then
--   PLAYBACK_RESTING, if set)
function features.media_events(playback)
  local events = {}
  ev(events, features.CAP_PLAYBACK, "supportedPlaybackCommands", copy_list(features.PLAYBACK_COMMANDS))
  playback = playback or features.PLAYBACK_RESTING
  if playback then
    ev(events, features.CAP_PLAYBACK, "playbackStatus", playback)
  end
  ev(events, features.CAP_TRACK, "supportedTrackControlCommands", copy_list(features.TRACK_COMMANDS))
  return events
end

-- #118: the track fields worth a row, and how long each may be.
features.TRACK_FIELDS = { "title", "artist", "album" }
features.TRACK_MAX_CHARS = 128

--- #118: `audioTrackData.audioTrackData` - `{ title, artist, album }` of what
--- is playing, or nil when nothing is to be said.
--
-- Only with the "재생 정보 공유" opt-in (`features` has "nowplaying") and a
-- title in `status.media`. A field the PC did not send is left out rather than
-- sent as "" (platform notes: an empty value reads as "-"). A PC that has a
-- `media` block but no title says so in words, so a title that stopped
-- playing - or an opt-in that was switched off - does not stay on screen:
-- "재생 중인 미디어 없음" with the opt-in on, "재생 정보 꺼짐" with it off. A
-- service without the block (older than #117) gets nothing at all.
function features.track_data(status, lang)
  local media = (status or {}).media
  if type(media) ~= "table" then
    return nil
  end
  local sharing = features.has({ features = features.parse(status) }, features.NOWPLAYING)
  local title = sharing and type(media.title) == "string" and media.title:gsub("%c", " ") or ""
  if title:match("^%s*$") then
    return { title = i18n.t(lang, sharing and "track_none" or "track_off") }
  end
  local data = {}
  for _, field in ipairs(features.TRACK_FIELDS) do
    local value = media[field]
    if type(value) == "string" then
      value = value:gsub("%c", " "):gsub("^%s+", ""):gsub("%s+$", "")
      if value ~= "" then
        data[field] = features.truncate(value, features.TRACK_MAX_CHARS)
      end
    end
  end
  return data
end

function features.track_events(status, lang)
  local events = {}
  local data = features.track_data(status, lang)
  if data then
    ev(events, features.CAP_TRACK_DATA, "audioTrackData", data)
    -- The service does not report durations. Left unset, both rows are null in
    -- the cloud record and the app warns that the device does not report all
    -- its state (measured, Dev channel 2026-09-30), so they rest on 0.
    ev(events, features.CAP_TRACK_DATA, "totalTime", 0)
    ev(events, features.CAP_TRACK_DATA, "elapsedTime", 0)
  end
  return events
end

--------------------------------------------------------------------------------
-- #113: presets
--------------------------------------------------------------------------------

--- UTF-8 code points of `text`, at most `max` of them; a cut string ends in
--- "…" (which counts). Used wherever a row or the service has a length limit.
function features.truncate(text, max)
  text = tostring(text or "")
  local points = {}
  -- A lead byte and its continuation bytes. NUL never reaches here: every
  -- caller has stripped control characters, and no status string carries one.
  for point in text:gmatch("[\1-\127\194-\244][\128-\191]*") do
    points[#points + 1] = point
  end
  if #points <= max then
    return text
  end
  return table.concat(points, "", 1, math.max(0, max - 1)) .. "…"
end

--- `status.presets` as a list of `{ slot = 1..10, name = "…" }`, by slot, with
--- anything malformed left out. The service sends only slot and name - what a
--- preset runs stays on the PC (media-notify.md §10).
function features.presets_of(status)
  local out, seen = {}, {}
  local list = (status or {}).presets
  if type(list) ~= "table" then
    return out
  end
  for _, preset in ipairs(list) do
    local slot = type(preset) == "table" and tonumber(preset.slot) or nil
    if slot and slot == math.floor(slot) and slot >= 1 and slot <= 10 and not seen[slot] then
      seen[slot] = true
      local name = type(preset.name) == "string" and preset.name or ""
      out[#out + 1] = { slot = math.floor(slot), name = name }
    end
  end
  table.sort(out, function(a, b) return a.slot < b.slot end)
  return out
end

-- The names row is one line the phone truncates anyway; the definition allows
-- 255 characters and this stays well inside it.
features.NAMES_MAX_CHARS = 200

--- `pcPreset.names`: "1 게임 모드 · 2 방송 시작", or "없음" when the PC has no
--- preset. A service older than v1.2.0 gets "서비스 v1.2.0 필요" - the row is
--- there on every v2 screen and has to say why it is empty. Never "": an empty
--- state row reads "-" (platform notes "상세 화면(detailView) 위젯").
function features.preset_names(status, lang)
  if features.parse(status) == false then
    return i18n.t(lang, "needs_service")
  end
  local parts = {}
  for _, preset in ipairs(features.presets_of(status)) do
    local name = preset.name ~= "" and preset.name or i18n.t(lang, "preset_unnamed")
    parts[#parts + 1] = tostring(preset.slot) .. " " .. name
  end
  if #parts == 0 then
    return i18n.t(lang, "presets_none")
  end
  return features.truncate(table.concat(parts, " · "), features.NAMES_MAX_CHARS)
end

--- `pcPreset.supportedSlots`: the slots the list may offer (#93's
--- `supportedValues` experiment, platform notes). Never empty - the community
--- reports that an empty array brings the whole list back - so a PC without a
--- preset gets `none`, which is not a menu entry: the hoped-for effect is a
--- list with nothing to pick.
function features.supported_slots(status)
  local out = {}
  for _, preset in ipairs(features.presets_of(status)) do
    out[#out + 1] = tostring(preset.slot)
  end
  if #out == 0 then
    out[1] = features.PRESET_NONE
  end
  return out
end

--- True when `slot` is a key of the preset list ("1".."10").
function features.is_preset_slot(slot)
  for _, key in ipairs(features.PRESET_SLOTS) do
    if key == slot then
      return true
    end
  end
  return false
end

--- #113: the preset rows a status body carries. `lastPreset` is not one of
--- them - it is the list's resting value, and poll.lua owns it like
--- `lastAction` (poll.ensure_preset).
function features.preset_events(status, lang)
  local events = {}
  ev(events, caps.PRESET, "names", features.preset_names(status, lang))
  ev(events, caps.PRESET, "supportedSlots", features.supported_slots(status))
  return events
end

--------------------------------------------------------------------------------
-- #114: activity
--------------------------------------------------------------------------------

-- `pcActivity.activity`, the routine condition ("활동이 게임"). `none` when
-- nothing on the watch list runs and when the opt-in is off.
features.ACTIVITY_KINDS = { "none", "game", "work", "media", "stream", "other" }

-- The summary row, like `pcInfo.summary`, is cut by the phone without a word
-- (platform notes "화면 배치"), so it is built to fit this many code points.
features.ACTIVITY_MAX_CHARS = 24

local function char_len(s)
  return #(tostring(s):gsub("[\128-\191]", ""))
end

local function is_kind(kind)
  for _, k in ipairs(features.ACTIVITY_KINDS) do
    if k == kind then
      return true
    end
  end
  return false
end

--- True when the status says the watch list is on: the block says so and
--- the service lists the feature (it does only while the opt-in is on). An
--- older service has neither.
function features.activity_enabled(status)
  local block = (status or {}).activity
  if type(block) ~= "table" or block.enabled ~= true then
    return false
  end
  return features.has({ features = features.parse(status) }, features.ACTIVITY)
end

--- The `activity` enum value of a status body. A kind this driver does not
--- know (a newer service) is `other` rather than dropped: something IS running.
function features.activity_kind(status)
  if not features.activity_enabled(status) then
    return "none"
  end
  local kind = tostring(((status or {}).activity or {}).kind or "none")
  if is_kind(kind) then
    return kind
  end
  return "other"
end

--- `pcActivity.summary`: "게임 중 · Steam", "없음" when nothing on the watch
--- list runs, "꺼짐" when the opt-in is off (or the service is too old to have
--- one). Labels are dropped from the end until the line fits: all of them,
--- then the first with "외 N", then the first alone, then the word alone.
function features.activity_summary(status, lang)
  if not features.activity_enabled(status) then
    return i18n.t(lang, "activity_off")
  end
  local kind = features.activity_kind(status)
  if kind == "none" then
    return i18n.t(lang, "activity_none")
  end
  local word = i18n.t(lang, "activity_" .. kind)
  local labels = {}
  for _, label in ipairs(((status or {}).activity or {}).labels or {}) do
    if type(label) == "string" and label ~= "" then
      labels[#labels + 1] = label
    end
  end
  local candidates = {}
  if #labels > 0 then
    candidates[#candidates + 1] = word .. " · " .. table.concat(labels, ", ")
    if #labels > 1 then
      candidates[#candidates + 1] = word .. " · " .. i18n.t(lang, "activity_more", labels[1], #labels - 1)
    end
    candidates[#candidates + 1] = word .. " · " .. labels[1]
  end
  candidates[#candidates + 1] = word
  for _, line in ipairs(candidates) do
    if char_len(line) <= features.ACTIVITY_MAX_CHARS then
      return line
    end
  end
  return word
end

--- #114: the activity rows of a status body.
function features.activity_events(status, lang)
  local events = {}
  ev(events, caps.ACTIVITY, "activity", features.activity_kind(status))
  ev(events, caps.ACTIVITY, "summary", features.activity_summary(status, lang))
  return events
end

--------------------------------------------------------------------------------
-- #115: keep-awake
--------------------------------------------------------------------------------

--- True when the status says keep-awake is on. A service without the block
--- (older than v1.2.0) cannot keep the PC awake, so that is "off" too.
function features.awake_on(status)
  local awake = (status or {}).awake
  return type(awake) == "table" and awake.on == true
end

--- The minutes an `awake` command asks for: the `awakeMinutes` preference,
--- 0 (until switched off) to 1440, default 60.
function features.awake_minutes(preferences)
  local minutes = tonumber((preferences or {}).awakeMinutes)
  if not minutes then
    return features.AWAKE_DEFAULT_MINUTES
  end
  minutes = math.floor(minutes)
  if minutes < 0 then
    return 0
  end
  if minutes > features.AWAKE_MAX_MINUTES then
    return features.AWAKE_MAX_MINUTES
  end
  return minutes
end

--- #115: the `awake` component's switch.
function features.awake_events(status)
  local events = {}
  ev(events, features.CAP_SWITCH, "switch", features.awake_on(status) and "on" or "off",
    features.AWAKE_COMPONENT)
  return events
end

--------------------------------------------------------------------------------
-- #108: PC notifications
--------------------------------------------------------------------------------

--- The text a notify request carries: control characters (C0 and DEL) turned
--- into spaces, runs of white space folded, the ends trimmed, and at most
--- `NOTIFY_MAX_CHARS` code points (a cut text ends in "…"). nil when nothing
--- is left - the service refuses an empty text, so it is not sent at all.
function features.notify_text(text)
  if type(text) ~= "string" then
    return nil
  end
  text = text:gsub("[%c\127]", " "):gsub("%s+", " "):gsub("^ ", ""):gsub(" $", "")
  if text == "" then
    return nil
  end
  return features.truncate(text, features.NOTIFY_MAX_CHARS)
end

--- The note for a notify request the service refused, or nil for an ordinary
--- failure. On top of `error_note`: `429` is the service's per-source limit
--- (ten a minute, §3), which for a notification is worth saying.
function features.notify_error_note(kind, body)
  if kind == "ratelimited" then
    return "try_later"
  end
  return features.error_note(kind, body)
end

--------------------------------------------------------------------------------
-- #116: battery
--------------------------------------------------------------------------------

--- True when the status says this PC has a battery (§13). An older service
--- has no block, which reads as "no battery" - the plain profile.
function features.battery_present(status)
  local battery = (status or {}).battery
  return type(battery) == "table" and battery.present == true
end

--- #116: `battery.battery` (percent, left out while the service does not know
--- it: -1) and `powerSource.powerSource` ("mains" on AC, else "battery") on the
--- `battery` component. Nothing at all for a PC without a battery - its
--- profile has no such component, and a desktop must not grow an empty card.
function features.battery_events(status)
  local events = {}
  if not features.battery_present(status) then
    return events
  end
  local battery = status.battery
  local percent = tonumber(battery.percent)
  if percent and percent >= 0 then
    percent = math.floor(percent + 0.5)
    if percent > 100 then
      percent = 100
    end
    ev(events, features.CAP_BATTERY, "battery", percent, features.BATTERY_COMPONENT)
  end
  ev(events, features.CAP_POWER_SOURCE, "powerSource", battery.ac == true and "mains" or "battery",
    features.BATTERY_COMPONENT)
  return events
end

local function append(into, list)
  for _, e in ipairs(list) do
    into[#into + 1] = e
  end
  return into
end

--- Every v1.2.0 row a status body paints. Called by state.apply_status.
-- @param opts `lang`
function features.apply_status(status, opts)
  local lang = (opts or {}).lang
  local events = {}
  -- #118: in the order of the media group on screen.
  append(events, features.track_events(status, lang))
  append(events, features.media_events(features.playback_status(status)))
  append(events, features.audio_events(status))
  append(events, features.preset_events(status, lang))
  append(events, features.activity_events(status, lang))
  append(events, features.awake_events(status))
  append(events, features.battery_events(status))
  return events
end

--- Every v1.2.0 row a device that has never been polled paints (state.initial_rows).
function features.initial_rows(lang)
  local events = features.media_events()
  -- #113: before the first status there is no list of presets to show.
  ev(events, caps.PRESET, "names", i18n.t(lang, "presets_none"))
  ev(events, caps.PRESET, "supportedSlots", { features.PRESET_NONE })
  -- #114: nothing known yet, which reads as "nothing running".
  ev(events, caps.ACTIVITY, "activity", "none")
  ev(events, caps.ACTIVITY, "summary", i18n.t(lang, "activity_none"))
  -- #115: a service that has just started has keep-awake off (it does not
  -- carry the period over a restart, §12), so "off" is the honest default.
  ev(events, features.CAP_SWITCH, "switch", "off", features.AWAKE_COMPONENT)
  return events
end

return features
