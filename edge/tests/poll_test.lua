-- Poll scheduling: the interval preference and the multi-PC stagger of §6.7.
--
-- The err_kind mapping and a whole poll cycle live in client_test.lua, which
-- has the http fakes; this file is about when the polls happen.

local h = require "helpers"
local Driver = require "st.driver"
local discovery = require "discovery"
local poll = require "poll"

local T = {}

local function fake_driver()
  return Driver("test", {})
end

local function device_with(dni, prefs)
  local device = h.fake_device(prefs or {})
  device.device_network_id = dni
  device.id = "device-" .. dni
  return device
end

--- The most recent live timer with this name (a restart adds another one).
local function timer_named(driver, name)
  local found
  for _, timer in ipairs(driver.timers) do
    if timer.name == name and not timer.cancelled then
      found = timer
    end
  end
  return found
end

local function fire(driver, name)
  local timer = timer_named(driver, name)
  if not timer then
    error("no timer named " .. name, 2)
  end
  return timer.fn()
end

function T.test_interval_comes_from_the_preference()
  h.assert_equal(poll.interval({ pollInterval = "10" }), 10)
  h.assert_equal(poll.interval({ pollInterval = 300 }), 300)
  h.assert_equal(poll.interval({}), 30, "the default is 30s")
  h.assert_equal(poll.interval({ pollInterval = "1" }), 30, "an absurd interval is ignored")
  h.assert_equal(poll.interval(nil), 30)
end

function T.test_offsets_are_inside_the_interval_and_deterministic()
  local dnis = {
    "pc-control-9f3c-guid", "pc-control-a1b2-guid", "pc-control-manual-68c0-1",
    "pc-control-manual-68c0-2", "pc-control-0000-1111-2222",
  }
  for _, dni in ipairs(dnis) do
    local offset = poll.offset(dni, 30)
    h.assert_true(offset >= 0 and offset < 30,
      string.format("%s -> %s is outside [0, 30)", dni, tostring(offset)))
    h.assert_equal(poll.offset(dni, 30), offset, "the same DNI always gets the same slot")
  end
end

function T.test_offsets_of_different_devices_differ()
  -- §6.7: N PCs must not all be polled in the same second.
  local seen = {}
  for _, dni in ipairs({ "pc-control-9f3c-guid", "pc-control-a1b2-guid",
    "pc-control-c3d4-guid", "pc-control-manual-68c0-1" }) do
    local offset = poll.offset(dni, 30)
    h.assert_nil(seen[offset], "two devices share the slot " .. tostring(offset))
    seen[offset] = dni
  end
end

function T.test_offset_is_zero_without_anything_to_hash()
  h.assert_equal(poll.offset(nil, 30), 0)
  h.assert_equal(poll.offset("", 30), 0)
  h.assert_equal(poll.offset("pc-control-9f3c-guid", 1), 0, "nothing to spread over")
end

function T.test_start_delays_the_schedule_by_the_offset()
  local driver = fake_driver()
  local device = device_with("pc-control-9f3c-guid", { pollInterval = "30" })
  poll.start(driver, device)

  local offset = poll.offset(device.device_network_id, 30)
  h.assert_true(offset > 0, "this DNI is meant to be staggered")
  local starter = timer_named(driver, "pc-poll-start")
  h.assert_equal(starter.delay, offset)
  h.assert_nil(timer_named(driver, "pc-poll"), "the schedule starts after the offset")

  -- The tiles are still primed straight away.
  h.assert_equal(timer_named(driver, "pc-poll-initial").delay, 1)

  fire(driver, "pc-poll-start")
  h.assert_equal(timer_named(driver, "pc-poll").interval, 30)
  h.assert_nil(device:get_field(poll.START_TIMER_FIELD))
end

function T.test_stop_cancels_both_timers()
  local driver = fake_driver()
  local device = device_with("pc-control-9f3c-guid", { pollInterval = "30" })
  poll.start(driver, device)
  local starter = timer_named(driver, "pc-poll-start")
  fire(driver, "pc-poll-start")
  local schedule = timer_named(driver, "pc-poll")

  poll.stop(driver, device)
  h.assert_true(schedule.cancelled)
  h.assert_nil(device:get_field(poll.TIMER_FIELD))
  h.assert_nil(device:get_field(poll.START_TIMER_FIELD))

  -- A stop before the offset elapsed cancels the pending start too.
  poll.start(driver, device)
  local pending = timer_named(driver, "pc-poll-start")
  h.assert_true(pending ~= starter, "a restart arms a new starter")
  poll.stop(driver, device)
  h.assert_true(pending.cancelled)
end

function T.test_restarting_replaces_the_schedule()
  local driver = fake_driver()
  local device = device_with("pc-control-9f3c-guid", { pollInterval = "30" })
  poll.start(driver, device)
  fire(driver, "pc-poll-start")
  local first = timer_named(driver, "pc-poll")

  device.preferences.pollInterval = "60"
  poll.start(driver, device)
  h.assert_true(first.cancelled, "the old schedule is cancelled")
  fire(driver, "pc-poll-start")
  h.assert_equal(timer_named(driver, "pc-poll").interval, 60)
end

function T.test_identity_is_remembered_from_a_status_body()
  -- §6.5: this is how a manually added device learns its machine_id.
  local device = h.fake_device({})
  poll.remember_identity(device, { machine_id = "9f3c-guid", hostname = "DESKTOP-ABC" })
  h.assert_equal(device:get_field(discovery.MACHINE_FIELD), "9f3c-guid")
  h.assert_equal(device:get_field(discovery.HOSTNAME_FIELD), "DESKTOP-ABC")

  -- A body without them leaves what is there.
  poll.remember_identity(device, {})
  h.assert_equal(device:get_field(discovery.MACHINE_FIELD), "9f3c-guid")
end

function T.test_a_failed_poll_rewrites_the_status_summary()
  -- #78: the summary is the only pcInfo row left on screen, so the failure
  -- path has to repaint it too — otherwise it would still read "Connected".
  local caps = require "caps"
  local state = require "state"
  local device = h.fake_device({ language = "ko" })
  poll.set_state(device, state.new(state.OFF))

  poll.emit_connection(device, "unauthorized", "시크릿이 일치하지 않습니다")
  local emitted = h.emitted(device)
  h.assert_equal(h.event_value(emitted, caps.STATUS, "connection"), "unauthorized")
  h.assert_equal(h.event_value(emitted, caps.STATUS, "summary"), "연결 안 됨 · 시크릿 불일치")
  -- #85/#86: the version row goes out on a failed poll too - the driver half
  -- still answers "did my update land?" - and since #86 it is a capability of
  -- its own, while pcInfo keeps the attribute it defines.
  -- #87: the service half is "v?" until a PC answers.
  local unreached = "v? · 드라이버 " .. require("driver_version"):match("^(%d+%.%d+)")
  h.assert_equal(h.event_value(emitted, caps.VERSION, "versions"), unreached)
  h.assert_equal(h.event_value(emitted, caps.STATUS, "versions"), unreached)
end

function T.test_emit_passes_the_state_change_option_through()
  -- #86: an event whose value equals the current one is dropped by the
  -- platform, and the app - waiting for exactly that attribute - spins until it
  -- errors. A record marked `force` has to reach `emit_event` as
  -- `{ state_change = true }` (platform notes "상세 화면(detailView) 위젯").
  local caps = require "caps"
  local device = h.fake_device()
  poll.emit(device, {
    { cap = caps.COMMAND, attr = "lastAction", value = "none", force = true },
    { cap = caps.COMMAND, attr = "lastCommand", value = "없음 (None)" },
  })
  local emitted = h.emitted(device)
  h.assert_true(h.event_forced(emitted, caps.COMMAND, "lastAction"),
    "a forced record must carry state_change")
  h.assert_false(h.event_forced(emitted, caps.COMMAND, "lastCommand"),
    "an ordinary poll update must stay unforced")
end

function T.test_force_rows_marks_only_the_rows_a_command_answers()
  local caps = require "caps"
  local events = {
    { cap = caps.SCHEDULE, attr = "status", value = "idle" },
    { cap = caps.SCHEDULE, attr = "summary", value = "예약 없음" },
    { cap = caps.STATUS, attr = "summary", value = "연결됨" },
  }
  poll.force_rows(events, poll.SCHEDULE_ROWS)
  h.assert_true(events[1].force, "the schedule list's own row is answered")
  h.assert_true(events[2].force)
  h.assert_nil(events[3].force, "the status summary is an ordinary update")
end

return T
