-- SmartThings Edge driver for PC Control.
--
-- Entry point only: the handlers in handlers/ are registered here. The logic
-- lives in poll/push/wol/discovery, device/ and model/, so it can be
-- tested without a hub (design doc §2).

local Driver = require "st.driver"
local capabilities = require "st.capabilities"
local log = require "log"

local awake = require "handlers.awake"
local caps = require "caps"
local discovery = require "discovery"
local lifecycle = require "handlers.lifecycle"
local media = require "handlers.media"
local power = require "handlers.power"
local preset = require "handlers.preset"
local schedule = require "handlers.schedule"
local toast = require "handlers.toast"
local version = require "driver_version"

-- Custom capabilities only resolve once the account owner has created them and
-- `caps.NAMESPACE` holds the real namespace. Missing ones are logged and their
-- handlers left unregistered; switch and refresh work either way.
local custom, missing = caps.load(capabilities)
if #missing > 0 then
  log.warn("custom capabilities not available yet: " .. table.concat(missing, ", "))
end

-- `switch` is on two components: `main` is the PC's power, `awake` keep-awake.
local function switch_on(driver, device, cmd)
  if awake.is_awake(cmd) then
    return awake.on(driver, device)
  end
  return power.switch_on(driver, device)
end

local function switch_off(driver, device, cmd)
  if awake.is_awake(cmd) then
    return awake.off(driver, device)
  end
  return power.switch_off(driver, device)
end

local capability_handlers = {
  [capabilities.switch.ID] = {
    [capabilities.switch.commands.on.NAME] = switch_on,
    [capabilities.switch.commands.off.NAME] = switch_off,
  },
  [capabilities.refresh.ID] = {
    [capabilities.refresh.commands.refresh.NAME] = power.refresh,
  },
  [capabilities.audioVolume.ID] = {
    [capabilities.audioVolume.commands.setVolume.NAME] = media.set_volume,
    [capabilities.audioVolume.commands.volumeUp.NAME] = media.volume_up,
    [capabilities.audioVolume.commands.volumeDown.NAME] = media.volume_down,
  },
  [capabilities.audioMute.ID] = {
    [capabilities.audioMute.commands.mute.NAME] = media.mute,
    [capabilities.audioMute.commands.unmute.NAME] = media.unmute,
    [capabilities.audioMute.commands.setMute.NAME] = media.set_mute,
  },
  [capabilities.mediaPlayback.ID] = {
    [capabilities.mediaPlayback.commands.play.NAME] = media.play,
    [capabilities.mediaPlayback.commands.pause.NAME] = media.pause,
    [capabilities.mediaPlayback.commands.stop.NAME] = media.stop,
    [capabilities.mediaPlayback.commands.setPlaybackStatus.NAME] = media.set_playback_status,
  },
  [capabilities.mediaTrackControl.ID] = {
    [capabilities.mediaTrackControl.commands.nextTrack.NAME] = media.next_track,
    [capabilities.mediaTrackControl.commands.previousTrack.NAME] = media.previous_track,
  },
}

-- Command names are literals: what `capabilities/*.json` declare. The
-- generated capability object only carries them once the capability exists.
if custom.command then
  local handlers = { execute = power.execute }
  for name, service_command in pairs(power.BUTTONS) do
    handlers[name] = power.button(service_command)
  end
  capability_handlers[custom.command.ID] = handlers
end
if custom.preset then
  capability_handlers[custom.preset.ID] = { run = preset.run }
end
if custom.toast then
  capability_handlers[custom.toast.ID] = { send = toast.send }
end
if custom.schedule then
  capability_handlers[custom.schedule.ID] = {
    setPlanCommand = schedule.set_plan_command,
    cancel = schedule.cancel,
    schedule = schedule.schedule,
  }
end

local pc_driver = Driver("smartthings-pc-control", {
  discovery = discovery.handle,
  driver_lifecycle = lifecycle.driver,
  lifecycle_handlers = {
    init = lifecycle.init,
    added = lifecycle.added,
    removed = lifecycle.removed,
    infoChanged = lifecycle.info_changed,
    doConfigure = lifecycle.do_configure,
  },
  capability_handlers = capability_handlers,
})

log.info("starting smartthings-pc-control driver " .. version)
pc_driver:run()

-- The hub ignores what this chunk returns; tests call the handlers on it.
return pc_driver
