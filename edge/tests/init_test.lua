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

--- Every `lastAction` value a device was told, in order.
local function all_actions(device)
  local out = {}
  for _, e in ipairs(h.emitted(device)) do
    if e.cap == caps.COMMAND and e.attr == "lastAction" then
      out[#out + 1] = e.value
    end
  end
  return out
end

--- The `planCommand` a device was last told to show, or nil.
-- #85: emitted on the schedule capability, with the row it drives.
local function plan_command(device)
  return h.event_value(h.emitted(device), caps.SCHEDULE, "planCommand")
end

--------------------------------------------------------------------------------
-- the command row rests on `none` (#84)
--------------------------------------------------------------------------------

-- capability command -> the service command it sends (§4.3).
local BUTTONS = {
  { command = "suspend", sends = "suspend" },
  { command = "hibernate", sends = "hibernate" },
  { command = "restart", sends = "restart" },
  { command = "shutdown", sends = "shutdown" },
  { command = "lock", sends = "lock" },
  { command = "screenOff", sends = "turnscreenoff" },
  { command = "screenOn", sends = "turnscreenon" },
}

function T.test_every_no_argument_command_sends_its_service_command()
  local handlers = handlers_for(caps.COMMAND)
  for _, case in ipairs(BUTTONS) do
    local device = device_with()
    with_service(nil, function(calls)
      handlers[case.command](driver, device, { command = case.command, args = {} })
      h.assert_equal(#calls.commands, 1, case.command .. " sent no command")
      h.assert_equal(calls.commands[1].command, case.sends, case.command .. " service command")
      h.assert_equal(calls.polls, 1, case.command .. " did not refresh the tiles")
    end)
    -- #84: what ran is read off `lastCommand`, which the poll fills in. The
    -- picker row itself must stay on `none` - it is what a dismissed list
    -- sends back as the argument.
    h.assert_deep_equal(all_actions(device), {}, case.command .. " touched lastAction")
  end
end

function T.test_no_command_ever_paints_an_action()
  -- The invariant #84 rests on, over every path that used to flash a value.
  local handlers = handlers_for(caps.COMMAND)
  local cases = {
    function(device) handlers.lock(driver, device, { command = "lock", args = {} }) end,
    function(device) handlers.wake(driver, device, { command = "wake", args = {} }) end,
    function(device)
      handlers.execute(driver, device, { command = "execute", args = { command = "turnscreenoff" } })
    end,
    function(device)
      handlers.execute(driver, device,
        { command = "execute", args = { command = "forceshutdown", mode = "grace" } })
    end,
    function(device)
      handlers_for("switch").off(driver, device, { command = "off", args = {} })
    end,
    function(device)
      handlers_for("switch").on(driver, device, { command = "on", args = {} })
    end,
  }
  for i, run in ipairs(cases) do
    local device = device_with()
    driver.timers = {}
    with_service(nil, function() run(device) end)
    for _, value in ipairs(all_actions(device)) do
      h.assert_equal(value, state.ACTION_NONE,
        string.format("case %d emitted lastAction = %s", i, tostring(value)))
    end
    for _, timer in ipairs(driver.timers) do
      h.assert_true(timer.name ~= "action-reset",
        "the 5 s flash timer is gone (#84)")
    end
  end
end

function T.test_a_dismissed_command_list_does_nothing()
  -- Measured on the phone (2026-09-22): closing the list without picking
  -- anything sends the row's current value, which is `none`. It has to be a
  -- valid argument (or the cloud refuses it) and it has to be harmless.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "none" } })
  end)
  h.assert_equal(#calls.commands, 0, "`none` must not reach the service")
  h.assert_equal(calls.wakes, 0, "`none` must not wake the PC either")
  h.assert_equal(calls.polls, 1, "`none` refreshes the tiles")
  h.assert_deep_equal(all_actions(device), {})
end

function T.test_an_execute_without_a_command_does_nothing()
  -- Some firmwares send the list's argument as an empty string.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device, { command = "execute", args = {} })
  end)
  h.assert_equal(#calls.commands, 0)
  h.assert_equal(calls.polls, 1)
end

function T.test_wake_never_reaches_the_service()
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).wake(driver, device, { command = "wake", args = {} })
  end)
  h.assert_equal(#calls.commands, 0, "wake is the WoL sequence, not a service command")
  h.assert_equal(calls.wakes, 1)
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
end

function T.test_execute_from_an_automation_keeps_its_own_mode()
  local device = device_with({ ipAddress = "192.168.1.20", buttonMode = "immediate" })
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "forceshutdown", mode = "grace", minutes = 0 } })
  end)
  h.assert_equal(calls.commands[1].command, "forceshutdown")
  h.assert_equal(calls.commands[1].mode, "grace")
end

function T.test_execute_can_wake_from_the_list()
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "wake" } })
  end)
  h.assert_equal(#calls.commands, 0)
  h.assert_equal(calls.wakes, 1)
end

function T.test_a_scheduled_execute_still_goes_to_the_service()
  -- `minutes > 0` schedules instead of executing (§4.3); what is pending is
  -- the pcCountdown row's business.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "shutdown", mode = "default", minutes = 30 } })
  end)
  h.assert_equal(calls.commands[1].minutes, 30)
  h.assert_deep_equal(all_actions(device), {})
end

function T.test_a_refused_command_says_so()
  local device = device_with()
  local calls = with_service({ fail = "unauthorized" }, function()
    handlers_for(caps.COMMAND).shutdown(driver, device, { command = "shutdown", args = {} })
  end)
  h.assert_equal(#calls.commands, 1)
  h.assert_equal(calls.polls, 0, "a refused command has nothing to refresh")
  h.assert_equal(h.event_value(h.emitted(device), caps.STATUS, "connection"), "unauthorized")
end

function T.test_the_switch_off_sends_the_configured_action()
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "turnscreenoff" })
  local calls = with_service(nil, function()
    handlers_for("switch").off(driver, device, { command = "off", args = {} })
  end)
  h.assert_equal(calls.commands[1].command, "turnscreenoff")
end

function T.test_a_new_device_shows_the_placeholder_and_a_plan_command()
  -- #82: an attribute that was never emitted reads as "-" on the phone, so
  -- `added` paints `none` and a poll fills it in for a device from before.
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "hibernate" })
  driver.lifecycle_handlers.added(driver, device)
  h.assert_equal(last_action(device), state.ACTION_NONE)
  -- #84: the schedule row's command, defaulted from `offAction`.
  h.assert_equal(plan_command(device), "hibernate")

  -- ... and only once: neither row is repainted by the next poll.
  local emitted_before = #device.emitted
  poll.ensure_action(device)
  poll.ensure_plan_command(device)
  poll.ensure_rows(device)
  h.assert_equal(#device.emitted, emitted_before, "a row was painted twice")
end

function T.test_a_new_device_reports_every_command_and_schedule_attribute()
  -- #85: an attribute that was never emitted reads as "-" on the phone and
  -- keeps the app saying the device has not reported all of its state. No
  -- status body carries `lastAction` / `planCommand`, and a device that has
  -- just been added has no status body at all, so the whole set is painted at
  -- its resting value up front.
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "restart" })
  driver.lifecycle_handlers.added(driver, device)

  local seen = {}
  for _, e in ipairs(h.emitted(device)) do
    seen[e.cap .. "." .. e.attr] = true
  end
  local used = state.attributes_used()
  for _, id in ipairs({ caps.COMMAND, caps.SCHEDULE }) do
    for attr in pairs(used[id]) do
      h.assert_true(seen[id .. "." .. attr] == true,
        id .. "." .. attr .. ' was never emitted, so the row reads "-"')
    end
  end

  -- #85: and the version row of the info card, which no status body has filled
  -- in yet - it names the driver and the screen template regardless.
  h.assert_true(seen[caps.STATUS .. ".versions"] == true,
    "the versions row was never emitted")
  h.assert_equal(h.event_value(h.emitted(device), caps.STATUS, "versions"),
    state.versions(nil, nil))
end

function T.test_a_migrated_device_repaints_the_rows_the_old_ids_held()
  -- #85: pcPlan/pcRun became pcCountdown/pcExec, so on the hub every attribute
  -- of the new ids starts out unset - but the driver's own persisted fields
  -- survive the migration and would otherwise say "already painted".
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "shutdown" })
  device:set_field(poll.ACTION_FIELD, state.ACTION_NONE, { persist = true })
  device:set_field(poll.PLAN_FIELD, "suspend", { persist = true })

  h.assert_true(poll.ensure_rows(device), "a migrated device has to be repainted")
  h.assert_equal(last_action(device), state.ACTION_NONE)
  h.assert_equal(plan_command(device), "suspend",
    "the command the user picked survives the rename")
  h.assert_false(poll.ensure_rows(device), "... and is painted only once")
end

--------------------------------------------------------------------------------
-- pcCountdown.setPlanCommand: what a schedule runs (#84, moved in #85)
--------------------------------------------------------------------------------

function T.test_set_plan_command_persists_and_emits()
  local device = device_with()
  handlers_for(caps.SCHEDULE).setPlanCommand(driver, device,
    { command = "setPlanCommand", args = { command = "restart" } })
  h.assert_equal(plan_command(device), "restart")
  h.assert_equal(device:get_field(poll.PLAN_FIELD), "restart",
    "the choice has to survive a hub restart")
end

function T.test_set_plan_command_refuses_a_command_the_service_cannot_schedule()
  local device = device_with()
  handlers_for(caps.SCHEDULE).setPlanCommand(driver, device,
    { command = "setPlanCommand", args = { command = "lock" } })
  h.assert_equal(plan_command(device), "shutdown", "an unschedulable value is coerced")
end

function T.test_a_schedule_without_a_command_uses_the_picked_one()
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "shutdown" })
  handlers_for(caps.SCHEDULE).setPlanCommand(driver, device,
    { command = "setPlanCommand", args = { command = "suspend" } })
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = 30 } })
  end)
  h.assert_equal(calls.commands[1].command, "suspend",
    "the detail view's schedule list only picks the minutes (#84)")
end

function T.test_an_explicit_schedule_command_still_wins()
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "shutdown" })
  handlers_for(caps.SCHEDULE).setPlanCommand(driver, device,
    { command = "setPlanCommand", args = { command = "suspend" } })
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = 30, command = "restart" } })
  end)
  h.assert_equal(calls.commands[1].command, "restart")
end

--------------------------------------------------------------------------------
-- pcCountdown: the cancel entry of the preset list (#82, valid argument since #85)
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
