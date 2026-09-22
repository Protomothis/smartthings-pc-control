-- SmartThings Edge driver for PC Control.
--
-- Entry point only: lifecycle and capability handlers wire the modules
-- together, all logic lives in client/state/poll/wol/discovery so it can be
-- unit-tested without a hub (design doc §3).

local Driver = require "st.driver"
local capabilities = require "st.capabilities"
local log = require "log"

local caps = require "caps"
local client = require "client"
local discovery = require "discovery"
local i18n = require "i18n"
local poll = require "poll"
local profiles = require "profiles"
local push = require "push"
local state = require "state"
local version = require "driver_version"
local wol = require "wol"

-- Custom capabilities only resolve once the account owner has created them and
-- `caps.NAMESPACE` holds the real namespace (#72). Missing ones are logged and
-- their handlers are left unregistered; switch/refresh keep working either way.
local custom, missing = caps.load(capabilities)
if #missing > 0 then
  log.warn("custom capabilities not available yet: " .. table.concat(missing, ", "))
end

--------------------------------------------------------------------------------
-- lifecycle
--------------------------------------------------------------------------------

local function device_init(driver, device)
  log.info(string.format("init %s (driver %s)", device.id, version))
  -- #81: the display child is gone. A hub that ran an older driver still has
  -- the children it created, and their profiles are no longer in the package,
  -- so they are deleted here — once per device per driver run.
  if profiles.remove_legacy_child(driver, device) then
    return
  end
  -- §14.3: a device keeps the screen definition it was created with, so a
  -- device left on an older profile is moved to the current one, once.
  profiles.ensure(device)
  -- §6.4: one listener per driver, opened on the first device that needs it.
  push.start(driver)
  poll.start(driver, device)
end

local function device_added(driver, device)
  log.info("added " .. device.id)
  -- A device that is being added was created by this driver run, so it is on
  -- the current profile: record the name now (§14.3, the hub does not always
  -- expose it) and let `ensure` confirm there is nothing to migrate.
  profiles.remember(device)
  profiles.ensure(device)
  -- §13.1: a device SSDP just created arrives with the address it was found at.
  discovery.adopt(device)
  -- Paint the tiles immediately; the first poll fills in the real values.
  local initial = state.new()
  poll.set_state(device, initial)
  poll.emit_power(device, initial)
  if not client.device_base_url(device) then
    poll.emit_connection(device, "unreachable", i18n.t(poll.lang(device), "no_ip"))
  end
end

local function device_removed(driver, device)
  log.info("removed " .. device.id)
  poll.stop(driver, device)
  wol.cancel_wake(driver, device)
  push.stop(driver, device)
end

local function device_info_changed(driver, device, _event, _args)
  -- Preferences are already updated on `device` here; restarting the timer
  -- picks up a new pollInterval and a poll picks up a new IP/secret/port.
  log.info("preferences changed for " .. device.id)
  poll.start(driver, device)
end

local function device_do_configure(driver, device)
  poll.start(driver, device)
end

--------------------------------------------------------------------------------
-- capability handlers
--------------------------------------------------------------------------------

-- Report a failed command through pcStatus instead of failing silently (§1.3).
-- A rate-limited request (§8) says nothing about the connection, so it is
-- logged and the tiles keep what the last poll put there.
local function report_error(device, kind, body)
  local connection = poll.connection_for(kind)
  if not connection then
    log.warn(string.format("command refused (%s) on %s", tostring(kind), device.id))
    return
  end
  local lang = poll.lang(device)
  poll.emit_connection(device, connection, poll.message_for(kind, body, lang))
end

--- switch.on: WoL sequence, device goes to `waking` (§6.2/§6.3).
local function handle_switch_on(driver, device)
  local nxt = state.transition(poll.get_state(device), "switch_on")
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  wol.wake(driver, device)
end

--- switch.off: the configured off action with the service's own grace handling.
local function handle_switch_off(driver, device)
  local prefs = device.preferences or {}
  local ok, body, kind = client.command(device, prefs.offAction or "shutdown", "default", 0)
  if not ok then
    report_error(device, kind, body)
    return
  end
  poll.once(driver, device)
end

local function handle_refresh(driver, device)
  poll.once(driver, device)
end

--- The remote-control buttons of the detail view (#78): one no-argument
--- capability command per row, mapped to the service command name of §4.3.
--- `wake` is not a service command at all — it is the WoL sequence, the same
--- thing `switch on` does.
local BUTTONS = {
  wake = "wake",
  suspend = "suspend",
  hibernate = "hibernate",
  restart = "restart",
  shutdown = "shutdown",
  lock = "lock",
  screenOff = "turnscreenoff",
  screenOn = "turnscreenon",
}

--- §4.3 `mode` for a button press, from the `buttonMode` preference (§5.4).
--- `default` follows whatever grace period the PC is configured with (so the
--- toast is still cancellable); `immediate` skips it.
local function button_mode(device)
  local mode = (device.preferences or {}).buttonMode
  if mode == "immediate" then
    return "immediate"
  end
  return "default"
end

--- Build the handler for one remote-control button.
local function button_handler(service_command)
  return function(driver, device)
    if service_command == "wake" then
      return handle_switch_on(driver, device)
    end
    local ok, body, kind = client.command(device, service_command, button_mode(device), 0)
    if not ok then
      report_error(device, kind, body)
      return
    end
    poll.once(driver, device)
  end
end

--- pcCommand.execute(command, mode, minutes) — capabilities/pcCommand.json.
--- `minutes > 0` turns the same endpoint into a schedule (§4.3).
local function handle_execute(driver, device, cmd)
  local args = (cmd or {}).args or {}
  local ok, body, kind = client.command(device, args.command, args.mode or "default", args.minutes or 0)
  if not ok then
    report_error(device, kind, body)
    return
  end
  -- The service acted; poll straight away so the tiles show the result instead
  -- of the state from up to `pollInterval` ago.
  poll.once(driver, device)
end

-- Commands pcSchedule.schedule may carry; anything else (or nothing, when the
-- detail-view list only sends minutes) falls back to the switch-off action
-- when that is schedulable, else shutdown.
local SCHEDULABLE = { shutdown = true, restart = true, suspend = true, hibernate = true }

local function schedule_command(device, requested)
  if requested and SCHEDULABLE[requested] then
    return requested
  end
  local off = (device.preferences or {}).offAction
  if off and SCHEDULABLE[off] then
    return off
  end
  return "shutdown"
end

--- pcSchedule.schedule(minutes, command?): same endpoint, minutes > 0 (§4.3).
--- `command` is optional (SmartThings list presentations send one argument);
--- see schedule_command for the fallback. An existing schedule is replaced by
--- the service, which is worth saying.
local function handle_schedule(driver, device, cmd)
  local args = (cmd or {}).args or {}
  local had_schedule = poll.get_state(device).schedule_active == true
  local ok, body, kind = client.command(device, schedule_command(device, args.command), "default", args.minutes or 0)
  if not ok then
    report_error(device, kind, body)
    return
  end
  poll.once(driver, device, {
    note = had_schedule and i18n.t(poll.lang(device), "schedule_replaced") or nil,
  })
end

--- pcSchedule.cancel(): DELETE /st/v1/schedule (§4.4). The service answers
--- `{"cancelled": false}` when there was nothing to cancel.
local function handle_cancel(driver, device)
  local ok, body, kind = client.cancel(device)
  if not ok then
    report_error(device, kind, body)
    return
  end
  local cancelled = (body or {}).cancelled == true
  -- §6.2: cancelling the grace period brings the switch back on.
  local nxt = state.transition(poll.get_state(device), "schedule_cancelled")
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  poll.once(driver, device, {
    note = i18n.t(poll.lang(device), cancelled and "schedule_cancelled" or "schedule_none"),
  })
end

local capability_handlers = {
  [capabilities.switch.ID] = {
    [capabilities.switch.commands.on.NAME] = handle_switch_on,
    [capabilities.switch.commands.off.NAME] = handle_switch_off,
  },
  [capabilities.refresh.ID] = {
    [capabilities.refresh.commands.refresh.NAME] = handle_refresh,
  },
}

-- Command names are literals: they are what `capabilities/pcCommand.json` and
-- `capabilities/pcSchedule.json` declare, and the generated capability object
-- only carries them once the account owner has created the capabilities.
if custom.command then
  local handlers = { execute = handle_execute }
  for name, service_command in pairs(BUTTONS) do
    handlers[name] = button_handler(service_command)
  end
  capability_handlers[custom.command.ID] = handlers
end
if custom.schedule then
  capability_handlers[custom.schedule.ID] = {
    cancel = handle_cancel,
    schedule = handle_schedule,
  }
end

--------------------------------------------------------------------------------

local pc_driver = Driver("smartthings-pc-control", {
  discovery = discovery.handle,
  lifecycle_handlers = {
    init = device_init,
    added = device_added,
    removed = device_removed,
    infoChanged = device_info_changed,
    doConfigure = device_do_configure,
  },
  capability_handlers = capability_handlers,
})

log.info("starting smartthings-pc-control driver " .. version)
pc_driver:run()

-- The hub ignores what this chunk returns; the driver object is returned so
-- tests can call the lifecycle handlers exactly as the hub would.
return pc_driver
