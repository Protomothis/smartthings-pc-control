local h = require "helpers"
local caps = require "caps"
local state = require "state"
local i18n = require "i18n"

local T = {}

-- poll.now(): the hub's local clock time of the last good poll (§4).
local NOW = "14:05:00"

-- The example body from design doc §3.2.
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
-- the whole §3.2 -> §4 mapping, field by field
--------------------------------------------------------------------------------

-- Every event the §3.2 example produces, in the order apply_status emits them.
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
    -- #78: the one schedule row the detail view still shows. #87: without the
    -- origin, which the row right above it already carries.
    { cap = caps.SCHEDULE, attr = "summary",
      value = en and "Shut down · in 4 min" or "종료 · 4분 후" },
    { cap = caps.STATUS, attr = "connection", value = "ok" },
    { cap = caps.STATUS, attr = "serviceVersion", value = "v1.1.0" },
    { cap = caps.STATUS, attr = "updateAvailable", value = false },
    { cap = caps.STATUS, attr = "wolReady", value = true },
    { cap = caps.STATUS, attr = "lastSeen", value = NOW },
    -- #85: the bottom row of the bottom card, refreshed on every poll.
    -- #86: on its own capability (two state rows of one capability are drawn as
    -- two narrow, truncated columns), and still on pcInfo, which defines it.
    { cap = caps.VERSION, attr = "versions", value = state.versions("v1.1.0", lang) },
    { cap = caps.STATUS, attr = "versions", value = state.versions("v1.1.0", lang) },
    { cap = caps.STATUS, attr = "message", value = "" },
    -- #87: the connection alone. The version moved to the row below, and the
    -- advice notices are `message`'s job.
    { cap = caps.STATUS, attr = "summary",
      value = en and "Connected" or "연결됨" },
    -- #78: emitted either way, so the session row can be hidden again.
    { cap = caps.SESSION, attr = "exposed", value = true },
    { cap = caps.SESSION, attr = "locked", value = true },
    { cap = caps.SESSION, attr = "idleMinutes", value = 20 },
    { cap = caps.SESSION, attr = "user", value = "kim" },
    { cap = caps.SESSION, attr = "summary",
      value = en and "Locked · 20 min · kim" or "잠김 · 20분 · kim" },
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
  -- §6.8: only the string attributes follow `language`; enums and numbers do not.
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
  -- §4: the local clock time, not the RFC3339 string the service sends.
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
  -- #86: no last command yet -> a sentence, never an empty string. An empty
  -- `state` row is drawn as "-" (platform notes "상세 화면(detailView) 위젯"), which reads as a fault.
  local status = sample_status()
  status.last_command = nil
  h.assert_equal(h.event_value(events_for(status), caps.COMMAND, "lastCommand"), "None")
  h.assert_equal(h.event_value(events_for(status, state.ON, "ko"), caps.COMMAND, "lastCommand"),
    "없음 (None)")
end

function T.test_apply_status_clears_an_inactive_schedule()
  local status = sample_status()
  status.schedule = { active = false }
  local events = events_for(status)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "active"), false)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "status"), state.IDLE)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "command"), "none")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "remainingSeconds"), 0)
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "executeAt"), "none")
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "origin"), "none")
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
  -- #87: one word, the same one the power row uses for a PC that is not there.
  h.assert_equal(h.event_value(events, caps.SESSION, "summary"), "Off")
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
  -- §3.1: no secret is still a healthy connection, but it must be visible.
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

function T.test_the_versions_row_names_the_two_versions()
  -- #87: the bottom row of the bottom card, and nothing but the two numbers a
  -- user can compare against a release page. The driver half is shortened to
  -- major.minor and the service half writes the "v" itself, so the one the
  -- release tag carries must not be doubled.
  local version = require "driver_version"
  local major_minor = version:match("^(%d+%.%d+)")
  h.assert_true(major_minor ~= nil, "driver_version is not a dotted version")

  h.assert_equal(state.versions("v1.1.0", "ko"), "v1.1.0 · 드라이버 " .. major_minor)
  h.assert_equal(state.versions("v1.1.0", "en"), "v1.1.0 · Driver " .. major_minor)
  -- A service that does not prefix its version gets the same row.
  h.assert_equal(state.versions("1.1.0", "ko"), "v1.1.0 · 드라이버 " .. major_minor)
  -- #87: the profile name is gone; it was an implementation detail.
  h.assert_equal(state.versions("v1.1.0", "ko"):find(require("profiles").current(), 1, true), nil,
    "the screen name left the version row")
end

function T.test_the_versions_row_appends_an_update_only_when_there_is_one()
  local base = state.versions("v1.1.0", "ko")
  h.assert_equal(state.versions("v1.1.0", "ko", { available = false, latest = "v1.2.0" }), base)
  h.assert_equal(state.versions("v1.1.0", "ko", { available = true, latest = "v1.2.0" }),
    base .. " · 업데이트 v1.2.0")
  h.assert_equal(state.versions("v1.1.0", "en", { available = true, latest = "v1.2.0" }),
    state.versions("v1.1.0", "en") .. " · Update v1.2.0")
  -- `available` without a usable `latest` still has to say something.
  h.assert_equal(state.versions("v1.1.0", "en", { available = true }),
    state.versions("v1.1.0", "en") .. " · Update available")
end

function T.test_the_versions_row_says_question_mark_before_the_first_answer()
  -- A PC we have not reached has no service version, and the row still has to
  -- read as something: an attribute that was never emitted shows "-" (platform notes "상세 화면(detailView) 위젯").
  local major_minor = require("driver_version"):match("^(%d+%.%d+)")
  for _, missing in ipairs({ "", "\0nil" }) do
    local value = state.versions(missing ~= "\0nil" and missing or nil, "ko")
    h.assert_equal(value, "v? · 드라이버 " .. major_minor)
  end
  h.assert_equal(state.versions(nil, "en"), "v? · Driver " .. major_minor)
end

function T.test_a_status_body_refreshes_the_versions_row()
  local events = state.apply_status(state.new(state.ON), sample_status(),
    { now = NOW, lang = "ko" })
  -- #86: the row on screen is `pcVersion.versions`; `pcInfo.versions` keeps
  -- being emitted because pcInfo still defines it (platform notes "허브의 정의 캐시") and an attribute that
  -- is never emitted makes the app report incomplete state.
  h.assert_equal(h.event_value(events, caps.VERSION, "versions"),
    state.versions("v1.1.0", "ko"))
  h.assert_equal(h.event_value(events, caps.STATUS, "versions"),
    state.versions("v1.1.0", "ko"))
end

function T.test_the_initial_rows_cover_every_row_no_status_body_carries()
  -- #85: `added` paints these so no row of the command, schedule or info card
  -- reads "-" before the first successful poll.
  local events = state.initial_rows("ko")
  for _, case in ipairs({
    { caps.COMMAND, "lastCommand" },
    { caps.SCHEDULE, "active" }, { caps.SCHEDULE, "status" },
    { caps.SCHEDULE, "command" }, { caps.SCHEDULE, "remainingSeconds" },
    { caps.SCHEDULE, "executeAt" }, { caps.SCHEDULE, "origin" },
    { caps.SCHEDULE, "summary" }, { caps.STATUS, "versions" },
    -- #86: the new capability's row, which starts out unset on every device.
    { caps.VERSION, "versions" },
  }) do
    h.assert_true(h.event_value(events, case[1], case[2]) ~= nil,
      case[1] .. "." .. case[2] .. " is not painted at `added`")
  end
  h.assert_equal(h.event_value(events, caps.STATUS, "versions"), state.versions(nil, "ko"))
  h.assert_equal(h.event_value(events, caps.VERSION, "versions"), state.versions(nil, "ko"))
  -- #86: and no painted row is an empty string - that is drawn as "-" too.
  h.assert_equal(h.event_value(events, caps.COMMAND, "lastCommand"), "없음 (None)")
end

function T.test_the_initial_rows_keep_a_remembered_service_version()
  -- #92: `initial_rows` is also what a repaint paints, and a device that has
  -- been answering for months must not have its version reset to "v?" by a
  -- profile change or by the PC being off at the time.
  local events = state.initial_rows("ko", "v1.1.0")
  h.assert_equal(h.event_value(events, caps.VERSION, "versions"), state.versions("v1.1.0", "ko"))
  h.assert_equal(h.event_value(events, caps.STATUS, "versions"), state.versions("v1.1.0", "ko"))
  -- Nothing remembered is still "v?": the row has to say something.
  h.assert_equal(h.event_value(state.initial_rows("ko"), caps.VERSION, "versions"),
    state.versions(nil, "ko"))
end

function T.test_status_summary_reads_like_the_issue()
  -- #82: no power word — the pcPower row sits directly above this one.
  -- #87: no version either — the pcVersion row is nothing but versions.
  h.assert_equal(state.status_summary("ok", "ko"), "연결됨")
  h.assert_equal(state.status_summary("ok", "en"), "Connected")
  h.assert_equal(state.status_summary(nil, "en"), "Connected")
  h.assert_equal(state.status_summary("unauthorized", "ko"), "연결 안 됨 · 시크릿 불일치")
  h.assert_equal(state.status_summary("unauthorized", "en"),
    "Not connected · Secret mismatch")
  h.assert_equal(state.status_summary("unreachable", "ko"), "연결 안 됨 · 응답 없음")
  h.assert_equal(state.status_summary("incompatible", "en"), "Not connected · Version mismatch")
end

function T.test_status_summary_warns_when_wol_is_off()
  -- #87: the one notice the row still carries. A PC that answers but cannot be
  -- woken makes `switch on` do nothing, so the row says so next to "Connected".
  h.assert_equal(state.status_summary("ok", "ko", true), "연결됨 · WoL 꺼짐")
  h.assert_equal(state.status_summary("ok", "en", true), "Connected · WoL off")
  -- A PC we cannot reach is not told off for its adapter as well.
  h.assert_equal(state.status_summary("unreachable", "en", true), "Not connected · No response")
end

function T.test_status_summary_never_repeats_the_power_state()
  -- #82: the power word moved out of this line for good; a summary that
  -- carried it again would duplicate the row above it.
  for _, power in ipairs({ state.ON, state.SLEEPING, state.HIBERNATED, state.OFF,
                           state.WAKING, state.SHUTTING_DOWN, state.UNKNOWN }) do
    local summary = state.status_summary("ok", "ko")
    h.assert_contains(summary, "연결됨", power)
    h.assert_equal(summary:find(i18n.power("ko", power), 1, true), nil,
      power .. " is the pcPower row's job")
  end
  for _, connection in ipairs({ "unauthorized", "unreachable", "incompatible" }) do
    local summary = state.status_summary(connection, "ko")
    h.assert_contains(summary, "연결 안 됨", connection)
    h.assert_equal(summary:find(connection, 1, true), nil,
      connection .. " has no short Korean label")
  end
end

--------------------------------------------------------------------------------
-- what the summary row keeps, and what #87 moved to `message`
--------------------------------------------------------------------------------

function T.test_the_advice_notices_are_message_only()
  -- #87: "set a secret" and "an update is out" are things to read, not things
  -- to do right now, so they left the row that is glanced at. `message` still
  -- carries the whole ladder.
  local status = sample_status()
  status.secret_set = false
  local events = events_for(status, state.ON, "ko")
  h.assert_equal(h.event_value(events, caps.STATUS, "summary"), "연결됨")
  h.assert_equal(h.event_value(events, caps.STATUS, "message"),
    "시크릿이 설정되지 않았습니다 · 설정을 권장합니다")

  status = sample_status()
  status.update = { available = true, latest = "v1.2.0" }
  events = events_for(status, state.ON, "en")
  h.assert_equal(h.event_value(events, caps.STATUS, "summary"), "Connected")
  h.assert_equal(h.event_value(events, caps.STATUS, "message"),
    "Service update v1.2.0 available")
  -- The update is on the version row instead, where a version belongs.
  h.assert_equal(h.event_value(events, caps.VERSION, "versions"),
    "v1.1.0 · Driver " .. require("driver_version"):match("^(%d+%.%d+)") .. " · Update v1.2.0")

  h.assert_nil(state.status_notice, "#87 removed the short-notice ladder")
end

function T.test_the_summary_warns_about_a_wol_that_is_off()
  local status = sample_status()
  status.wol = { ready = false, adapters = {} }
  local events = events_for(status, state.ON, "ko")
  h.assert_equal(h.event_value(events, caps.STATUS, "summary"), "연결됨 · WoL 꺼짐")
  -- The long sentence, with what to do about it, stays in `message`.
  h.assert_contains(h.event_value(events, caps.STATUS, "message"), "네트워크 탭")
end

function T.test_a_quiet_status_has_no_notice_at_all()
  h.assert_equal(h.event_value(events_for(sample_status()), caps.STATUS, "summary"),
    "Connected")
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
-- pcDelay.planCommand (#84, moved in #85)
--------------------------------------------------------------------------------

function T.test_only_the_schedulable_commands_are_plan_commands()
  for _, value in ipairs({ "shutdown", "restart", "suspend", "hibernate" }) do
    h.assert_true(state.is_plan_command(value), value .. " is schedulable (§3.3)")
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

function T.test_schedule_summary_says_none_when_idle()
  -- #87: "없음", not "예약 없음" - the row already carries the "예약" label.
  h.assert_equal(state.schedule_summary({ active = false }, "ko"), "없음")
  h.assert_equal(state.schedule_summary(nil, "en"), "None")
  local events = events_for((function()
    local status = sample_status()
    status.schedule = { active = false }
    return status
  end)())
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "summary"), "None")
end

function T.test_schedule_summary_rounds_the_countdown_up()
  local function summary(seconds, lang)
    return state.schedule_summary({
      active = true, command = "shutdown", origin = "smartthings",
      remaining_seconds = seconds,
    }, lang or "ko")
  end
  h.assert_equal(summary(240), "종료 · 4분 후")
  h.assert_equal(summary(241), "종료 · 5분 후", "a part minute still counts")
  h.assert_equal(summary(59), "종료 · 곧")
  h.assert_equal(summary(59, "en"), "Shut down · soon")
  h.assert_equal(state.schedule_summary({
    active = true, command = "restart", origin = "ui", remaining_seconds = 600,
  }, "en"), "Restart · in 10 min")
end

function T.test_schedule_summary_reads_hours_and_days_for_the_long_presets()
  -- #89: the presets reach three days, and "4320분 후" is not a number anyone
  -- reads as three days. Under an hour stays in minutes; from an hour on the
  -- row switches to hours, and from a day on to days and hours.
  local function summary(seconds, lang)
    return state.schedule_summary({
      active = true, command = "shutdown", origin = "smartthings",
      remaining_seconds = seconds,
    }, lang or "ko")
  end
  h.assert_equal(summary(59 * 60), "종료 · 59분 후", "under an hour stays in minutes")
  h.assert_equal(summary(60 * 60), "종료 · 1시간 후")
  h.assert_equal(summary(120 * 60), "종료 · 2시간 후")
  h.assert_equal(summary(120 * 60, "en"), "Shut down · in 2 h")
  h.assert_equal(summary(90 * 60), "종료 · 1시간 30분 후")
  h.assert_equal(summary(90 * 60, "en"), "Shut down · in 1 h 30 min")
  h.assert_equal(summary(1440 * 60), "종료 · 1일 후")
  h.assert_equal(summary(4320 * 60), "종료 · 3일 후")
  h.assert_equal(summary(4320 * 60, "en"), "Shut down · in 3 d")
  -- 1일 3시간: the odd minutes are noise at that distance and are dropped.
  h.assert_equal(summary((1440 + 180 + 5) * 60), "종료 · 1일 3시간 후")
  h.assert_equal(summary((1440 + 180 + 5) * 60, "en"), "Shut down · in 1 d 3 h")
end

function T.test_remaining_text_is_the_unit_ladder_on_its_own()
  -- The helper the summary is built from (#89), so the ladder can be read
  -- without a whole schedule table around it.
  h.assert_equal(state.remaining_text(0, "ko"), "곧")
  h.assert_equal(state.remaining_text(1, "ko"), "1분 후")
  h.assert_equal(state.remaining_text(59, "en"), "in 59 min")
  h.assert_equal(state.remaining_text(60, "en"), "in 1 h")
  h.assert_equal(state.remaining_text(1439, "ko"), "23시간 59분 후")
  h.assert_equal(state.remaining_text(2880, "ko"), "2일 후")
  h.assert_equal(state.remaining_text(4320, "ko"), "3일 후")
end

function T.test_schedule_summary_drops_the_origin()
  -- #87: who asked is in `pcDelay.origin` and `pcExec.lastCommand`; on the
  -- summary row it pushed the minutes off the end of the line.
  for _, origin in ipairs({ "smartthings", "ui", "telegram", "remote" }) do
    local summary = state.schedule_summary({
      active = true, command = "shutdown", origin = origin, remaining_seconds = 240,
    }, "ko")
    h.assert_equal(summary, "종료 · 4분 후", origin .. " must not reach the summary")
  end
end

function T.test_session_summary_follows_the_language()
  local function session(t) t.exposed = true; return t end
  h.assert_equal(state.session_summary(session({ locked = true, idle_seconds = 1380, user = "kim" }), "ko"),
    "잠김 · 23분 · kim")
  h.assert_equal(state.session_summary(session({ locked = true, idle_seconds = 1380 }), "en"),
    "Locked · 23 min")
  -- #87: someone at the keyboard is "In use" whatever the idle counter says.
  h.assert_equal(state.session_summary(session({ locked = false, idle_seconds = 30 }), "ko"),
    "사용 중")
  h.assert_equal(state.session_summary(session({ locked = false, idle_seconds = 3000, user = "kim" }), "en"),
    "In use · kim")
  -- ... and under a minute of idle is not worth a "· 0분".
  h.assert_equal(state.session_summary(session({ locked = true, idle_seconds = 59 }), "ko"), "잠김")
  h.assert_equal(state.session_summary(session({ locked = true, idle_seconds = 60 }), "ko"), "잠김 · 1분")
end

function T.test_session_summary_says_off_when_the_block_is_not_exposed()
  -- §3.2: the session block is opt-in, and the row has to say something.
  h.assert_equal(state.session_summary({ exposed = false, locked = true }, "ko"), "꺼짐")
  h.assert_equal(state.session_summary(nil, "en"), "Off")
  h.assert_equal(state.session_summary({}, "ko"), "꺼짐")
end

--------------------------------------------------------------------------------
-- message priority (§4, state.MESSAGE_ORDER)
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
  -- §3.2: `power` in the body is always "on"; powerState comes from the machine.
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
  h.assert_equal(s.power_state, state.SHUTTING_DOWN, "one miss is not enough (§6.2)")
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
  -- §6.2: a PC that told us it was suspending stays "sleeping", not "off".
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
