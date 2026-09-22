-- The capability handlers of src/init.lua (#82).
--
-- init.lua is the one module the other suites do not touch: it is the driver
-- entry point, and `Driver(...)` plus `run()` come from the st.driver mock, so
-- loading it here is exactly what the hub does. The handlers it registers are
-- then called the way the hub calls them - `handler(driver, device, command)` -
-- with the HTTP layer swapped for a recorder, because what is interesting is
-- which service command went out and what the tiles were told afterwards.

local h = require "helpers"
local caps = require "caps"
local client = require "client"
local poll = require "poll"
local state = require "state"
local wol = require "wol"

-- Loading the module registers the handlers and "runs" the driver.
local driver = require "init"

local T = {}

local function handlers_for(id)
  local handlers = (driver.capability_handlers or {})[id]
  if not handlers then
    error("no capability handlers registered for " .. tostring(id), 0)
  end
  return handlers
end

local function device_with(preferences)
  local device = h.fake_device(preferences or { ipAddress = "192.168.1.20", secret = "s" })
  device.device_network_id = "pc-control-test"
  return device
end

--- Run `fn(calls)` with the service and the poll swapped out.
--
-- Everything init.lua reaches the network through is a module table field, so
-- replacing the field is enough - and it is restored afterwards even when the
-- test fails, because the suites share `package.loaded`.
-- @param opts `fail` (an err_kind the service answers with), `cancelled`
local function with_service(opts, fn)
  opts = opts or {}
  local calls = { commands = {}, cancels = 0, polls = 0, wakes = 0 }
  local original = {
    command = client.command,
    cancel = client.cancel,
    once = poll.once,
    wake = wol.wake,
  }

  client.command = function(_, command, mode, minutes)
    calls.commands[#calls.commands + 1] =
      { command = command, mode = mode, minutes = minutes }
    if opts.fail then
      return false, nil, opts.fail
    end
    return true, {}, nil
  end
  client.cancel = function()
    calls.cancels = calls.cancels + 1
    if opts.fail then
      return false, nil, opts.fail
    end
    return true, { cancelled = opts.cancelled ~= false }, nil
  end
  poll.once = function()
    calls.polls = calls.polls + 1
    return true
  end
  wol.wake = function()
    calls.wakes = calls.wakes + 1
  end

  local ok, err = pcall(fn, calls)

  client.command, client.cancel = original.command, original.cancel
  poll.once, wol.wake = original.once, original.wake

  if not ok then
    error(err, 0)
  end
  return calls
end

--- The `lastAction` value a device was last told to show, or nil.
local function last_action(device)
  return h.event_value(h.emitted(device), caps.COMMAND, "lastAction")
end

--------------------------------------------------------------------------------
-- lastAction, one row per command (#82)
--------------------------------------------------------------------------------

-- capability command -> the service command it sends and the enum value the
-- row ends up showing (§4.3 / §5.1).
local BUTTONS = {
  { command = "suspend", sends = "suspend", action = "suspend" },
  { command = "hibernate", sends = "hibernate", action = "hibernate" },
  { command = "restart", sends = "restart", action = "restart" },
  { command = "shutdown", sends = "shutdown", action = "shutdown" },
  { command = "lock", sends = "lock", action = "lock" },
  { command = "screenOff", sends = "turnscreenoff", action = "screenOff" },
  { command = "screenOn", sends = "turnscreenon", action = "screenOn" },
}

function T.test_every_no_argument_command_paints_its_own_action()
  local handlers = handlers_for(caps.COMMAND)
  for _, case in ipairs(BUTTONS) do
    local device = device_with()
    with_service(nil, function(calls)
      handlers[case.command](driver, device, { command = case.command, args = {} })
      h.assert_equal(#calls.commands, 1, case.command .. " sent no command")
      h.assert_equal(calls.commands[1].command, case.sends, case.command .. " service command")
      h.assert_equal(calls.polls, 1, case.command .. " did not refresh the tiles")
    end)
    h.assert_equal(last_action(device), case.action, case.command .. " lastAction")
  end
end

function T.test_wake_never_reaches_the_service()
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).wake(driver, device, { command = "wake", args = {} })
  end)
  h.assert_equal(#calls.commands, 0, "wake is the WoL sequence, not a service command")
  h.assert_equal(calls.wakes, 1)
  h.assert_equal(last_action(device), "wake")
  -- §6.2: the switch follows the power state, which is now `waking`.
  h.assert_equal(h.event_value(h.emitted(device), caps.POWER_STATE, "powerState"), state.WAKING)
end

function T.test_the_detail_view_list_sends_execute_with_the_command_only()
  -- #82: the list has one argument, so mode comes from `buttonMode` and the
  -- delay is 0 - the same behaviour the push buttons had.
  local device = device_with({ ipAddress = "192.168.1.20", buttonMode = "immediate" })
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "turnscreenoff" } })
  end)
  h.assert_equal(calls.commands[1].command, "turnscreenoff")
  h.assert_equal(calls.commands[1].mode, "immediate")
  h.assert_equal(calls.commands[1].minutes, 0)
  h.assert_equal(last_action(device), "screenOff")
end

function T.test_execute_from_an_automation_keeps_its_own_mode()
  local device = device_with({ ipAddress = "192.168.1.20", buttonMode = "immediate" })
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "forceshutdown", mode = "grace", minutes = 0 } })
  end)
  h.assert_equal(calls.commands[1].mode, "grace")
  -- `forceshutdown` has no enum value of its own (§5.1).
  h.assert_equal(last_action(device), "shutdown")
end

function T.test_execute_can_wake_from_the_list()
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "wake" } })
  end)
  h.assert_equal(#calls.commands, 0)
  h.assert_equal(calls.wakes, 1)
  h.assert_equal(last_action(device), "wake")
end

function T.test_a_scheduled_execute_does_not_claim_it_already_ran()
  -- `minutes > 0` schedules instead of executing (§4.3); what is pending is
  -- the pcTimer row's business.
  local device = device_with()
  with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "shutdown", mode = "default", minutes = 30 } })
  end)
  h.assert_nil(last_action(device))
end

function T.test_a_refused_command_paints_no_action()
  local device = device_with()
  local calls = with_service({ fail = "unauthorized" }, function()
    handlers_for(caps.COMMAND).shutdown(driver, device, { command = "shutdown", args = {} })
  end)
  h.assert_equal(#calls.commands, 1)
  h.assert_equal(calls.polls, 0, "a refused command has nothing to refresh")
  h.assert_nil(last_action(device), "the PC did not act, so the row must not say it did")
  h.assert_equal(h.event_value(h.emitted(device), caps.STATUS, "connection"), "unauthorized")
end

function T.test_the_switch_paints_the_action_it_stands_for()
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "turnscreenoff" })
  local switch = handlers_for("switch")
  local calls = with_service(nil, function()
    switch.off(driver, device, { command = "off", args = {} })
  end)
  h.assert_equal(calls.commands[1].command, "turnscreenoff")
  h.assert_equal(last_action(device), "screenOff")

  device = device_with()
  with_service(nil, function()
    switch.on(driver, device, { command = "on", args = {} })
  end)
  h.assert_equal(last_action(device), "wake")
end

function T.test_a_device_that_has_run_nothing_shows_none()
  -- #82: an attribute that was never emitted reads as "-" on the phone, so
  -- `added` paints `none` and a poll fills it in for a device from before.
  local device = device_with()
  driver.lifecycle_handlers.added(driver, device)
  h.assert_equal(last_action(device), state.ACTION_NONE)

  -- ... and only once: the value a command wrote is not reset by the next poll.
  local emitted_before = #device.emitted
  poll.ensure_action(device)
  h.assert_equal(#device.emitted, emitted_before, "none was painted twice")
end

--------------------------------------------------------------------------------
-- pcTimer: the cancel entry of the preset list (#82)
--------------------------------------------------------------------------------

function T.test_schedule_zero_cancels()
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = 0 } })
  end)
  h.assert_equal(calls.cancels, 1, "minutes = 0 is the list's Cancel entry")
  h.assert_equal(#calls.commands, 0, "nothing may be scheduled for zero minutes")
end

function T.test_schedule_zero_as_a_string_cancels_too()
  -- The list sends its alternative key, which is a string on some firmwares.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = "0" } })
  end)
  h.assert_equal(calls.cancels, 1)
end

function T.test_the_cancel_command_is_still_handled()
  -- The definition is unchanged (the hub caches definitions by id, §14.4), so
  -- `cancel()` still arrives from devices on an older profile.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).cancel(driver, device, { command = "cancel", args = {} })
  end)
  h.assert_equal(calls.cancels, 1)
end

function T.test_a_preset_still_schedules()
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "suspend" })
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = 30 } })
  end)
  h.assert_equal(calls.cancels, 0)
  h.assert_equal(#calls.commands, 1)
  -- No command in the list, so the switch-off action decides (§4.3).
  h.assert_equal(calls.commands[1].command, "suspend")
  h.assert_equal(calls.commands[1].minutes, 30)
end

return T
