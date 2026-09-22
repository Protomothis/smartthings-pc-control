-- SmartThings Edge driver for PC Control.
--
-- Entry point only: lifecycle and capability handlers wire the modules
-- together, all logic lives in client/state/poll/wol/discovery so it can be
-- unit-tested without a hub (design doc §2).

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
  -- platform notes "프로필과 화면 생성": a device keeps the screen definition it was created with, so a
  -- device left on an older profile is moved to the current one, once.
  profiles.ensure(device)
  -- #85: a migration onto the new capability ids leaves every attribute of
  -- pcExec and pcDelay unset, which reads as "-" and keeps the app saying
  -- the device has not reported all of its state. Paint them once.
  if poll.ensure_rows(device) then
    -- First run on this generation of rows: forced rows + forced poll, now
    -- and again shortly, so attributes that never change (updateAvailable)
    -- and rows the cloud dropped while applying the profile are filled in.
    poll.repaint_soon(driver, device)
  end
  -- §6.3: one listener per driver, opened on the first device that needs it.
  push.start(driver)
  poll.start(driver, device)
end

local function device_added(driver, device)
  log.info("added " .. device.id)
  -- A device that is being added was created by this driver run, so it is on
  -- the current profile: record the name now (platform notes "프로필과 화면 생성", the hub does not always
  -- expose it) and let `ensure` confirm there is nothing to migrate.
  profiles.remember(device)
  profiles.ensure(device)
  -- §6.5: a device SSDP just created arrives with the address it was found at.
  discovery.adopt(device)
  -- Paint the tiles immediately; the first poll fills in the real values.
  local initial = state.new()
  poll.set_state(device, initial)
  poll.emit_power(device, initial)
  -- #82: the command list shows `lastAction`, and an attribute that was never
  -- emitted reads as "-" on the phone. #84: the row rests on `none` for good.
  -- #85: and the same goes for every other pcExec / pcDelay attribute,
  -- including the "command to schedule" row, whose default is the `offAction`
  -- preference.
  poll.ensure_rows(device)
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
  -- infoChanged also fires when a profile migration has landed: the cloud's
  -- record of the new profile is empty until every row is sent again.
  poll.start(driver, device)
  poll.repaint_soon(driver, device)
end

local function device_do_configure(driver, device)
  poll.start(driver, device)
end

--------------------------------------------------------------------------------
-- capability handlers
--------------------------------------------------------------------------------

-- Report a failed command through pcInfo instead of failing silently (§1).
-- A rate-limited request (§3.1) says nothing about the connection, so it is
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

--- switch.on: WoL sequence, device goes to `waking` (§6.2/§6.4).
local function handle_switch_on(driver, device)
  local nxt = state.transition(poll.get_state(device), "switch_on")
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  wol.wake(driver, device)
end

--- switch.off: the configured off action with the service's own grace handling.
local function handle_switch_off(driver, device)
  local prefs = device.preferences or {}
  local command = prefs.offAction or "shutdown"
  local ok, body, kind = client.command(device, command, "default", 0)
  if not ok then
    report_error(device, kind, body)
    return
  end
  poll.once(driver, device)
end

local function handle_refresh(driver, device)
  poll.once(driver, device)
end

--- The no-argument commands of §4, mapped to the service command name of
--- §3.3. `wake` is not a service command at all — it is the WoL sequence, the
--- same thing `switch on` does.
---
--- #82 took their `pushButton` rows off the detail view (a button has no value,
--- so the phone drew "-" next to each of the eight); the screen sends
--- `execute(command)` from one list instead. The commands stay in the
--- definition and keep their handlers: a device created on an older profile
--- still shows the buttons, and they are the shape a hub-local automation or a
--- scene can call.
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

--- §3.3 `mode` for a command sent without one, from the `buttonMode`
--- preference (§7). `default` follows whatever grace period the PC is
--- configured with (so the toast is still cancellable); `immediate` skips it.
--- The detail-view list sends `command` only, so it lands here too (#82).
local function button_mode(device)
  local mode = (device.preferences or {}).buttonMode
  if mode == "immediate" then
    return "immediate"
  end
  return "default"
end

--- Run one service command and report what happened (#82, #84).
--
-- The one path every command row takes: send it, then poll straight away so
-- the tiles show the result instead of the state from up to `pollInterval`
-- ago. What ran appears on the `lastCommand` row, which the poll fills in from
-- the service's own `last_command`; `lastAction` stays on `none` (#84).
-- `wake` never reaches the service — it is the WoL sequence.
--
-- #84: `none` is a command in the enum and does nothing on purpose. Closing
-- the detail view's list without picking anything sends the row's current
-- value (platform notes "상세 화면(detailView) 위젯"), and the row rests on `none`, so this is the path a dismissed
-- picker takes: refresh the tiles and leave the PC alone.
--
-- #86: whatever happens next, the command row is answered first with a forced
-- re-emit of its resting value. The row never changes value (it rests on
-- `none`), so without that the app keeps a spinner up until it fails with an
-- error - measured on the phone 2026-09-22 (platform notes "상세 화면(detailView) 위젯").
local function run_command(driver, device, service_command, mode, minutes)
  poll.answer_action(device)
  if service_command == nil or service_command == "" or service_command == state.ACTION_NONE then
    return poll.once(driver, device)
  end
  if service_command == "wake" then
    return handle_switch_on(driver, device)
  end
  minutes = math.floor(tonumber(minutes) or 0)
  local ok, body, kind = client.command(device, service_command, mode, minutes)
  if not ok then
    report_error(device, kind, body)
    return
  end
  poll.once(driver, device)
end

--- Build the handler for one no-argument command.
local function button_handler(service_command)
  return function(driver, device)
    return run_command(driver, device, service_command, button_mode(device), 0)
  end
end

--- pcExec.execute(command, mode, minutes) — capabilities/pcExec.json.
--- `minutes > 0` turns the same endpoint into a schedule (§3.3).
--
--- The detail-view list (#82) sends `command` alone: the mode then follows the
--- `buttonMode` preference, exactly as the buttons it replaced did, and the
--- delay is 0. An automation fills all three in.
local function handle_execute(driver, device, cmd)
  local args = (cmd or {}).args or {}
  return run_command(driver, device, args.command,
    args.mode or button_mode(device), args.minutes or 0)
end

--- pcDelay.setPlanCommand(command): what a schedule without a command of
--- its own runs (#84; moved onto the schedule capability in #85).
--
-- The detail view's schedule list can only pick the minutes (one argument per
-- list, platform notes "상세 화면(detailView) 위젯"), so the command is picked on its own row and kept in a device
-- field. Every value it can hold is a valid argument, so a dismissed picker
-- simply re-sends the current one.
--
-- #85: the row sits in the schedule card, because the app groups detail rows by
-- the capability that owns them and the user read the schedule card as
-- "minutes only" while this row lived with the PC commands.
--
-- #86: emitted with `state_change = true`. Re-picking the value the row already
-- shows (restart -> restart) changes nothing, and the app then spins until it
-- gives up with an error (platform notes "상세 화면(detailView) 위젯").
local function handle_set_plan_command(_driver, device, cmd)
  local args = (cmd or {}).args or {}
  poll.emit_plan_command(device, args.command, true)
end

--- The command `pcDelay.schedule` runs when it carries none of its own:
--- the automation's argument first, then the `planCommand` the user picked,
--- then the `offAction` preference, else shutdown (§3.3).
local function schedule_command(device, requested)
  return state.plan_command_for(requested, poll.plan_command(device))
end

--- pcDelay.cancel(): DELETE /st/v1/schedule (§3.4). The service answers
--- `{"cancelled": false}` when there was nothing to cancel.
local handle_cancel

--- pcDelay.schedule(minutes, command?): same endpoint, minutes > 0 (§3.3).
--- `command` is optional (SmartThings list presentations send one argument);
--- see schedule_command for the fallback. An existing schedule is replaced by
--- the service, which is worth saying.
---
--- #82: the detail view is one list with the presets and a `Cancel` entry that
--- sends `minutes = 0`, because a `pushButton` row has no value and drew "-".
--- #85: and the definition now says `minimum: 0`, because the cloud validates
--- a command's arguments against the definition before the hub ever sees them -
--- with `minimum: 1` the Cancel entry only ever produced a "system error"
--- popup (platform notes "상세 화면(detailView) 위젯"). Zero minutes takes the `cancel()` path, which is still in the
--- definition and still handled for devices on an older profile. The list sends
--- the key as a string on some firmwares, hence the `tonumber`.
---
--- #88: `minutes` starts at -1, which does nothing at all. Closing the list
--- without picking anything sends the row's current value (platform notes "상세 화면(detailView) 위젯"), and the
--- row rests on `minutesPick` = "-1", so that is the path a dismissed picker
--- takes: refresh the tiles and leave the schedule alone. Before it, the row
--- rested on `status` and the phone sent `schedule(minutes: "idle")`, which the
--- cloud rejected with a network-error popup. Whatever the argument turns out
--- to be, the row is answered first with a forced re-emit of "-1" - it never
--- changes value, so an unforced event is dropped and the app spins (#86).
local function handle_schedule(driver, device, cmd)
  local args = (cmd or {}).args or {}
  poll.answer_minutes_pick(device)
  -- A `schedule` with no minutes at all is the same "nothing was picked" case.
  local minutes = math.floor(tonumber(args.minutes) or state.MINUTES_NONE)
  if minutes < 0 then
    return poll.once(driver, device)
  end
  if minutes == 0 then
    return handle_cancel(driver, device)
  end
  local had_schedule = poll.get_state(device).schedule_active == true
  local ok, body, kind = client.command(device, schedule_command(device, args.command), "default", minutes)
  if not ok then
    report_error(device, kind, body)
    return
  end
  poll.once(driver, device, {
    note = had_schedule and i18n.t(poll.lang(device), "schedule_replaced") or nil,
    -- #86: the rows the schedule list is bound to answer this command.
    force = poll.SCHEDULE_ROWS,
  })
end

function handle_cancel(driver, device)
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
    -- #86: cancelling with nothing scheduled leaves every schedule row exactly
    -- as it was, which is precisely when the app's spinner used to end in an
    -- error. Forced, the rows go out anyway and the spinner finishes (platform notes "상세 화면(detailView) 위젯").
    force = poll.SCHEDULE_ROWS,
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

-- Command names are literals: they are what `capabilities/pcExec.json` and
-- `capabilities/pcDelay.json` declare, and the generated capability object
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
    -- #85: `setPlanCommand` moved here with the row it drives.
    setPlanCommand = handle_set_plan_command,
    cancel = handle_cancel,
    schedule = handle_schedule,
  }
end

--------------------------------------------------------------------------------

--- Driver shutdown (hub restart, driver update): drop every device's push
--- subscription and close the listener so the service does not keep posting
--- to a port nobody listens on until the TTL runs out.
local function driver_lifecycle(driver, event)
  if event ~= "shutdown" then
    return
  end
  local ok, devices = pcall(function() return driver:get_devices() end)
  for _, device in ipairs(ok and devices or {}) do
    pcall(function() push.stop(driver, device) end)
  end
  pcall(function() push.shutdown(driver) end)
  log.info("driver shutting down: push subscriptions released")
end

local pc_driver = Driver("smartthings-pc-control", {
  discovery = discovery.handle,
  driver_lifecycle = driver_lifecycle,
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
