-- The standard audio and media capabilities: `audioVolume`, `audioMute`,
-- `mediaPlayback`, `mediaTrackControl` (media-notify.md §3, §5). Their ids and
-- command names are the platform's, so they resolve on every hub.

local common = require "handlers.common"
local emit = require "device.emit"
local features = require "features"
local fields = require "device.fields"

local media = {}

-- The rows a successful command is answered on: forced, so the spinner ends
-- even when the value did not change (volume already at 100).
local AUDIO_ROWS = {
  [features.CAP_VOLUME .. ".volume"] = true,
  [features.CAP_MUTE .. ".mute"] = true,
}
local MEDIA_ROWS = {
  [features.CAP_PLAYBACK .. ".supportedPlaybackCommands"] = true,
  [features.CAP_PLAYBACK .. ".playbackStatus"] = true,
  [features.CAP_TRACK .. ".supportedTrackControlCommands"] = true,
}

-- What a refused command is answered with: the value the row already had.
local function answer_volume(device)
  local audio = (fields.extras(device) or {}).audio or {}
  if audio.volume ~= nil then
    emit.rows(device, { { cap = features.CAP_VOLUME, attr = "volume", value = audio.volume } },
      { reason = "answer" })
  end
end

local function answer_mute(device)
  local audio = (fields.extras(device) or {}).audio or {}
  if audio.muted ~= nil then
    emit.rows(device, { { cap = features.CAP_MUTE, attr = "mute",
      value = audio.muted and "muted" or "unmuted" } }, { reason = "answer" })
  end
end

-- What the last status said is playing, and the constant attributes.
local function answer_media(device)
  emit.rows(device, features.media_events((fields.extras(device) or {}).playback),
    { reason = "answer" })
end

local function audio_command(service_command, answer)
  return function(driver, device)
    return common.run_feature(driver, device, service_command, nil, answer, AUDIO_ROWS)
  end
end

local function media_command(service_command)
  return function(driver, device)
    return common.run_feature(driver, device, service_command, nil, answer_media, MEDIA_ROWS)
  end
end

--- audioVolume.setVolume(volume): the slider, 0-100, rounded.
function media.set_volume(driver, device, cmd)
  local volume = features.volume_of({ volume = ((cmd or {}).args or {}).volume })
  if volume == nil then
    return answer_volume(device)
  end
  return common.run_feature(driver, device, "volume", volume, answer_volume, AUDIO_ROWS)
end

media.volume_up = audio_command("volumeup", answer_volume)
media.volume_down = audio_command("volumedown", answer_volume)
media.mute = audio_command("mute", answer_mute)
media.unmute = audio_command("unmute", answer_mute)

--- audioMute.setMute(state): the routine action's "muted"/"unmuted".
function media.set_mute(driver, device, cmd)
  local wanted = ((cmd or {}).args or {}).state
  local service_command = wanted == "muted" and "mute" or "unmute"
  return common.run_feature(driver, device, service_command, nil, answer_mute, AUDIO_ROWS)
end

media.play = media_command("play")
media.pause = media_command("pause")
media.stop = media_command("stop")
media.next_track = media_command("next")
media.previous_track = media_command("prev")

-- mediaPlayback.setPlaybackStatus(status) -> the media key that asks for it.
local PLAYBACK_FOR_STATUS = { playing = "play", paused = "pause", stopped = "stop" }

function media.set_playback_status(driver, device, cmd)
  local wanted = ((cmd or {}).args or {}).status
  local service_command = PLAYBACK_FOR_STATUS[tostring(wanted or "")]
  if not service_command then
    return answer_media(device)
  end
  return common.run_feature(driver, device, service_command, nil, answer_media, MEDIA_ROWS)
end

return media
