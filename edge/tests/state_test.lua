local h = require "helpers"
local caps = require "caps"
local state = require "state"
local i18n = require "i18n"

local T = {}

-- poll.now(): the hub's local clock time of the last good poll (§5.1).
local NOW = "14:05:00"

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
-- the whole §4.2 -> §5.1 mapping, field by field
--------------------------------------------------------------------------------

-- Every event the §4.2 example produces, in the order apply_status emits them.
-- A golden list rather than a handful of spot checks: it is the one place that
-- says what the app actually shows, so an accidental extra or missing event
-- fails here instead of quietly changing a tile.
local function golden(lang)
  local en = lang ~= "ko"
  return {
    { cap = state.CAP_SWITCH, attr = "switch", value = "on" },
    { cap = caps.POWER_STATE, attr = "powerState", value = "on" },
    { cap = caps.COMMAND, attr = "lastCommand",
      value = en and "Shut down · SmartThings · 23:05" or "종료 · SmartThings · 23:05" },
    { cap = caps.SCHEDULE, attr = "active", value = true },
    -- #83: the same fact as an enum, because a list cannot read a boolean.
    { cap = caps.SCHEDULE, attr = "status", value = "scheduled" },
    { cap = caps.SCHEDULE, attr = "command", value = en and "Shut down" or "종료" },
    { cap = caps.SCHEDULE, attr = "remainingSeconds", value = 240 },
    { cap = caps.SCHEDULE, attr = "executeAt", value = "23:10" },
    { cap = caps.SCHEDULE, attr = "origin", value = "SmartThings" },
    -- #78: the one schedule row the detail view still shows.
    { cap = caps.SCHEDULE, attr = "summary",
      value = en and "Shut down · 4 min left · SmartThings" or "종료 · 4분 남음 · SmartThings" },
    { cap = caps.STATUS, attr = "connection", value = "ok" },
    { cap = caps.STATUS, attr = "serviceVersion", value = "v1.1.0" },
    { cap = caps.STATUS, attr = "updateAvailable", value = false },
    { cap = caps.STATUS, attr = "wolReady", value = true },
    { cap = caps.STATUS, attr = "lastSeen", value = NOW },
    { cap = caps.STATUS, attr = "message", value = "" },
    { cap = caps.STATUS, attr = "summary",
      value = en and "Connected · v1.1.0" or "연결됨 · v1.1.0" },
    -- #78: emitted either way, so the session row can be hidden again.
    { cap = caps.SESSION, attr = "exposed", value = true },
    { cap = caps.SESSION, attr = "locked", value = true },
    { cap = caps.SESSION, attr = "idleMinutes", value = 20 },
    { cap = caps.SESSION, attr = "user", value = "kim" },
    { cap = caps.SESSION, attr = "summary",
      value = en and "Locked · idle 20 min · kim" or "잠김 · 유휴 20분 · kim" },
  }
end

local function exposed_status()
  local status = sample_status()
  status.session = { exposed = true, locked = true, idle_seconds = 1200, user = "kim" }
  return status
end

function T.test_apply_status_maps_every_field_in_english()
  h.assert_deep_equal(events_for(exposed_status(), state.ON, "en"), golden("en"))
end

function T.test_apply_status_maps_every_field_in_korean()
  -- §6.5: only the string attributes follow `language`; enums and numbers do not.
  h.assert_deep_equal(events_for(exposed_status(), state.ON, "ko"), golden("ko"))
end

function T.test_attributes_used_covers_every_emitted_event()
  -- capabilities_test.lua checks this set against the JSON definitions, so it
  -- is only useful if it really is everything apply_status can emit.
  local used = state.attributes_used()
  for _, e in ipairs(events_for(exposed_status())) do
    h.assert_true((used[e.cap] or {})[e.attr] == true,
      "state.attributes_used() is missing " .. tostring(e.cap) .. "." .. tostring(e.attr))
  end
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
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "status"), state.SCHEDULED)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "command"), "Shut down")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "remainingSeconds"), 240)
  -- §5.1: the local clock time, not the RFC3339 string the service sends.
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "executeAt"), "23:10")
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
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "status"), state.IDLE)
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
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "status"), state.IDLE)
end

function T.test_the_schedule_status_enum_tracks_active()
  -- #83: `status` is what the detail-view list reads, so it may never drift
  -- from `active` - the row would then say "No schedule" over a live one.
  for _, active in ipairs({ true, false }) do
    local status = sample_status()
    status.schedule = { active = active, command = "shutdown", remaining_seconds = 240 }
    local events = events_for(status)
    h.assert_equal(h.event_value(events, caps.SCHEDULE, "active"), active)
    h.assert_equal(h.event_value(events, caps.SCHEDULE, "status"),
      active and state.SCHEDULED or state.IDLE)
  end
end

function T.test_apply_status_emits_session_only_when_exposed()
  local status = sample_status()
  local events = events_for(status)
  -- #78: `exposed` is always emitted so automations can key off it, and the
  -- summary row (always shown; the phone ignores visibleCondition) says the
  -- feature is off instead of reading "-". The values themselves stay untouched.
  h.assert_equal(h.event_value(events, caps.SESSION, "exposed"), false)
  h.assert_equal(h.event_value(events, caps.SESSION, "summary"), i18n.t("en", "session_hidden"))
  for _, attr in ipairs({ "locked", "idleMinutes", "user" }) do
    h.assert_nil(h.event_value(events, caps.SESSION, attr),
      "session is opt-in and must not invent " .. attr)
  end

  status.session = { exposed = true, locked = true, idle_seconds = 1200, user = "kim" }
  events = events_for(status)
  h.assert_equal(h.event_value(events, caps.SESSION, "exposed"), true)
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

function T.test_apply_status_reports_an_available_update()
  local status = sample_status()
  status.update = { available = true, latest = "v1.2.0" }
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.STATUS, "updateAvailable"), true)
  h.assert_equal(h.event_value(events, caps.STATUS, "message"), "Service update v1.2.0 available")
  local ko = events_for(status, state.ON, "ko")
  h.assert_equal(h.event_value(ko, caps.STATUS, "message"), "서비스 업데이트 v1.2.0 사용 가능")
end

function T.test_an_update_without_a_version_still_says_so()
  local status = sample_status()
  status.update = { available = true }
  h.assert_equal(h.event_value(events_for(status), caps.STATUS, "message"),
    "A service update is available")
end

--------------------------------------------------------------------------------
-- summaries (#78): the one-line rows that replaced the raw attribute rows
--------------------------------------------------------------------------------

function T.test_status_summary_reads_like_the_issue()
  -- #82: no power word — the pcPower row sits directly above this one.
  h.assert_equal(state.status_summary("ok", "v1.1.0", "ko"), "연결됨 · v1.1.0")
  h.assert_equal(state.status_summary("ok", "v1.1.0", "en"), "Connected · v1.1.0")
  h.assert_equal(state.status_summary("unauthorized", nil, "ko"), "연결 안 됨 · 시크릿 불일치")
  h.assert_equal(state.status_summary("unauthorized", nil, "en"),
    "Not connected · Secret mismatch")
end

function T.test_status_summary_never_repeats_the_power_state()
  -- #82: the power word moved out of this line for good; a summary that
  -- carried it again would duplicate the row above it.
  for _, power in ipairs({ state.ON, state.SLEEPING, state.HIBERNATED, state.OFF,
                           state.WAKING, state.SHUTTING_DOWN, state.UNKNOWN }) do
    local summary = state.status_summary("ok", "", "ko")
    h.assert_contains(summary, "연결됨", power)
    h.assert_equal(summary:find(i18n.power("ko", power), 1, true), nil,
      power .. " is the pcPower row's job")
  end
  for _, connection in ipairs({ "unauthorized", "unreachable", "incompatible" }) do
    local summary = state.status_summary(connection, nil, "ko")
    h.assert_contains(summary, "연결 안 됨", connection)
    h.assert_equal(summary:find(connection, 1, true), nil,
      connection .. " has no short Korean label")
  end
end

function T.test_status_summary_omits_an_unknown_version()
  h.assert_equal(state.status_summary(nil, nil, "en"), "Connected")
  h.assert_equal(state.status_summary("ok", "", "en"), "Connected")
end

--------------------------------------------------------------------------------
-- the short notices the summary carries (#82)
--------------------------------------------------------------------------------

function T.test_the_summary_notice_is_shorter_than_the_message()
  -- Both ladders pick the same notice; the summary takes the short wording
  -- because it shares the screen with three other summary rows.
  local status = sample_status()
  status.secret_set = false
  h.assert_equal(state.status_notice(status, { lang = "ko" }), "시크릿 미설정 · 설정 권장")
  h.assert_equal(state.status_notice(status, { lang = "en" }), "No secret · set one")
  h.assert_equal(state.status_message(status, { lang = "en" }),
    "No secret is set · setting one is recommended")

  status = sample_status()
  status.wol = { ready = false, adapters = {} }
  h.assert_equal(state.status_notice(status, { lang = "ko" }), "어댑터 WoL 꺼짐")
  h.assert_equal(state.status_notice(status, { lang = "en" }), "Adapter WoL off")

  status = sample_status()
  status.update = { available = true, latest = "v1.2.0" }
  h.assert_equal(state.status_notice(status, { lang = "ko" }), "업데이트 v1.2.0 사용 가능")
  h.assert_equal(state.status_notice(status, { lang = "en" }), "Update v1.2.0 available")
  status.update = { available = true }
  h.assert_equal(state.status_notice(status, { lang = "en" }), "Update available")
end

function T.test_the_summary_carries_the_short_notice_and_message_the_long_one()
  local status = sample_status()
  status.secret_set = false
  local events = events_for(status, state.ON, "ko")
  h.assert_equal(h.event_value(events, caps.STATUS, "summary"),
    "연결됨 · v1.1.0 · 시크릿 미설정 · 설정 권장")
  h.assert_equal(h.event_value(events, caps.STATUS, "message"),
    "시크릿이 설정되지 않았습니다 · 설정을 권장합니다")
end

function T.test_a_quiet_status_has_no_notice_at_all()
  h.assert_equal(state.status_notice(sample_status(), { lang = "en" }), "")
  h.assert_equal(h.event_value(events_for(sample_status()), caps.STATUS, "summary"),
    "Connected · v1.1.0")
end

--------------------------------------------------------------------------------
-- pcExec.lastAction (#82, #84)
--------------------------------------------------------------------------------

function T.test_the_action_values_are_the_service_command_names()
  -- #84: the enum is the `execute` argument enum, so whatever the row holds is
  -- something `execute` accepts - a dismissed picker sends it straight back.
  -- The old `screenOff`/`screenOn` spellings are gone with it.
  for _, value in ipairs({ "none", "wake", "shutdown", "forceshutdown", "restart",
      "hibernate", "suspend", "lock", "turnscreenoff", "turnscreenon" }) do
    h.assert_true(state.is_action(value), value .. " is not a lastAction value")
  end
  h.assert_equal(#state.ACTIONS, 10)
  h.assert_equal(state.ACTION_NONE, "none")
  h.assert_nil(state.action_for, "#84 removed the service-command mapping")
end

function T.test_is_action_rejects_anything_outside_the_enum()
  -- The hub rejects an event whose value is not in the enum.
  for _, bogus in ipairs({ "ping", "", "screenOff", "screenOn" }) do
    h.assert_false(state.is_action(bogus), tostring(bogus) .. " must not pass")
  end
  h.assert_false(state.is_action(nil))
  h.assert_false(state.is_action(42))
end

--------------------------------------------------------------------------------
-- pcCountdown.planCommand (#84, moved in #85)
--------------------------------------------------------------------------------

function T.test_only_the_schedulable_commands_are_plan_commands()
  for _, value in ipairs({ "shutdown", "restart", "suspend", "hibernate" }) do
    h.assert_true(state.is_plan_command(value), value .. " is schedulable (§4.3)")
  end
  for _, value in ipairs({ "lock", "turnscreenoff", "wake", "forceshutdown", "none", "" }) do
    h.assert_false(state.is_plan_command(value), value .. " must not be schedulable")
  end
  h.assert_false(state.is_plan_command(nil))
end

function T.test_plan_command_for_takes_the_first_schedulable_argument()
  -- The caller's preference order: the automation's argument, then the command
  -- the user picked on the detail view, then the `offAction` preference.
  h.assert_equal(state.plan_command_for("restart", "suspend", "hibernate"), "restart")
  h.assert_equal(state.plan_command_for(nil, "suspend", "hibernate"), "suspend")
  -- `lock` is an offAction but not schedulable, so it is skipped.
  h.assert_equal(state.plan_command_for(nil, nil, "lock"), state.PLAN_DEFAULT)
  h.assert_equal(state.plan_command_for("lock", "hibernate"), "hibernate")
  h.assert_equal(state.plan_command_for(), "shutdown")
  h.assert_equal(state.plan_command_for(nil), "shutdown")
end

function T.test_schedule_summary_says_no_schedule_when_idle()
  h.assert_equal(state.schedule_summary({ active = false }, "ko"), "예약 없음")
  h.assert_equal(state.schedule_summary(nil, "en"), "No schedule")
  local events = events_for((function()
    local status = sample_status()
    status.schedule = { active = false }
    return status
  end)())
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "summary"), "No schedule")
end

function T.test_schedule_summary_rounds_the_countdown_up()
  local function summary(seconds)
    return state.schedule_summary({
      active = true, command = "shutdown", origin = "smartthings",
      remaining_seconds = seconds,
    }, "ko")
  end
  h.assert_equal(summary(240), "종료 · 4분 남음 · SmartThings")
  h.assert_equal(summary(241), "종료 · 5분 남음 · SmartThings", "a part minute still counts")
  h.assert_equal(summary(59), "종료 · 곧 실행 · SmartThings")
  h.assert_contains(state.schedule_summary({
    active = true, command = "restart", origin = "ui", remaining_seconds = 600,
  }, "en"), "Restart · 10 min left · App")
end

function T.test_session_summary_follows_the_language()
  h.assert_equal(state.session_summary({ locked = true, idle_seconds = 1200, user = "kim" }, "ko"),
    "잠김 · 유휴 20분 · kim")
  h.assert_equal(state.session_summary({ locked = false, idle_seconds = 30 }, "ko"),
    "사용 중 · 유휴 0분")
  h.assert_equal(state.session_summary({ locked = false, idle_seconds = 30 }, "en"),
    "In use · idle 0 min")
end

--------------------------------------------------------------------------------
-- message priority (§5.1, state.MESSAGE_ORDER)
--------------------------------------------------------------------------------

function T.test_message_order_is_the_documented_one()
  h.assert_deep_equal(state.MESSAGE_ORDER, {
    "error", "incompatible", "wol_not_ready", "update_available", "no_secret", "note",
  })
end

function T.test_message_priority_picks_one_notice()
  -- All four at once: only the most important sentence is shown.
  local status = sample_status()
  status.wol = { ready = false }
  status.update = { available = true, latest = "v1.2.0" }
  status.secret_set = false

  local message = h.event_value(events_for(status), caps.STATUS, "message")
  h.assert_contains(message, "Wake-on-LAN")
  h.assert_equal(message:find("update", 1, true), nil, "the update notice must not be appended")

  -- WoL fixed: the update notice is next.
  status.wol = { ready = true }
  h.assert_contains(h.event_value(events_for(status), caps.STATUS, "message"), "v1.2.0")

  -- Nothing left but the missing secret.
  status.update = { available = false }
  h.assert_contains(h.event_value(events_for(status), caps.STATUS, "message"), "No secret")

  -- And a healthy service says nothing at all.
  status.secret_set = true
  h.assert_equal(h.event_value(events_for(status), caps.STATUS, "message"), "")
end

function T.test_an_error_outranks_every_status_notice()
  -- poll.lua maps a failed request to a message and passes it in; nothing a
  -- successful body could say is more urgent than "we could not talk to it".
  local status = sample_status()
  status.wol = { ready = false }
  status.secret_set = false
  local events = state.apply_status(state.new(state.OFF), status,
    { now = NOW, lang = "en", error = "Cannot reach the PC" })
  h.assert_equal(h.event_value(events, caps.STATUS, "message"), "Cannot reach the PC")
end

function T.test_a_note_shows_only_when_nothing_is_wrong()
  local status = sample_status()
  local events = state.apply_status(state.new(state.ON), status,
    { now = NOW, lang = "en", note = "Schedule cancelled" })
  h.assert_equal(h.event_value(events, caps.STATUS, "message"), "Schedule cancelled")

  -- A warning outranks a confirmation.
  status.wol = { ready = false }
  events = state.apply_status(state.new(state.ON), status,
    { now = NOW, lang = "en", note = "Schedule cancelled" })
  h.assert_contains(h.event_value(events, caps.STATUS, "message"), "Wake-on-LAN")
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
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "status"), state.IDLE)
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
