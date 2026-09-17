local h = require "helpers"
local caps = require "caps"
local state = require "state"

local T = {}

local NOW = "2026-09-17T14:05:00Z"

-- The example body from design doc §4.2.
local function sample_status()
  return {
    protocol = 1,
    service_version = "v1.1.0",
    machine_id = "9f3c-machine-guid",
    hostname = "DESKTOP-ABC",
    power = "on",
    uptime_seconds = 12345,
    last_shutdown_clean = true,
    secret_set = true,
    grace = { enabled = true, seconds = 300 },
    schedule = {
      active = true,
      command = "shutdown",
      origin = "smartthings",
      remaining_seconds = 240,
      execute_at = "2026-09-17T23:10:00+09:00",
    },
    last_command = {
      command = "shutdown",
      origin = "smartthings",
      at = "2026-09-17T23:05:00+09:00",
    },
    update = { available = false, latest = "v1.1.0" },
    wol = {
      ready = true,
      adapters = {
        { name = "Ethernet", mac = "AA:BB:CC:DD:EE:FF", wol_enabled = true, wol_capable = true },
      },
    },
    display = "on",
    session = { exposed = false },
  }
end

local function events_for(status, power_state, lang)
  local s = state.new(power_state or state.ON)
  return state.apply_status(s, status, { now = NOW, lang = lang or "en" })
end

--------------------------------------------------------------------------------
-- apply_status
--------------------------------------------------------------------------------

function T.test_apply_status_maps_core_attributes()
  local events = events_for(sample_status())
  h.assert_equal(h.event_value(events, state.CAP_SWITCH, "switch"), "on")
  h.assert_equal(h.event_value(events, caps.POWER_STATE, "powerState"), "on")
  h.assert_equal(h.event_value(events, caps.STATUS, "connection"), "ok")
  h.assert_equal(h.event_value(events, caps.STATUS, "serviceVersion"), "v1.1.0")
  h.assert_equal(h.event_value(events, caps.STATUS, "updateAvailable"), false)
  h.assert_equal(h.event_value(events, caps.STATUS, "wolReady"), true)
  h.assert_equal(h.event_value(events, caps.STATUS, "lastSeen"), NOW)
  h.assert_equal(h.event_value(events, caps.STATUS, "message"), "")
end

function T.test_apply_status_maps_schedule()
  local events = events_for(sample_status())
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "active"), true)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "command"), "Shut down")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "remainingSeconds"), 240)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "executeAt"), "2026-09-17T23:10:00+09:00")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "origin"), "SmartThings")
end

function T.test_apply_status_localises_schedule_strings()
  local events = events_for(sample_status(), state.ON, "ko")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "command"), "종료")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "origin"), "SmartThings")
  h.assert_equal(h.event_value(events, caps.COMMAND, "lastCommand"), "종료 · SmartThings · 23:05")
end

function T.test_apply_status_formats_last_command()
  local events = events_for(sample_status())
  h.assert_equal(h.event_value(events, caps.COMMAND, "lastCommand"), "Shut down · SmartThings · 23:05")
  -- No last command yet -> empty string rather than a stale value.
  local status = sample_status()
  status.last_command = nil
  h.assert_equal(h.event_value(events_for(status), caps.COMMAND, "lastCommand"), "")
end

function T.test_apply_status_clears_an_inactive_schedule()
  local status = sample_status()
  status.schedule = { active = false }
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "active"), false)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "command"), "")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "remainingSeconds"), 0)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "executeAt"), "")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "origin"), "")
end

function T.test_apply_status_handles_a_missing_schedule_block()
  local status = sample_status()
  status.schedule = nil
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "active"), false)
end

function T.test_apply_status_emits_session_only_when_exposed()
  local status = sample_status()
  h.assert_false(h.has_capability(events_for(status), caps.SESSION),
    "session is opt-in and must stay silent when exposed = false")

  status.session = { exposed = true, locked = true, idle_seconds = 1200, user = "kim" }
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.SESSION, "locked"), true)
  h.assert_equal(h.event_value(events, caps.SESSION, "idleMinutes"), 20)
  h.assert_equal(h.event_value(events, caps.SESSION, "user"), "kim")
end

function T.test_apply_status_session_without_user()
  local status = sample_status()
  status.session = { exposed = true, locked = false, idle_seconds = 30 }
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.SESSION, "locked"), false)
  h.assert_equal(h.event_value(events, caps.SESSION, "idleMinutes"), 0)
  h.assert_equal(h.event_value(events, caps.SESSION, "user"), "")
end

function T.test_apply_status_warns_about_a_missing_secret()
  -- §4.1: no secret is still a healthy connection, but it must be visible.
  local status = sample_status()
  status.secret_set = false
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.STATUS, "connection"), "ok")
  h.assert_contains(h.event_value(events, caps.STATUS, "message"), "No secret")
end

function T.test_apply_status_warns_when_wol_is_not_ready()
  local status = sample_status()
  status.wol = { ready = false, adapters = {} }
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.STATUS, "wolReady"), false)
  h.assert_contains(h.event_value(events, caps.STATUS, "message"), "Wake-on-LAN")
end

function T.test_apply_status_joins_multiple_warnings()
  local status = sample_status()
  status.secret_set = false
  status.wol = { ready = false }
  local message = h.event_value(events_for(status), caps.STATUS, "message")
  h.assert_contains(message, "No secret")
  h.assert_contains(message, "Wake-on-LAN")
  h.assert_contains(message, " · ")
end

function T.test_apply_status_reports_an_available_update()
  local status = sample_status()
  status.update = { available = true, latest = "v1.2.0" }
  h.assert_equal(h.event_value(events_for(status), caps.STATUS, "updateAvailable"), true)
end

function T.test_apply_status_uses_the_state_power_not_the_body()
  -- §4.2: `power` in the body is always "on"; powerState comes from the machine.
  local events = events_for(sample_status(), state.SHUTTING_DOWN)
  h.assert_equal(h.event_value(events, caps.POWER_STATE, "powerState"), "shuttingDown")
  h.assert_equal(h.event_value(events, state.CAP_SWITCH, "switch"), "on")
end

function T.test_apply_status_survives_an_empty_body()
  local events = state.apply_status(state.new(state.ON), {}, { now = NOW })
  h.assert_equal(h.event_value(events, caps.STATUS, "serviceVersion"), "")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "active"), false)
end

function T.test_wol_mac_prefers_an_enabled_adapter()
  local status = {
    wol = {
      ready = true,
      adapters = {
        { name = "Wi-Fi", mac = "11:22:33:44:55:66", wol_enabled = false },
        { name = "Ethernet", mac = "AA:BB:CC:DD:EE:FF", wol_enabled = true },
      },
    },
  }
  h.assert_equal(state.wol_mac(status), "AA:BB:CC:DD:EE:FF")
  status.wol.adapters[2].wol_enabled = false
  h.assert_equal(state.wol_mac(status), "11:22:33:44:55:66", "falls back to the first mac")
  h.assert_nil(state.wol_mac({}))
end

--------------------------------------------------------------------------------
-- switch derivation (§6.2)
--------------------------------------------------------------------------------

function T.test_switch_is_derived_from_power_state()
  h.assert_equal(state.switch_for(state.ON), "on")
  h.assert_equal(state.switch_for(state.WAKING), "on")
  h.assert_equal(state.switch_for(state.SHUTTING_DOWN), "on")
  h.assert_equal(state.switch_for(state.OFF), "off")
  h.assert_equal(state.switch_for(state.SLEEPING), "off")
  h.assert_equal(state.switch_for(state.HIBERNATED), "off")
  h.assert_equal(state.switch_for(state.UNKNOWN), "off")
end

--------------------------------------------------------------------------------
-- transitions (§6.2)
--------------------------------------------------------------------------------

function T.test_transition_does_not_mutate_its_input()
  local before = state.new(state.ON)
  local after = state.transition(before, "stopping", "suspend")
  h.assert_equal(before.power_state, state.ON, "input must be untouched")
  h.assert_equal(after.power_state, state.SLEEPING)
end

function T.test_stopping_suspend_goes_to_sleeping()
  local s = state.transition(state.new(state.ON), "stopping", "suspend")
  h.assert_equal(s.power_state, state.SLEEPING)
  h.assert_equal(s.last_stopping_reason, "suspend")
end

function T.test_stopping_hibernate_goes_to_hibernated()
  local s = state.transition(state.new(state.ON), "stopping", "hibernate")
  h.assert_equal(s.power_state, state.HIBERNATED)
end

function T.test_stopping_shutdown_and_restart_go_to_shutting_down()
  h.assert_equal(state.transition(state.new(state.ON), "stopping", "shutdown").power_state,
    state.SHUTTING_DOWN)
  h.assert_equal(state.transition(state.new(state.ON), "stopping", "restart").power_state,
    state.SHUTTING_DOWN)
  -- An unknown reason still means the PC is going away.
  h.assert_equal(state.transition(state.new(state.ON), "stopping", "unknown").power_state,
    state.SHUTTING_DOWN)
  h.assert_equal(state.transition(state.new(state.ON), "stopping").power_state,
    state.SHUTTING_DOWN)
end

function T.test_stopping_accepts_a_table_event()
  local s = state.transition(state.new(state.ON), { type = "stopping", reason = "suspend" })
  h.assert_equal(s.power_state, state.SLEEPING)
end

function T.test_shutting_down_becomes_off_once_unreachable()
  local s = state.transition(state.new(state.ON), "stopping", "shutdown")
  s = state.transition(s, "unreachable")
  h.assert_equal(s.power_state, state.SHUTTING_DOWN, "one miss is not enough (§6.1)")
  s = state.transition(s, "unreachable")
  h.assert_equal(s.power_state, state.OFF)
end

function T.test_two_consecutive_unreachable_polls_turn_the_pc_off()
  local s = state.new(state.ON)
  s = state.transition(s, "unreachable")
  h.assert_equal(s.power_state, state.ON, "a single missed poll must not flip the tile")
  h.assert_equal(s.unreachable_count, 1)
  s = state.transition(s, "unreachable")
  h.assert_equal(s.power_state, state.OFF)
  h.assert_equal(s.unreachable_count, 2)
end

function T.test_sleeping_is_preserved_while_unreachable()
  -- §6.1: a PC that told us it was suspending stays "sleeping", not "off".
  local s = state.transition(state.new(state.ON), "stopping", "suspend")
  for _ = 1, 5 do
    s = state.transition(s, "unreachable")
  end
  h.assert_equal(s.power_state, state.SLEEPING)
  h.assert_equal(state.switch_for(s.power_state), "off")
end

function T.test_hibernated_is_preserved_while_unreachable()
  local s = state.transition(state.new(state.ON), "stopping", "hibernate")
  s = state.transition(s, "unreachable")
  s = state.transition(s, "unreachable")
  h.assert_equal(s.power_state, state.HIBERNATED)
end

function T.test_stopping_reason_survives_a_state_we_never_saw()
  -- Push said "suspend" while the poller still believed "unknown".
  local s = state.new(state.UNKNOWN)
  s.last_stopping_reason = "hibernate"
  s = state.transition(s, "unreachable")
  s = state.transition(s, "unreachable")
  h.assert_equal(s.power_state, state.HIBERNATED)
end

function T.test_status_ok_always_means_on()
  for _, from in ipairs({ state.OFF, state.SLEEPING, state.HIBERNATED, state.WAKING,
                          state.SHUTTING_DOWN, state.UNKNOWN }) do
    local s = state.transition(state.new(from), "status_ok")
    h.assert_equal(s.power_state, state.ON, "status_ok from " .. from)
  end
end

function T.test_status_ok_resets_the_failure_counters()
  local s = state.new(state.ON)
  s = state.transition(s, "unreachable")
  s = state.transition(s, "stopping", "suspend")
  s = state.transition(s, "status_ok")
  h.assert_equal(s.unreachable_count, 0)
  h.assert_nil(s.last_stopping_reason)
  h.assert_nil(s.wake_from)
end

function T.test_switch_on_starts_waking()
  for _, from in ipairs({ state.OFF, state.SLEEPING, state.HIBERNATED }) do
    local s = state.transition(state.new(from), "switch_on")
    h.assert_equal(s.power_state, state.WAKING, "switch_on from " .. from)
    h.assert_equal(s.wake_from, from)
    h.assert_equal(state.switch_for(s.power_state), "on")
  end
end

function T.test_switch_on_while_already_on_changes_nothing()
  local s = state.transition(state.new(state.ON), "switch_on")
  h.assert_equal(s.power_state, state.ON)
end

function T.test_waking_survives_unreachable_polls()
  local s = state.transition(state.new(state.OFF), "switch_on")
  s = state.transition(s, "unreachable")
  s = state.transition(s, "unreachable")
  h.assert_equal(s.power_state, state.WAKING, "a booting PC is expected to be quiet")
end

function T.test_waking_becomes_on_when_the_pc_answers()
  local s = state.transition(state.new(state.OFF), "switch_on")
  s = state.transition(s, "status_ok")
  h.assert_equal(s.power_state, state.ON)
end

function T.test_wake_timeout_restores_the_previous_state()
  local s = state.transition(state.new(state.SLEEPING), "switch_on")
  s = state.transition(s, "wake_timeout")
  h.assert_equal(s.power_state, state.SLEEPING)
  h.assert_nil(s.wake_from)

  s = state.transition(state.new(state.OFF), "switch_on")
  s = state.transition(s, "wake_timeout")
  h.assert_equal(s.power_state, state.OFF)
end

function T.test_wake_timeout_is_ignored_when_not_waking()
  local s = state.transition(state.new(state.ON), "wake_timeout")
  h.assert_equal(s.power_state, state.ON)
end

function T.test_schedule_cancelled_brings_the_switch_back_on()
  -- §6.2: cancelling the grace period on the PC must not leave a stale switch.
  local s = state.transition(state.new(state.ON), "stopping", "shutdown")
  h.assert_equal(s.power_state, state.SHUTTING_DOWN)
  s = state.transition(s, "schedule_cancelled")
  h.assert_equal(s.power_state, state.ON)
  h.assert_nil(s.last_stopping_reason)
end

function T.test_schedule_cancelled_leaves_other_states_alone()
  local s = state.transition(state.new(state.SLEEPING), "schedule_cancelled")
  h.assert_equal(s.power_state, state.SLEEPING)
end

function T.test_unknown_events_are_ignored()
  local s = state.transition(state.new(state.ON), "not_an_event")
  h.assert_equal(s.power_state, state.ON)
end

function T.test_transition_tolerates_a_nil_state()
  local s = state.transition(nil, "status_ok")
  h.assert_equal(s.power_state, state.ON)
end

return T
