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

local features = {}

-- The names `status.features` carries (media-notify.md §3). An older service
-- sends no `features` key at all, which is how the driver tells "too old" from
-- "this PC does not offer it".
features.AUDIO = "audio"
features.MEDIA = "media"

-- Standard capability ids.
features.CAP_VOLUME = "audioVolume"
features.CAP_MUTE = "audioMute"
features.CAP_PLAYBACK = "mediaPlayback"
features.CAP_TRACK = "mediaTrackControl"

-- What the media rows offer. Constant: the service sends the key whatever is
-- playing, and it has no way to know what is.
features.PLAYBACK_COMMANDS = { "play", "pause", "stop" }
features.TRACK_COMMANDS = { "nextTrack", "previousTrack" }

-- #107, 실측 대기: `mediaPlayback.playbackStatus` is NOT reported. The service
-- sends media keys and cannot see what they did, so any value would be made
-- up (media-notify.md §5). Whether the app then draws the media row as "-", or
-- spins after play/pause waiting for a status that never comes, is measured on
-- the Dev channel. If it does, set this to "stopped": the driver then emits it
-- as the row's resting value (initial rows and as the answer to every media
-- command), the way `lastAction` rests on `none`.
features.PLAYBACK_RESTING = nil

-- service command -> the feature it needs (media-notify.md §3).
features.COMMAND_FEATURE = {
  volume = features.AUDIO, volumeup = features.AUDIO, volumedown = features.AUDIO,
  mute = features.AUDIO, unmute = features.AUDIO,
  play = features.MEDIA, pause = features.MEDIA, playpause = features.MEDIA,
  stop = features.MEDIA, next = features.MEDIA, prev = features.MEDIA,
}

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
  device_state.extras = {
    features = features.parse(status),
    audio = {
      available = audio.available,
      volume = features.volume_of(audio),
      muted = muted,
    },
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

--- #107: the media rows' constant attributes. Emitted with every status (the
--- hub drops an unchanged value) and in the initial rows, so a device that was
--- just migrated has its buttons before the first poll.
function features.media_events()
  local events = {}
  ev(events, features.CAP_PLAYBACK, "supportedPlaybackCommands", copy_list(features.PLAYBACK_COMMANDS))
  ev(events, features.CAP_TRACK, "supportedTrackControlCommands", copy_list(features.TRACK_COMMANDS))
  if features.PLAYBACK_RESTING then
    ev(events, features.CAP_PLAYBACK, "playbackStatus", features.PLAYBACK_RESTING)
  end
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
  local _ = opts
  local events = {}
  append(events, features.audio_events(status))
  append(events, features.media_events())
  return events
end

--- Every v1.2.0 row a device that has never been polled paints (state.initial_rows).
function features.initial_rows(lang)
  local _ = lang
  return features.media_events()
end

return features
