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
local display = require "display"
local i18n = require "i18n"
local poll = require "poll"
local push = require "push"
local state = require "state"
local version = require "version"
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
  if display.is_child(device) then
    -- The display child has no service of its own: it mirrors the parent.
    return
  end
  -- §6.4: one listener per driver, opened on the first device that needs it.
  push.start(driver)
  poll.start(driver, device)
  display.ensure(driver, device)
end

local function device_added(driver, device)
  log.info("added " .. device.id)
  if display.is_child(device) then
    return
  end
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
  if display.is_child(device) then
    return
  end
  poll.stop(driver, device)
  wol.cancel_wake(driver, device)
  push.stop(driver, device)
  -- §5.2: the child belongs to this PC and goes with it.
  display.delete(driver, display.child_of(driver, device))
end

local function device_info_changed(driver, device, _event, _args)
  -- Preferences are already updated on `device` here; restarting the timer
  -- picks up a new pollInterval and a poll picks up a new IP/secret/port.
  log.info("preferences changed for " .. device.id)
  if display.is_child(device) then
    return
  end
  poll.start(driver, device)
  -- createDisplayDevice may have been toggled either way.
  display.ensure(driver, device)
end

local function device_do_configure(driver, device)
  if display.is_child(device) then
    return
  end
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
--- On the display child it is `turnscreenon` on the parent instead (§5.2).
local function handle_switch_on(driver, device)
  if display.is_child(device) then
    local ok, kind = display.handle_switch(driver, device, "on")
    if not ok then
      log.warn(string.format("display on failed on %s: %s", device.id, tostring(kind)))
    end
    return
  end
  local nxt = state.transition(poll.get_state(device), "switch_on")
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  wol.wake(driver, device)
end

--- switch.off: the configured off action with the service's own grace handling.
local function handle_switch_off(driver, device)
  if display.is_child(device) then
    local ok, kind = display.handle_switch(driver, device, "off")
    if not ok then
      log.warn(string.format("display off failed on %s: %s", device.id, tostring(kind)))
    end
    return
  end
  local prefs = device.preferences or {}
  local ok, body, kind = client.command(device, prefs.offAction or "shutdown", "default", 0)
  if not ok then
    report_error(device, kind, body)
    return
  end
  poll.once(driver, device)
end

local function handle_refresh(driver, device)
  if display.is_child(device) then
    local parent = display.parent_of(driver, device)
    if parent then
      poll.once(driver, parent)
    end
    return
  end
  poll.once(driver, device)
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

--- pcSchedule.schedule(command, minutes): same endpoint, minutes > 0 (§4.3).
--- An existing schedule is replaced by the service, which is worth saying.
local function handle_schedule(driver, device, cmd)
  local args = (cmd or {}).args or {}
  local had_schedule = poll.get_state(device).schedule_active == true
  local ok, body, kind = client.command(device, args.command, "default", args.minutes or 0)
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
  capability_handlers[custom.command.ID] = { execute = handle_execute }
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
