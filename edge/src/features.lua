-- The v1.2.0 additions of the service (docs/design/media-notify.md): which of
-- them a PC offers, what their status blocks become on screen, and whether a
-- command that needs one may go out at all.
--
-- Pure, like model/: nothing here touches st.* or cosock. `apply_status`
-- returns the same `{ cap, attr, value }` records model/status.lua does (and
-- is called from it), `refusal` / `error_note` return i18n keys, and the handlers
-- (handlers/) do the sending.
--
-- #107: volume, mute and the media keys, on the STANDARD capabilities
-- `audioVolume`, `audioMute`, `mediaPlayback` and `mediaTrackControl`. A
-- standard capability has no definition cache problem (platform notes "허브의
-- 정의 캐시") and brings the app's own slider, toggle and buttons.

local caps = require "caps"
local i18n = require "i18n"

local features = {}

--------------------------------------------------------------------------------
-- which service (PC app) this driver wants (edge-driver.md "버전 짝 맞춤")
--------------------------------------------------------------------------------

-- The protocol floor: the first service release that speaks protocol 1 (§3).
-- Below it the driver cannot talk to the PC at all, and says so
-- (`incompatible_service`, poll.message_for). poll.MIN_SERVICE_VERSION is this.
features.MIN_SERVICE_VERSION = "1.1.0"

-- The service release whose features this driver uses. A PC below it still
-- works - power, schedules - but what is newer is refused with "PC 앱 v%s
-- 필요", and the summary and message rows nudge the user to update the PC
-- app (`needs_app_update`).
-- 드라이버가 새 서비스 기능을 쓰기 시작하면 이 값을 그 서비스 버전으로 올린다.
features.RECOMMENDED_SERVICE_VERSION = "1.2.0"

--- "v1.2.0", "1.2.0", "v1.2.0-rc14" -> { 1, 2, 0 }: major.minor.patch only,
--- so a prerelease counts as its release (our rc builds do not nag). A
--- missing patch reads as 0. Nil for anything else ("dev", "", nil).
function features.parse_version(v)
  if type(v) ~= "string" then
    return nil
  end
  local major, minor, rest = v:match("^%s*[vV]?(%d+)%.(%d+)(.*)$")
  if not major then
    return nil
  end
  local patch = rest:match("^%.(%d+)") or "0"
  return { tonumber(major), tonumber(minor), tonumber(patch) }
end

--- True when version `a` is older than `b`, false when not, nil when either
--- does not parse (`parse_version`).
function features.version_below(a, b)
  local x, y = features.parse_version(a), features.parse_version(b)
  if not x or not y then
    return nil
  end
  for i = 1, 3 do
    if x[i] ~= y[i] then
      return x[i] < y[i]
    end
  end
  return false
end

--- True when the PC's service is older than RECOMMENDED_SERVICE_VERSION, by
--- the status body's `service_version`. A version that does not parse ("dev",
--- missing) is not flagged - unless the body has no `features` at all, which
--- only a service older than v1.2.0 sends.
function features.needs_app_update(status)
  if type(status) ~= "table" then
    return false
  end
  local below = features.version_below(status.service_version, features.RECOMMENDED_SERVICE_VERSION)
  if below ~= nil then
    return below
  end
  return type(status.features) ~= "table"
end

--- The sentence for a note key (`refusal`, `error_note`, …): `needs_service`
--- names RECOMMENDED_SERVICE_VERSION, any other key is formatted with `...`.
function features.note_text(lang, key, ...)
  if key == "needs_service" then
    return i18n.t(lang, key, features.RECOMMENDED_SERVICE_VERSION)
  end
  return i18n.t(lang, key, ...)
end

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
-- Our own `pcToast` (`send(text)` + `lastMessage`) since the standard
-- `notification` showed Samsung's label ("텍스트 표시") that a device
-- configuration cannot override (platform notes "표준 capability").
features.CAP_TOAST = caps.TOAST
-- The service takes 1-200 characters (§3). `pcToast` declares the same 200
-- (`maxLength`, so the cloud stops a longer text before the hub), and the
-- driver cuts anyway, after cleaning. `lastMessage` holds what was sent, so
-- it has the same limit.
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
-- profile layout (`gen-profiles.js --media-component`). emit.rows sends these
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
  -- #123: read before `extras` is replaced - the slots it showed are what a
  -- list edit holds and an "off" list keeps (`watch_state`).
  local watch, watch_signature = features.watch_state(status, device_state.extras)
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
    -- #123: the watch list, by slot, and the five slot values the card shows
    -- (`watch_state`: a list edit holds "running", an "off" list keeps them).
    apps_mode = features.apps_mode(status),
    apps = features.apps_of(status),
    watch = watch,
    watch_signature = watch_signature,
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
--   needs_service    the service sends no `features` - older than v1.2.0 -
--                    or does not list this feature while it is older than
--                    RECOMMENDED_SERVICE_VERSION (a feature of a newer release)
--   feature_missing  a new enough service that does not list this feature
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
    if features.version_below((extras.last_status or {}).service_version,
        features.RECOMMENDED_SERVICE_VERSION) == true then
      return "needs_service"
    end
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

-- One slot's name inside a names row (presets and the watch list): twelve
-- characters, a longer one keeps twelve and "…" (`truncate` to 13). With the
-- whole row cut at its cap, one long name used to push every later slot - and
-- its number - off the row; this way ten presets ("10 " + 13 + " · " each)
-- stay inside `NAMES_MAX_CHARS` and five watch slots inside
-- `WATCH_NAMES_MAX_CHARS`, so every slot number is on the row.
features.SLOT_NAME_MAX_CHARS = 13

--- `pcPreset.names`: "1 게임 모드 · 2 방송 시작", or "없음" when the PC has no
--- preset. A service older than v1.2.0 gets "PC 앱 v1.2.0 필요" - the row is
--- there on every v2 screen and has to say why it is empty. Never "": an empty
--- state row reads "-" (platform notes "상세 화면(detailView) 위젯"). Each
--- name is cut to `SLOT_NAME_MAX_CHARS`.
function features.preset_names(status, lang)
  if features.parse(status) == false then
    return features.note_text(lang, "needs_service")
  end
  local parts = {}
  for _, preset in ipairs(features.presets_of(status)) do
    local name = preset.name ~= "" and preset.name or i18n.t(lang, "preset_unnamed")
    parts[#parts + 1] = tostring(preset.slot) .. " "
      .. features.truncate(name, features.SLOT_NAME_MAX_CHARS)
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
--- them - it is the list's resting value, and device/rows.lua owns it like
--- `lastAction` (rows.ensure_preset).
function features.preset_events(status, lang)
  local events = {}
  ev(events, caps.PRESET, "names", features.preset_names(status, lang))
  ev(events, caps.PRESET, "supportedSlots", features.supported_slots(status))
  return events
end

--------------------------------------------------------------------------------
-- #123: the watch card
--------------------------------------------------------------------------------

-- The opt-in watch list (media-notify.md §11) as the service reports it since
-- contract v2: `activity = { enabled, apps = [ { slot, id, label, running } ],
-- top }`, by slot (1-5, 1 = highest priority), filled slots only. The PC shows
-- it on a card of its own, the `apps` component's `pcWatchList`: a summary row, a
-- names row ("1 Steam · 3 OBS") and one state per slot, `slotOne`..`slotFive`
-- (`running` / `stopped` / `empty`), which is what a routine reads as
-- "감시 1".."감시 5". A slot number is a stable thing to name in a routine;
-- an app name is not something a capability presentation can list
-- (platform notes "자식 장치 대신 슬롯").

-- The component and its capability.
features.WATCH_COMPONENT = "apps"
features.CAP_WATCH = caps.WATCH

-- The service watches at most this many processes, one per slot.
features.WATCH_SLOTS = 5

-- `pcWatchList.summary` is defined with `maxLength: 60`, `names` with 120.
features.WATCH_SUMMARY_MAX_CHARS = 60
features.WATCH_NAMES_MAX_CHARS = 120

-- The app label inside `summary`: 13 characters at most, a longer one keeps
-- twelve and "…" (`truncate`). The
-- summary is the first cell of the card's preview, a third of the width in
-- large type (platform notes "화면 배치"); "Claude 외 1" has to fit one line.
features.WATCH_SUMMARY_LABEL_MAX_CHARS = 13

-- The service keeps labels to 30 characters; the driver cuts anything longer
-- the same way rather than trusting it.
features.APP_LABEL_MAX_CHARS = 30

-- The `slotN` enum. A routine condition offers `running` and `stopped` only;
-- `empty` is the value of a slot nothing is assigned to (never "", which
-- reads "-").
features.WATCH_RUNNING = "running"
features.WATCH_STOPPED = "stopped"
features.WATCH_EMPTY = "empty"
local WATCH_VALUES = {
  [features.WATCH_RUNNING] = true, [features.WATCH_STOPPED] = true, [features.WATCH_EMPTY] = true,
}

-- What `apps_mode` answers.
features.APPS_OLD = "old"
features.APPS_OFF = "off"
features.APPS_ON = "on"

local function clean(text)
  if type(text) ~= "string" then
    return ""
  end
  return (text:gsub("%c", " "):gsub("^%s+", ""):gsub("%s+$", ""))
end

--- The attribute of slot `n`: "slotOne".."slotFive" (attribute names take
--- letters only, platform notes "capability id와 네임스페이스").
local SLOT_ATTRS = { "slotOne", "slotTwo", "slotThree", "slotFour", "slotFive" }
function features.slot_attr(n)
  return SLOT_ATTRS[n]
end

--- `value` when it is a `slotN` enum value, else nil.
function features.watch_value(value)
  if WATCH_VALUES[value] then
    return value
  end
  return nil
end

--- What a status says about the watch list.
---
---   "old"  a service older than v1.2.0 (no `features` at all): the summary
---          says "PC 앱 v1.2.0 필요" (RECOMMENDED_SERVICE_VERSION); the
---          names row says "없음"
---   "off"  the opt-in is off, the service does not list the feature, or the
---          block is not one this driver can read (a Dev build of the
---          kind-based #114 block): the summary says "감지 꺼짐", the
---          names row "꺼짐"
---   "on"   the list is on and `activity.apps` is its contents, possibly empty
---
-- Only "on" moves a slot. "old" and "off" leave every slot on its last value:
-- a made-up "stopped" would fire every "꺼지면" routine, and a paused feature
-- is not a stopped app.
function features.apps_mode(status)
  local set = features.parse(status)
  if set == false then
    return features.APPS_OLD
  end
  local block = (status or {}).activity
  if type(block) ~= "table" or block.enabled ~= true or set[features.ACTIVITY] ~= true then
    return features.APPS_OFF
  end
  if type(block.apps) ~= "table" then
    return features.APPS_OFF
  end
  return features.APPS_ON
end

--- The watch list of a status, by slot: `{ slot, id, label, running }`.
--
-- `id` is the process name, lowercased here too. An entry without an id, a
-- repeated id, a slot outside 1-5 or one taken by an earlier entry is left
-- out. An entry with no `slot` at all (a Dev service from before contract v2)
-- takes the lowest free slot in list order - the rule the service itself uses
-- for an old config. A label that is missing or blank falls back to the id.
-- Empty for anything but an "on" list.
function features.apps_of(status)
  local out, seen, taken, unslotted = {}, {}, {}, {}
  if features.apps_mode(status) ~= features.APPS_ON then
    return out
  end
  for _, app in ipairs(status.activity.apps) do
    local id = type(app) == "table" and clean(app.id):lower() or ""
    if id ~= "" and not seen[id] then
      seen[id] = true
      local label = clean(app.label)
      if label == "" then
        label = id
      end
      local entry = {
        id = id,
        label = features.truncate(label, features.APP_LABEL_MAX_CHARS),
        running = app.running == true,
      }
      local slot = tonumber(app.slot)
      if app.slot == nil then
        unslotted[#unslotted + 1] = entry
      elseif slot and slot == math.floor(slot) and slot >= 1 and slot <= features.WATCH_SLOTS
          and not taken[slot] then
        entry.slot = math.floor(slot)
        taken[entry.slot] = true
        out[#out + 1] = entry
      end
    end
  end
  for _, entry in ipairs(unslotted) do
    for slot = 1, features.WATCH_SLOTS do
      if not taken[slot] then
        entry.slot = slot
        taken[slot] = true
        out[#out + 1] = entry
        break
      end
    end
  end
  table.sort(out, function(a, b) return a.slot < b.slot end)
  return out
end

--- The running app that leads the summary, and how many others run.
--
-- `top` is the service's own answer (the lowest-slot running app); it is
-- taken when it names a running entry, and otherwise the lowest-slot running
-- entry is - the same rule, read off the list.
-- @param apps `features.apps_of(status)`
function features.apps_top(status, apps)
  local running = 0
  local first
  local named = clean((((status or {}).activity) or {}).top):lower()
  local top
  for _, app in ipairs(apps or {}) do
    if app.running then
      running = running + 1
      first = first or app
      if app.id == named then
        top = app
      end
    end
  end
  top = top or first
  if not top then
    return nil, 0
  end
  return top, running - 1
end

--- `pcWatchList.summary`: "Steam", "Steam 외 2" ("Steam +2"), "없음" when
--- nothing on the list runs, "감지 꺼짐" ("Detection off") when the list is
--- off - not a bare "꺼짐", which is also what a slot that stopped reads - and
--- "PC 앱 v1.2.0 필요" (RECOMMENDED_SERVICE_VERSION) for a service that has
--- no such list. Never "" (an empty state row reads "-", platform notes "상세
--- 화면(detailView) 위젯").
---
--- Short on purpose: the row is the first of the card's preview in the main
--- view, a third of the width in large type, and its label "실행 중인 앱"
--- already says running (platform notes "화면 배치"). The app label is cut to
--- `WATCH_SUMMARY_LABEL_MAX_CHARS`.
function features.apps_summary(status, lang)
  local mode = features.apps_mode(status)
  if mode == features.APPS_OLD then
    return features.note_text(lang, "needs_service")
  end
  if mode == features.APPS_OFF then
    return i18n.t(lang, "apps_detection_off")
  end
  local top, others = features.apps_top(status, features.apps_of(status))
  if not top then
    return i18n.t(lang, "apps_none")
  end
  local label = features.truncate(top.label, features.WATCH_SUMMARY_LABEL_MAX_CHARS)
  local line = label
  if others > 0 then
    line = i18n.t(lang, "apps_running_more", label, others)
  end
  return features.truncate(line, features.WATCH_SUMMARY_MAX_CHARS)
end

--- `pcWatchList.names`: the filled slots in order, "1 Steam · 3 OBS"; "없음" for
--- an empty list (and for a service too old to have one - the summary row
--- says why), "꺼짐" for a list that is off. Never "". Each label is cut to
--- `SLOT_NAME_MAX_CHARS`.
function features.watch_names(status, lang)
  local mode = features.apps_mode(status)
  if mode == features.APPS_OFF then
    return i18n.t(lang, "apps_off")
  end
  local parts = {}
  for _, app in ipairs(features.apps_of(status)) do
    parts[#parts + 1] = tostring(app.slot) .. " "
      .. features.truncate(app.label, features.SLOT_NAME_MAX_CHARS)
  end
  if #parts == 0 then
    return i18n.t(lang, "apps_none")
  end
  return features.truncate(table.concat(parts, " · "), features.WATCH_NAMES_MAX_CHARS)
end

--- The five slot values of an "on" list: `running` / `stopped`, `empty` for
--- a slot nothing is assigned to.
-- @param apps `features.apps_of(status)`
function features.watch_slots(apps)
  local out = {}
  for slot = 1, features.WATCH_SLOTS do
    out[slot] = features.WATCH_EMPTY
  end
  for _, app in ipairs(apps or {}) do
    out[app.slot] = app.running and features.WATCH_RUNNING or features.WATCH_STOPPED
  end
  return out
end

--- The list's identity for the hold below: slots, ids and labels.
function features.watch_signature(apps)
  local parts = {}
  for _, app in ipairs(apps or {}) do
    parts[#parts + 1] = tostring(app.slot) .. "=" .. app.id .. "=" .. app.label
  end
  return table.concat(parts, "\n")
end

--- The slot values a status leaves the card on, and the list's signature, given
--- what the previous status left (`previous` = the extras `remember` replaces).
--
-- "old" and "off" keep the previous values (nil when there were none: the
-- slots are then not sent at all and the hub keeps what it has).
--
-- The hold: right after the list is edited on the PC (or the feature switched
-- on, or the service started), the service lists every entry as
-- `running: false` until its first scan for that list, and says so:
-- `activity.scanned` is false until then (contract C2). While it is false no
-- slot moves from "running" to "stopped" - a fake stop would fire every
-- "꺼지면" routine; once it is true the values are taken as they are.
--
-- A service without `scanned` (before the field) gets the old guess: a status
-- whose list differs from the previous one - or that follows one that was not
-- "on" - does not move a slot from "running" to "stopped"; the next status,
-- with the list unchanged, decides. A real stop that coincides with an edit is
-- late by one status there.
function features.watch_state(status, previous)
  previous = type(previous) == "table" and previous or {}
  if features.apps_mode(status) ~= features.APPS_ON then
    return previous.watch, previous.watch_signature
  end
  local apps = features.apps_of(status)
  local values = features.watch_slots(apps)
  local signature = features.watch_signature(apps)
  local shown = previous.watch
  local scanned = status.activity.scanned
  local hold
  if scanned == false then
    hold = true
  elseif scanned == true then
    hold = false
  else
    hold = previous.apps_mode ~= features.APPS_ON or previous.watch_signature ~= signature
  end
  if hold and type(shown) == "table" then
    for slot = 1, features.WATCH_SLOTS do
      if shown[slot] == features.WATCH_RUNNING and values[slot] == features.WATCH_STOPPED then
        values[slot] = features.WATCH_RUNNING
      end
    end
  end
  return values, signature
end

--- Contract C3: the PC shuts down or restarts, so the apps it ran really
--- stopped. A copy of `watch` with every `running` slot `stopped`, and the
--- records of the slots that moved - forced, so a routine on "꺼지면" fires
--- even when a lost event left the cloud elsewhere. `watch` is not modified;
--- nil (nothing known) moves nothing.
function features.stop_watch(watch)
  if type(watch) ~= "table" then
    return watch, {}
  end
  local out, records = {}, {}
  for slot = 1, features.WATCH_SLOTS do
    out[slot] = watch[slot]
    if watch[slot] == features.WATCH_RUNNING then
      out[slot] = features.WATCH_STOPPED
      records[#records + 1] = { cap = features.CAP_WATCH, attr = features.slot_attr(slot),
        value = features.WATCH_STOPPED, component = features.WATCH_COMPONENT, force = true }
    end
  end
  return out, records
end

--- #123: the watch card's rows of a status body, on the `apps` component.
--
-- @param watch the slot values to show (`extras.watch`, from `watch_state`),
--   or nil: then an "on" status's own values, and nothing for the slots of an
--   "old" or "off" one - they keep their last values
-- @param fill true for a repaint: a slot with no value at all gets `empty`
--   (a row never sent reads "-")
function features.watch_events(status, lang, watch, fill)
  local events = {}
  ev(events, features.CAP_WATCH, "summary", features.apps_summary(status, lang), features.WATCH_COMPONENT)
  ev(events, features.CAP_WATCH, "names", features.watch_names(status, lang), features.WATCH_COMPONENT)
  local values = watch
  if values == nil and features.apps_mode(status) == features.APPS_ON then
    values = features.watch_slots(features.apps_of(status))
  end
  if values == nil and fill then
    values = {}
  end
  if values then
    for slot = 1, features.WATCH_SLOTS do
      ev(events, features.CAP_WATCH, features.slot_attr(slot),
        features.watch_value(values[slot]) or features.WATCH_EMPTY, features.WATCH_COMPONENT)
    end
  end
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
-- @param opts `lang`; #123: `watch` and `fill` (`watch_events`)
function features.apply_status(status, opts)
  opts = opts or {}
  local lang = opts.lang
  local events = {}
  -- #118: in the order of the media group on screen.
  append(events, features.track_events(status, lang))
  append(events, features.media_events(features.playback_status(status)))
  append(events, features.audio_events(status))
  append(events, features.preset_events(status, lang))
  append(events, features.watch_events(status, lang, opts.watch, opts.fill))
  append(events, features.awake_events(status))
  append(events, features.battery_events(status))
  return events
end

--- Every v1.2.0 row a device that has never been polled paints (state.initial_rows).
-- @param watch #123: slot values to keep (a repaint), else every slot `empty`
function features.initial_rows(lang, watch)
  local events = features.media_events()
  -- #113: before the first status there is no list of presets to show.
  ev(events, caps.PRESET, "names", i18n.t(lang, "presets_none"))
  ev(events, caps.PRESET, "supportedSlots", { features.PRESET_NONE })
  -- #123: nothing known yet, which reads as "nothing running" and "nothing
  -- watched"; a slot the hub still knows a value for keeps it.
  ev(events, features.CAP_WATCH, "summary", i18n.t(lang, "apps_none"), features.WATCH_COMPONENT)
  ev(events, features.CAP_WATCH, "names", i18n.t(lang, "apps_none"), features.WATCH_COMPONENT)
  for slot = 1, features.WATCH_SLOTS do
    ev(events, features.CAP_WATCH, features.slot_attr(slot),
      features.watch_value((watch or {})[slot]) or features.WATCH_EMPTY, features.WATCH_COMPONENT)
  end
  -- #115: a service that has just started has keep-awake off (it does not
  -- carry the period over a restart, §12), so "off" is the honest default.
  ev(events, features.CAP_SWITCH, "switch", "off", features.AWAKE_COMPONENT)
  return events
end

return features
