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
  -- #86: `poll_opts` keeps what each poll was asked for, so a test can see
  -- which rows a command declared it is answering (`force`).
  local calls = { commands = {}, cancels = 0, polls = 0, wakes = 0, poll_opts = {} }
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
  poll.once = function(_driver, _device, poll_opts)
    calls.polls = calls.polls + 1
    calls.poll_opts[#calls.poll_opts + 1] = poll_opts or {}
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

--- #84/#86: every `lastAction` a command emitted is the resting `none`, and
--- #86 requires at least one of them to be forced: the row never changes value,
--- so an unforced event is dropped by the platform and the app spins until it
--- fails (platform notes "상세 화면(detailView) 위젯").
local function assert_answers_with_none(device, context)
  local values = all_actions(device)
  for _, value in ipairs(values) do
    h.assert_equal(value, state.ACTION_NONE,
      (context or "") .. " moved lastAction off `none`")
  end
  h.assert_true(#values > 0, (context or "") .. " never answered the command row (#86)")
  h.assert_true(h.event_forced(h.emitted(device), caps.COMMAND, "lastAction"),
    (context or "") .. " answered lastAction without state_change (#86)")
end

--- The `planCommand` a device was last told to show, or nil.
-- #85: emitted on the schedule capability, with the row it drives.
local function plan_command(device)
  return h.event_value(h.emitted(device), caps.SCHEDULE, "planCommand")
end

--------------------------------------------------------------------------------
-- the command row rests on `none` (#84)
--------------------------------------------------------------------------------

-- capability command -> the service command it sends (§3.3).
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
    -- sends back as the argument. #86: and it is re-emitted, forced, as the
    -- answer the app is waiting for.
    assert_answers_with_none(device, case.command)
  end
end

function T.test_no_command_ever_paints_an_action()
  -- The invariant #84 rests on, over every path that used to flash a value: the
  -- row never shows what was just run - `lastCommand` does that - it only ever
  -- shows what it is resting on. #93 gave it a second resting value for the
  -- time a transition is running, so `wake` and `switch on` leave it on
  -- `busyWake`; neither is the command that ran.
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
      h.assert_true(value == state.ACTION_NONE or state.is_busy_action(value),
        string.format("case %d emitted lastAction = %s, which is neither resting "
          .. "value (#84, #93)", i, tostring(value)))
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
  -- #86: even the no-op answers the row, or the app spins and then errors.
  assert_answers_with_none(device, "the dismissed list")
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
  -- `minutes > 0` schedules instead of executing (§3.3); what is pending is
  -- the pcDefer row's business.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "shutdown", mode = "default", minutes = 30 } })
  end)
  h.assert_equal(calls.commands[1].minutes, 30)
  assert_answers_with_none(device, "a scheduled execute")
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
  -- #86: and the capability the row actually lives on now, which is new and
  -- therefore unset on every device until this paint.
  h.assert_true(seen[caps.VERSION .. ".versions"] == true,
    "the pcVersion row was never emitted")
  h.assert_equal(h.event_value(h.emitted(device), caps.VERSION, "versions"),
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
-- pcDefer.setPlanCommand: what a schedule runs (#84, moved in #85)
--------------------------------------------------------------------------------

function T.test_set_plan_command_persists_and_emits()
  local device = device_with()
  handlers_for(caps.SCHEDULE).setPlanCommand(driver, device,
    { command = "setPlanCommand", args = { command = "restart" } })
  h.assert_equal(plan_command(device), "restart")
  h.assert_equal(device:get_field(poll.PLAN_FIELD), "restart",
    "the choice has to survive a hub restart")
end

function T.test_set_plan_command_answers_even_when_the_value_does_not_change()
  -- #86, measured on the phone: picking the value the row already shows
  -- (restart -> restart) changes no attribute, the platform drops the event and
  -- the app keeps a spinner up until it fails with an error. So the answer goes
  -- out with `state_change = true` (platform notes "상세 화면(detailView) 위젯").
  local device = device_with()
  local pick = function()
    handlers_for(caps.SCHEDULE).setPlanCommand(driver, device,
      { command = "setPlanCommand", args = { command = "restart" } })
  end
  pick()
  pick()

  local emits = 0
  for _, e in ipairs(h.emitted(device)) do
    if e.cap == caps.SCHEDULE and e.attr == "planCommand" then
      emits = emits + 1
      h.assert_equal(e.value, "restart")
      h.assert_true((e.options or {}).state_change == true,
        "setPlanCommand must force the event (#86)")
    end
  end
  h.assert_equal(emits, 2, "the second, unchanged pick has to be answered too")
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
-- pcDefer: the cancel entry of the preset list (#82, valid argument since #85)
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

--------------------------------------------------------------------------------
-- pcDefer: the 예약 시간 row rests on a no-op delay (#88)
--------------------------------------------------------------------------------

--- Every `minutesPick` event a device was told, with its options.
local function minutes_picks(device)
  local out = {}
  for _, e in ipairs(h.emitted(device)) do
    if e.cap == caps.SCHEDULE and e.attr == "minutesPick" then
      out[#out + 1] = e
    end
  end
  return out
end

function T.test_a_dismissed_delay_picker_does_nothing()
  -- #88, measured on the phone: closing the 예약 시간 list without picking
  -- anything sends the row's current value as `minutes`. The row rests on
  -- `minutesPick` = "-1", so the command arrives and has to be a no-op: no
  -- schedule, no cancel, just a refresh of the tiles.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = -1 } })
  end)
  h.assert_equal(#calls.commands, 0, "a dismissed picker may not schedule anything")
  h.assert_equal(calls.cancels, 0, "... and may not cancel what is scheduled either")
  h.assert_equal(calls.polls, 1, "the tiles are refreshed, as a dismissed command list is")
end

function T.test_a_dismissed_delay_picker_as_a_string_does_nothing_too()
  -- The list sends its alternative key, which is a string on some firmwares.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = state.MINUTES_PICK } })
  end)
  h.assert_equal(#calls.commands, 0)
  h.assert_equal(calls.cancels, 0)

  -- ... and so is a `schedule` that carries no minutes at all.
  local device2 = device_with()
  local calls2 = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device2, { command = "schedule", args = {} })
  end)
  h.assert_equal(#calls2.commands, 0)
  h.assert_equal(calls2.cancels, 0)
end

--------------------------------------------------------------------------------
-- pcDefer: every delay the app sends is a string (#91)
--------------------------------------------------------------------------------

-- #91, measured on the phone (2026-09-26): the value a dismissed list sends is
-- not "a string on some firmwares" - it is ALWAYS a string, because closing the
-- list skips the presentation's `argumentType` conversion. `schedule(-1)`
-- reached the hub and `schedule("-1")` came back 422 from the cloud, so the
-- definition says `minutes` is a string enum and these are the arguments the
-- driver actually receives now.
function T.test_the_delay_row_takes_every_value_as_a_string()
  -- The dismissed picker: the row's resting value, verbatim off the wire.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = "-1" } })
  end)
  h.assert_equal(#calls.commands, 0, '"-1" may not schedule anything')
  h.assert_equal(calls.cancels, 0, '... and may not cancel anything either')
  h.assert_equal(calls.polls, 1, "the tiles are refreshed instead")
  local picks = minutes_picks(device)
  h.assert_equal(#picks, 1, "the delay row is answered once (#88)")
  h.assert_equal(picks[1].value, state.MINUTES_PICK)
  h.assert_true((picks[1].options or {}).state_change == true,
    "the re-emit has to be forced or the app spins (#86)")

  -- The Cancel entry.
  local cancelling = device_with()
  local cancel_calls = with_service({ cancelled = true }, function()
    handlers_for(caps.SCHEDULE).schedule(driver, cancelling,
      { command = "schedule", args = { minutes = "0" } })
  end)
  h.assert_equal(cancel_calls.cancels, 1, '"0" is the list\'s Cancel entry')
  h.assert_equal(#cancel_calls.commands, 0)

  -- A picked preset.
  local scheduling = device_with({ ipAddress = "192.168.1.20", offAction = "shutdown" })
  local schedule_calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, scheduling,
      { command = "schedule", args = { minutes = "30" } })
  end)
  h.assert_equal(#schedule_calls.commands, 1, '"30" schedules')
  h.assert_equal(schedule_calls.commands[1].minutes, 30,
    "the driver reads the number back out of the key it was sent")
  h.assert_equal(schedule_calls.commands[1].command, "shutdown")
  h.assert_equal(cancel_calls.cancels, 1)
end

function T.test_a_delay_the_definition_does_not_allow_is_a_no_op()
  -- Nothing outside the enum can reach the hub any more - the cloud rejects it
  -- first - but a device on an older profile, or a routine written against one,
  -- can still send something else. It may not be read as a schedule.
  for _, minutes in ipairs({ "idle", "", "nonsense" }) do
    local device = device_with()
    local calls = with_service(nil, function()
      handlers_for(caps.SCHEDULE).schedule(driver, device,
        { command = "schedule", args = { minutes = minutes } })
    end)
    h.assert_equal(#calls.commands, 0,
      string.format('schedule(%q) may not schedule anything', minutes))
    h.assert_equal(calls.cancels, 0)
    h.assert_equal(#minutes_picks(device), 1, "the row is still answered")
  end
end

function T.test_every_schedule_answers_the_delay_row_with_the_resting_value()
  -- #86's rule on #88's row: `minutesPick` has one value, so the attribute the
  -- app is waiting on never changes and the platform drops an unforced event -
  -- the spinner then runs out into an error. Every `schedule`, no-op included,
  -- answers it with a forced re-emit of "-1".
  -- #91: the strings are what the app really sends; the numbers are what an
  -- automation written against an older definition still carries.
  local cases = {
    ["schedule(-1)"] = -1,
    ["schedule(0)"] = 0,
    ["schedule(30)"] = 30,
    ['schedule("-1")'] = "-1",
    ['schedule("0")'] = "0",
    ['schedule("30")'] = "30",
  }
  for name, minutes in pairs(cases) do
    local device = device_with()
    with_service({ cancelled = false }, function()
      handlers_for(caps.SCHEDULE).schedule(driver, device,
        { command = "schedule", args = { minutes = minutes } })
    end)
    local picks = minutes_picks(device)
    h.assert_equal(#picks, 1, name .. " has to answer the delay row exactly once (#88)")
    h.assert_equal(picks[1].value, state.MINUTES_PICK,
      name .. " moved the delay row off its resting value")
    h.assert_true((picks[1].options or {}).state_change == true,
      name .. " answered minutesPick without state_change (#86)")
  end
end

function T.test_a_refused_schedule_still_answers_the_delay_row()
  -- The row is answered before the service is asked anything: a command that
  -- the PC refuses must not leave the app spinning either.
  local device = device_with()
  with_service({ fail = "unreachable" }, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = 30 } })
  end)
  h.assert_equal(#minutes_picks(device), 1)
end

function T.test_a_new_device_paints_the_delay_row()
  -- An attribute that was never emitted reads as "-" and the list does not
  -- open at all, so the row is painted before the first poll (#88).
  local device = device_with()
  driver.lifecycle_handlers.added(driver, device)
  h.assert_equal(h.event_value(h.emitted(device), caps.SCHEDULE, "minutesPick"),
    state.MINUTES_PICK)
end

function T.test_the_cancel_command_is_still_handled()
  -- The definition is unchanged (the hub caches definitions by id, platform notes "허브의 정의 캐시"), so
  -- `cancel()` still arrives from devices on an older profile.
  local device = device_with()
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).cancel(driver, device, { command = "cancel", args = {} })
  end)
  h.assert_equal(calls.cancels, 1)
end

function T.test_schedule_and_cancel_answer_the_rows_the_list_is_bound_to()
  -- #86, measured on the phone: cancelling while nothing is scheduled leaves
  -- every schedule row exactly as it was, so the app's spinner runs out into an
  -- error. The poll that follows the command therefore declares which rows it
  -- is answering, and `poll.emit` sends those with `state_change = true`.
  local function forced_rows(run)
    local device = device_with({ ipAddress = "192.168.1.20", offAction = "shutdown" })
    local calls = with_service({ cancelled = false }, function() run(device) end)
    h.assert_equal(#calls.poll_opts, 1, "the command has to refresh the tiles")
    return calls.poll_opts[1].force
  end

  local cases = {
    ["schedule(30)"] = function(device)
      handlers_for(caps.SCHEDULE).schedule(driver, device,
        { command = "schedule", args = { minutes = 30 } })
    end,
    ["schedule(0)"] = function(device)
      handlers_for(caps.SCHEDULE).schedule(driver, device,
        { command = "schedule", args = { minutes = 0 } })
    end,
    ["cancel()"] = function(device)
      handlers_for(caps.SCHEDULE).cancel(driver, device, { command = "cancel", args = {} })
    end,
  }
  for name, run in pairs(cases) do
    local force = forced_rows(run)
    h.assert_true(type(force) == "table", name .. " forces no row (#86)")
    for _, attr in ipairs({ "status", "summary" }) do
      h.assert_true(force[caps.SCHEDULE .. "." .. attr] == true,
        name .. " does not answer the " .. attr .. " row (#86)")
    end
  end
end

--------------------------------------------------------------------------------
-- #93: nothing else goes out while the PC is in a power transition
--------------------------------------------------------------------------------

local i18n = require "i18n"

--- A device sitting in `power` (and, with `schedule`, in the PC's own grace
--- period - which is all a `switch off` with a grace really is).
local function busy_device(power, schedule)
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "shutdown" })
  local s = state.new(power)
  if schedule then
    s.schedule_active = true
    s.schedule_command = schedule.command
    s.schedule_seconds = schedule.seconds
    -- The PC's own grace period (§3.2). Left out, `state.grace_limit` falls
    -- back to `state.GRACE_SECONDS` for a service too old to send one.
    s.grace_seconds = schedule.grace
  end
  poll.set_state(device, s)
  return device
end

--- The last `pcInfo.message` / `pcInfo.summary` a device was told.
local function note_of(device)
  local emitted = h.emitted(device)
  return h.event_value(emitted, caps.STATUS, "message"),
    h.event_value(emitted, caps.STATUS, "summary")
end

-- Every command the guard holds back, and the row each one has to answer.
local BLOCKED = {
  ['execute("shutdown")'] = function(device)
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "shutdown" } })
  end,
  ['execute("restart")'] = function(device)
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "restart" } })
  end,
  ['execute("suspend")'] = function(device)
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "suspend" } })
  end,
  ['execute("forceshutdown")'] = function(device)
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "forceshutdown", mode = "immediate" } })
  end,
  -- The PC is leaving or not up yet, so it cannot lock or drive its screen
  -- either - these are held back for the same reason, not as power commands.
  ['execute("lock")'] = function(device)
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "lock" } })
  end,
  ['execute("turnscreenoff")'] = function(device)
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "turnscreenoff" } })
  end,
  ['execute("turnscreenon")'] = function(device)
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "turnscreenon" } })
  end,
  -- The no-argument commands an older profile's buttons and a scene still send.
  ["shutdown()"] = function(device)
    handlers_for(caps.COMMAND).shutdown(driver, device, { command = "shutdown", args = {} })
  end,
  ["lock()"] = function(device)
    handlers_for(caps.COMMAND).lock(driver, device, { command = "lock", args = {} })
  end,
  ["switch off"] = function(device)
    handlers_for("switch").off(driver, device, { command = "off", args = {} })
  end,
  ["schedule(30)"] = function(device)
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = "30" } })
  end,
  ["setPlanCommand(restart)"] = function(device)
    handlers_for(caps.SCHEDULE).setPlanCommand(driver, device,
      { command = "setPlanCommand", args = { command = "restart" } })
  end,
}

function T.test_nothing_reaches_the_service_while_the_pc_is_shutting_down()
  for name, run in pairs(BLOCKED) do
    local device = busy_device(state.SHUTTING_DOWN)
    local calls = with_service(nil, function() run(device) end)
    h.assert_equal(#calls.commands, 0, name .. " reached the service mid-shutdown (#93)")
    h.assert_equal(calls.cancels, 0, name .. " cancelled something")
    h.assert_equal(calls.wakes, 0, name .. " woke the PC")
    h.assert_equal(calls.polls, 0, name .. " polled instead of answering")

    -- ... and the user is told why, on both status rows, until the next poll.
    local message, summary = note_of(device)
    local expected = i18n.busy(nil, state.ACTION_BUSY_OFF)
    h.assert_equal(message, expected, name .. " left no note in pcInfo.message")
    h.assert_equal(summary, expected, name .. " left no note in pcInfo.summary")
  end
end

function T.test_nothing_reaches_the_service_while_the_pc_is_waking()
  for name, run in pairs(BLOCKED) do
    local device = busy_device(state.WAKING)
    local calls = with_service(nil, function() run(device) end)
    h.assert_equal(#calls.commands, 0, name .. " reached the service while waking (#93)")
    h.assert_equal(calls.cancels, 0)
    h.assert_equal(calls.polls, 0)
    h.assert_equal(select(2, note_of(device)), i18n.busy(nil, state.ACTION_BUSY_WAKE), name)
  end
end

function T.test_nothing_reaches_the_service_during_the_grace_period()
  -- §3.3: a `switch off` that follows the PC's grace is not executed - it comes
  -- back as a schedule a minute away, with powerState still `on`. That minute
  -- is a transition too (state.GRACE_SECONDS).
  for name, run in pairs(BLOCKED) do
    local device = busy_device(state.ON, { command = "restart", seconds = 60 })
    local calls = with_service(nil, function() run(device) end)
    h.assert_equal(#calls.commands, 0, name .. " reached the service mid-grace (#93)")
    h.assert_equal(calls.cancels, 0)
    h.assert_equal(select(1, note_of(device)), i18n.busy(nil, state.ACTION_BUSY_RESTART), name)
  end
end

function T.test_a_long_schedule_blocks_nothing()
  -- The other half of the rule: a PC that shuts down in three days is an
  -- ordinary, fully usable PC.
  local device = busy_device(state.ON, { command = "shutdown", seconds = 259200, grace = 60 })
  local calls = with_service(nil, function()
    handlers_for(caps.COMMAND).execute(driver, device,
      { command = "execute", args = { command = "lock" } })
  end)
  h.assert_equal(#calls.commands, 1, "a three-day schedule must not block a command (#93)")
  h.assert_equal(calls.commands[1].command, "lock")
end

function T.test_the_same_four_minutes_blocks_on_one_pc_and_not_on_another()
  -- The bound is the PC's own `grace.seconds` (§3.2), so an identical schedule
  -- means two different things on two differently configured PCs.
  local function locks(grace)
    local device = busy_device(state.ON,
      { command = "shutdown", seconds = 240, grace = grace })
    local calls = with_service(nil, function()
      handlers_for(caps.COMMAND).execute(driver, device,
        { command = "execute", args = { command = "lock" } })
    end)
    return #calls.commands
  end
  h.assert_equal(locks(300), 0,
    "four minutes left of a five-minute grace is the PC leaving (#93)")
  h.assert_equal(locks(60), 1,
    "four minutes on a one-minute grace is a schedule the user set (#93)")
end

function T.test_the_grace_length_is_learned_from_the_status_body()
  -- End to end through the poll's own glue: `remember_schedule` is what puts
  -- the service's number where the guard reads it.
  local device = device_with()
  local s = state.remember_schedule(state.new(state.ON), {
    grace = { enabled = true, seconds = 300 },
    schedule = { active = true, command = "shutdown", remaining_seconds = 240 },
  })
  poll.set_state(device, s)
  h.assert_equal(poll.resting_action(device), state.ACTION_BUSY_OFF)
  local calls = with_service(nil, function()
    handlers_for("switch").off(driver, device, { command = "off", args = {} })
  end)
  h.assert_equal(#calls.commands, 0, "the guard has to use the PC's own grace (#93)")
end

function T.test_every_blocked_command_still_answers_its_row()
  -- #86's rule, which the guard must not break: the app is waiting for an event
  -- on the row the command came from, and without one the spinner runs out into
  -- an error. A refused command answers the row with a forced re-emit of the
  -- value it already shows.
  local function rows(run)
    local device = busy_device(state.SHUTTING_DOWN)
    with_service(nil, function() run(device) end)
    return h.emitted(device)
  end

  -- The command list: the busy resting value, forced.
  local emitted = rows(BLOCKED['execute("shutdown")'])
  h.assert_equal(h.event_value(emitted, caps.COMMAND, "lastAction"), state.ACTION_BUSY_OFF)
  h.assert_true(h.event_forced(emitted, caps.COMMAND, "lastAction"))

  -- The switch: still "on" while `shuttingDown` (§6.2), so the toggle springs
  -- back - and forced, or the app would never see an unchanged value.
  emitted = rows(BLOCKED["switch off"])
  h.assert_equal(h.event_value(emitted, "switch", "switch"), "on")
  h.assert_true(h.event_forced(emitted, "switch", "switch"),
    "a refused switch off must force the re-emit (#86)")

  -- The delay row: `answer_minutes_pick` runs before the guard.
  emitted = rows(BLOCKED["schedule(30)"])
  h.assert_equal(h.event_value(emitted, caps.SCHEDULE, "minutesPick"), state.MINUTES_PICK)
  h.assert_true(h.event_forced(emitted, caps.SCHEDULE, "minutesPick"))

  -- The "command to schedule" row: the value it already holds, not the new one.
  local device = busy_device(state.SHUTTING_DOWN)
  device:set_field(poll.PLAN_FIELD, "suspend", { persist = true })
  with_service(nil, function() BLOCKED["setPlanCommand(restart)"](device) end)
  h.assert_equal(plan_command(device), "suspend",
    "a refused setPlanCommand must not change the choice (#93)")
  h.assert_equal(device:get_field(poll.PLAN_FIELD), "suspend")
  h.assert_true(h.event_forced(h.emitted(device), caps.SCHEDULE, "planCommand"))
end

function T.test_cancelling_is_always_allowed()
  -- The one thing that can still help mid-transition, in its three shapes.
  for _, power in ipairs({ state.SHUTTING_DOWN, state.WAKING }) do
    local device = busy_device(power, { command = "shutdown", seconds = 60 })
    local calls = with_service({ cancelled = true }, function()
      handlers_for(caps.SCHEDULE).cancel(driver, device, { command = "cancel", args = {} })
    end)
    h.assert_equal(calls.cancels, 1, "cancel() must survive a transition (#93)")

    local zero = busy_device(power, { command = "shutdown", seconds = 60 })
    local zero_calls = with_service({ cancelled = true }, function()
      handlers_for(caps.SCHEDULE).schedule(driver, zero,
        { command = "schedule", args = { minutes = "0" } })
    end)
    h.assert_equal(zero_calls.cancels, 1, 'schedule("0") must survive a transition')

    -- ... and the dismissed picker is still the no-op it always was.
    local dismissed = busy_device(power, { command = "shutdown", seconds = 60 })
    local dismissed_calls = with_service(nil, function()
      handlers_for(caps.SCHEDULE).schedule(driver, dismissed,
        { command = "schedule", args = { minutes = state.MINUTES_PICK } })
    end)
    h.assert_equal(dismissed_calls.cancels, 0)
    h.assert_equal(#dismissed_calls.commands, 0)
    h.assert_equal(dismissed_calls.polls, 1, "a dismissed picker still refreshes the tiles")
  end
end

function T.test_refresh_and_waking_the_pc_are_always_allowed()
  for _, power in ipairs({ state.SHUTTING_DOWN, state.WAKING }) do
    -- `refresh`: asking what is going on is what the user does next.
    local device = busy_device(power)
    local calls = with_service(nil, function()
      handlers_for("refresh").refresh(driver, device, { command = "refresh", args = {} })
    end)
    h.assert_equal(calls.polls, 1, "refresh must survive a transition (#93)")

    -- `switch on` and `execute(wake)` are the same WoL sequence, and re-sending
    -- a magic packet is free. During `shuttingDown` it also keeps the "switch
    -- on cancels the grace" behaviour of §6.2 intact.
    local switched = busy_device(power)
    local switch_calls = with_service(nil, function()
      handlers_for("switch").on(driver, switched, { command = "on", args = {} })
    end)
    h.assert_equal(switch_calls.wakes, 1, "switch on must survive a transition (#93)")
    h.assert_equal(h.event_value(h.emitted(switched), caps.POWER_STATE, "powerState"),
      state.WAKING)

    local woken = busy_device(power)
    local wake_calls = with_service(nil, function()
      handlers_for(caps.COMMAND).execute(driver, woken,
        { command = "execute", args = { command = "wake" } })
    end)
    h.assert_equal(wake_calls.wakes, 1, 'execute("wake") must survive a transition (#93)')
    h.assert_equal(#wake_calls.commands, 0)
  end
end

function T.test_a_dismissed_command_list_is_a_no_op_at_its_busy_value_too()
  -- #84's rule, at the value the row rests on during a transition: closing the
  -- list sends the row's CURRENT value, which is `busyOff` and not `none`, so
  -- `execute("busyOff")` has to be exactly as harmless.
  for _, busy in ipairs(state.BUSY_ACTIONS) do
    local device = busy_device(state.SHUTTING_DOWN)
    local calls = with_service(nil, function()
      handlers_for(caps.COMMAND).execute(driver, device,
        { command = "execute", args = { command = busy } })
    end)
    h.assert_equal(#calls.commands, 0, busy .. " must not reach the service")
    h.assert_equal(calls.wakes, 0, busy .. " must not wake the PC either")
    h.assert_equal(calls.polls, 1, busy .. " refreshes the tiles, as `none` does")
    -- The row is answered, and with the busy value - not with `none`.
    local emitted = h.emitted(device)
    h.assert_equal(h.event_value(emitted, caps.COMMAND, "lastAction"), state.ACTION_BUSY_OFF)
    h.assert_true(h.event_forced(emitted, caps.COMMAND, "lastAction"))
    -- ... and it is not the refusal path, so no note is left behind.
    h.assert_nil(h.event_value(emitted, caps.STATUS, "message"),
      busy .. " left a refusal note, but it refused nothing")
  end
end

function T.test_the_command_row_rests_on_busy_and_comes_back_to_none()
  -- The lifecycle of the resting value, which is what the user reads.
  local device = busy_device(state.ON)
  h.assert_equal(poll.resting_action(device), state.ACTION_NONE)

  -- Into a transition: the poll's own upkeep moves the row.
  poll.set_state(device, state.transition(poll.get_state(device), "stopping", "restart"))
  h.assert_equal(poll.resting_action(device), state.ACTION_BUSY_RESTART)
  h.assert_true(poll.ensure_action(device), "the row has to be moved to busyRestart")
  h.assert_equal(last_action(device), state.ACTION_BUSY_RESTART)

  -- ... and stays there, forced, because an unchanged event is dropped and the
  -- row has to keep saying "in progress".
  device.emitted = {}
  h.assert_true(poll.ensure_action(device), "the busy value is re-emitted")
  h.assert_equal(last_action(device), state.ACTION_BUSY_RESTART)
  h.assert_true(h.event_forced(h.emitted(device), caps.COMMAND, "lastAction"),
    "an unchanged busy re-emit has to be forced (#86)")

  -- Out of it: back to `none`, and then quiet again.
  poll.set_state(device, state.transition(poll.get_state(device), "status_ok"))
  device.emitted = {}
  h.assert_true(poll.ensure_action(device), "the row has to come back to `none`")
  h.assert_equal(last_action(device), state.ACTION_NONE)
  device.emitted = {}
  h.assert_false(poll.ensure_action(device), "an idle row is left alone")
  h.assert_equal(#device.emitted, 0)
end

function T.test_a_preset_still_schedules()
  local device = device_with({ ipAddress = "192.168.1.20", offAction = "suspend" })
  local calls = with_service(nil, function()
    handlers_for(caps.SCHEDULE).schedule(driver, device,
      { command = "schedule", args = { minutes = 30 } })
  end)
  h.assert_equal(calls.cancels, 0)
  h.assert_equal(#calls.commands, 1)
  -- No command in the list, so the switch-off action decides (§3.3).
  h.assert_equal(calls.commands[1].command, "suspend")
  h.assert_equal(calls.commands[1].minutes, 30)
end

return T
