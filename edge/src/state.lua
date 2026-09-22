-- Pure state layer: status JSON -> capability events, and the power state
-- machine of design doc §6.2.
--
-- Nothing here touches st.* or cosock, and nothing here emits: `apply_status`
-- returns a list of `{ cap = <capability id>, attr = <attribute>, value = ... }`
-- records and the driver layer (poll.lua) turns them into `device:emit_event`
-- calls. That split is what makes the interesting logic unit-testable without a
-- hub.

local caps = require "caps"
local i18n = require "i18n"

local state = {}

-- powerState enum (§4)
state.ON = "on"
state.SLEEPING = "sleeping"
state.HIBERNATED = "hibernated"
state.OFF = "off"
state.WAKING = "waking"
state.SHUTTING_DOWN = "shuttingDown"
state.UNKNOWN = "unknown"

-- pcDelay.status enum (§4, #83): the string twin of `active`.
state.IDLE = "idle"
-- Placeholder for automation-only string attributes that have nothing to say
-- (the app never shows them; "" would be stored as null by the cloud).
state.NONE = "none"
state.SCHEDULED = "scheduled"

-- #88: the 예약 시간 row's resting value, and the `minutes` argument that does
-- nothing. Closing a detailView list without picking anything sends the row's
-- CURRENT state value as the argument (platform notes "상세 화면(detailView) 위젯"), so the row has to rest
-- on something `schedule(minutes: integer)` accepts. It rested on `status`
-- ("idle"/"scheduled"), which the cloud rejected before the hub ever saw it -
-- the "네트워크 오류" popup of #88. `minutesPick` is a one-value enum holding the
-- string "-1", and `minimum: -1` makes that a valid, harmless argument.
state.MINUTES_NONE = -1
-- The attribute value is a string: the phone sends a list key, and an enum
-- attribute is a string attribute (a list bound to a number does not render).
state.MINUTES_PICK = "-1"

-- "switch" is not a custom capability, so it is referenced by its plain id.
state.CAP_SWITCH = "switch"

-- §6.2: two consecutive unreachable polls before we call the PC off.
state.UNREACHABLE_LIMIT = 2

-- §6.2 / §3.5: power.stopping reason -> resulting powerState.
local STOPPING_STATE = {
  suspend = state.SLEEPING,
  hibernate = state.HIBERNATED,
  shutdown = state.SHUTTING_DOWN,
  restart = state.SHUTTING_DOWN,
  unknown = state.SHUTTING_DOWN,
}

--- A fresh device state. Kept as a plain table so it can live in a device
--- field and be copied cheaply.
function state.new(power_state)
  return {
    power_state = power_state or state.UNKNOWN,
    -- consecutive failed polls classified as "unreachable"
    unreachable_count = 0,
    -- reason of the last power.stopping push, so an unreachable PC that told us
    -- it was suspending is not reported as off
    last_stopping_reason = nil,
    -- powerState to fall back to when a wake attempt times out
    wake_from = nil,
    -- last polled `schedule.active`, so `pcDelay.schedule` can say whether
    -- it replaced an existing schedule (§3.3) without asking the service twice
    schedule_active = false,
  }
end

local function copy(s)
  return {
    power_state = s.power_state,
    unreachable_count = s.unreachable_count or 0,
    last_stopping_reason = s.last_stopping_reason,
    wake_from = s.wake_from,
    schedule_active = s.schedule_active or false,
  }
end

--- §6.2: the `switch` attribute is derived from powerState, never tracked
--- separately. That is what keeps the switch from sticking "on" after the PC
--- shut down (the bug in the stock PCControl driver).
function state.switch_for(power_state)
  if power_state == state.ON or power_state == state.WAKING
      or power_state == state.SHUTTING_DOWN then
    return "on"
  end
  return "off"
end

--- Apply one event to a device state, returning a NEW state table.
--
-- Events (§6.2): `status_ok`, `unreachable`, `stopping` (+reason), `switch_on`,
-- `wake_timeout`, `schedule_cancelled`. The event may be a string with the
-- reason as third argument, or a table `{ type = ..., reason = ... }`.
function state.transition(s, event, arg)
  s = s or state.new()
  local reason = arg
  if type(event) == "table" then
    reason = event.reason or reason
    event = event.type
  end

  local cur = s.power_state
  local nxt = copy(s)

  if event == "status_ok" then
    -- The service answered, so the PC is up regardless of what we believed.
    nxt.power_state = state.ON
    nxt.unreachable_count = 0
    nxt.last_stopping_reason = nil
    nxt.wake_from = nil
  elseif event == "unreachable" then
    nxt.unreachable_count = (s.unreachable_count or 0) + 1
    if cur == state.WAKING then
      -- Still waking: silence is expected until the 90s timeout (§6.4).
      nxt.power_state = state.WAKING
    elseif cur == state.SLEEPING or cur == state.HIBERNATED then
      -- A sleeping PC is supposed to be unreachable; keep the finer state.
      nxt.power_state = cur
    elseif nxt.unreachable_count >= state.UNREACHABLE_LIMIT then
      local preserved = STOPPING_STATE[s.last_stopping_reason or ""]
      if preserved == state.SLEEPING or preserved == state.HIBERNATED then
        nxt.power_state = preserved
      else
        nxt.power_state = state.OFF
      end
    end
  elseif event == "stopping" then
    nxt.last_stopping_reason = reason or "unknown"
    nxt.unreachable_count = 0
    nxt.wake_from = nil
    nxt.power_state = STOPPING_STATE[nxt.last_stopping_reason] or state.SHUTTING_DOWN
  elseif event == "switch_on" then
    nxt.unreachable_count = 0
    if cur ~= state.ON then
      if cur ~= state.WAKING then
        nxt.wake_from = cur
      end
      nxt.power_state = state.WAKING
    end
  elseif event == "wake_timeout" then
    if cur == state.WAKING then
      -- §6.2: back to the previous state, the message says what happened.
      nxt.power_state = s.wake_from or state.OFF
      nxt.wake_from = nil
    end
  elseif event == "schedule_cancelled" then
    nxt.schedule_active = false
    -- §6.2: cancelling the grace period on the PC must bring the switch back on.
    if cur == state.SHUTTING_DOWN then
      nxt.power_state = state.ON
      nxt.last_stopping_reason = nil
      nxt.unreachable_count = 0
    end
  end

  return nxt
end

local function ev(list, cap, attr, value)
  list[#list + 1] = { cap = cap, attr = attr, value = value }
end

-- "2026-09-17T23:05:00+09:00" -> "23:05"; anything else comes back unchanged.
local function hhmm(iso)
  if type(iso) ~= "string" then
    return ""
  end
  return iso:match("T(%d%d:%d%d)") or iso
end

--------------------------------------------------------------------------------
-- pcExec.lastAction (#82, #84, renamed #85)
--------------------------------------------------------------------------------

-- The value the detail-view list rests on. #84: it is also a valid `execute`
-- argument, and the one the driver does nothing for - closing the list without
-- picking anything sends the row's current value (platform notes "상세 화면(detailView) 위젯"), so the value the row
-- shows has to be harmless.
state.ACTION_NONE = "none"

-- Every `lastAction` value. #84 made this the same set as the `execute`
-- `command` enum - service command names (§3.3) plus `none` and `wake` - so
-- that whatever the row holds is an argument `execute` accepts.
-- capabilities_test.lua checks this against the enum in pcExec.json.
state.ACTIONS = {
  "none", "wake", "shutdown", "forceshutdown", "restart", "hibernate",
  "suspend", "lock", "turnscreenoff", "turnscreenon",
}

--- True when `value` is a `lastAction` enum value.
function state.is_action(value)
  for _, action in ipairs(state.ACTIONS) do
    if action == value then
      return true
    end
  end
  return false
end

--------------------------------------------------------------------------------
-- pcDelay.planCommand (#84, moved off the command capability in #85)
--------------------------------------------------------------------------------

-- What the service can schedule (§3.3). `lock` and the screen commands are not
-- in here: the service refuses to schedule them.
state.PLAN_COMMANDS = { "shutdown", "restart", "suspend", "hibernate" }

-- What a device schedules when nothing else says otherwise.
state.PLAN_DEFAULT = "shutdown"

--- True when `value` is a command `pcDelay.schedule` may carry.
function state.is_plan_command(value)
  for _, command in ipairs(state.PLAN_COMMANDS) do
    if command == value then
      return true
    end
  end
  return false
end

--- The first schedulable command among the arguments, else `shutdown`.
--
-- The callers pass their preference order: the command an automation sent, the
-- `planCommand` the user picked on the detail view, and the `offAction`
-- preference (#84 - before it, the schedule row could only pick the minutes).
function state.plan_command_for(...)
  for i = 1, select("#", ...) do
    local candidate = select(i, ...)
    if state.is_plan_command(candidate) then
      return candidate
    end
  end
  return state.PLAN_DEFAULT
end

--- Format `pcExec.lastCommand` as "Shut down · SmartThings · 23:05" (§4).
--- #84: this is the row that says what ran; `lastAction` stays on `none`.
--
-- #86: a PC that has run nothing yet gets a sentence, not an empty string. The
-- phone draws "-" for an empty `state` row exactly as it does for one that was
-- never emitted (platform notes "상세 화면(detailView) 위젯"), and a "-" with a label beside it reads like a fault.
function state.format_last_command(last, lang)
  if type(last) ~= "table" or not last.command then
    return i18n.t(lang, "last_command_none")
  end
  local parts = { i18n.command(lang, last.command) }
  local origin = i18n.origin(lang, last.origin)
  if origin ~= "" then
    parts[#parts + 1] = origin
  end
  local at = hhmm(last.at)
  if at ~= "" then
    parts[#parts + 1] = at
  end
  return table.concat(parts, " · ")
end

--- `pcInfo.summary` (#78): the one line that replaced the six raw rows in the
--- detail view. "Connected" when the PC answers, "Not connected · Secret
--- mismatch" when it does not.
--
-- #82: the power word is gone from this line. The detail view now starts with
-- the `pcPower.powerState` row, so repeating "On" here only made the status
-- line longer than the phone shows.
--
-- #87: the service version left too - the row below it is nothing but the two
-- version numbers - and so did the "set a secret" / "an update is out"
-- notices, which are advice rather than status and stay in `pcInfo.message`.
-- What is left is the connection, plus the one warning that changes what the
-- switch will do: WoL off on the adapter means `switch on` cannot work.
-- @param connection a `pcInfo.connection` value; nil counts as `ok`
-- @param lang the resolved `language` preference
-- @param wol_off true when the PC answers but its adapter has WoL disabled
function state.status_summary(connection, lang, wol_off)
  if connection ~= nil and connection ~= "ok" then
    local parts = { i18n.t(lang, "conn_down") }
    local reason = i18n.connection(lang, connection)
    if reason ~= "" then
      parts[#parts + 1] = reason
    end
    return table.concat(parts, " · ")
  end
  if wol_off == true then
    return i18n.t(lang, "conn_ok") .. " · " .. i18n.t(lang, "wol_off_short")
  end
  return i18n.t(lang, "conn_ok")
end

-- The "v" a release tag and `status.service_version` carry ("v1.1.0"). The row
-- writes its own, so the one on the value would be doubled.
local function bare_version(v)
  return (tostring(v):gsub("^[vV]", ""))
end

-- "1.0.0" -> "1.0" (#87). The driver's patch digit is noise on a row read at a
-- glance: what a user compares against the channel is the minor version.
-- Anything that is not two dotted numbers is left alone.
local function major_minor(v)
  local major, minor = tostring(v):match("^(%d+)%.(%d+)")
  if major then
    return major .. "." .. minor
  end
  return tostring(v)
end

--- `pcVersion.versions` (#85, its own capability since #86, reworded in #87):
--- "v1.1.0 · 드라이버 1.0", plus " · 업데이트 v1.2.0" while one is out.
--
-- The last row of the last card, and the one every "the app still looks the
-- way it did" report needs. #87 dropped the screen (profile) name from it: it
-- is an implementation detail no user can act on, and on a narrow row it
-- pushed the two numbers that matter out of sight.
--
-- #86: the same value goes out under `pcVersion.versions` (the row on screen)
-- and `pcInfo.versions` (the definition, which cannot be dropped without yet
-- another rename and would make the app report missing state if left unset).
--
-- `service_version` is whatever the status body carried; a PC we have not
-- reached yet has none, and the row still has to say something (an attribute
-- that was never emitted reads as "-", platform notes "상세 화면(detailView) 위젯"), so it becomes "v?".
-- @param update `status.update`; the update half is appended only when
--   `available` is set, so a current PC's row stays two numbers long
function state.versions(service_version, lang, update)
  local service
  if type(service_version) == "string" and service_version ~= "" then
    service = bare_version(service_version)
  else
    service = i18n.t(lang, "version_unknown")
  end
  local driver = "?"
  local ok, value = pcall(require, "driver_version")
  if ok and type(value) == "string" and value ~= "" then
    driver = major_minor(value)
  end
  local text = i18n.t(lang, "versions", service, driver)
  update = update or {}
  if update.available == true then
    local latest = update.latest
    if type(latest) == "string" and latest ~= "" then
      return text .. " · " .. i18n.t(lang, "versions_update", bare_version(latest))
    end
    return text .. " · " .. i18n.t(lang, "versions_update_plain")
  end
  return text
end

--- `pcDelay.summary` (#78, reworded in #87): "Shut down · in 4 min", or
--- "None" when nothing is scheduled.
--
-- #87: the origin left the line. Who asked for the shutdown is in
-- `pcDelay.origin` and in `pcExec.lastCommand`; on the summary row it
-- pushed the minutes - the one number the row exists for - off the end.
--- #89: how far away a schedule is, in the largest unit that fits.
--
-- The preset list reaches three days, and "4320분 후" is a number nobody reads
-- as three days. So: under a minute is "곧", under an hour stays in minutes,
-- under a day is hours (with the odd minutes, because "2시간 5분 후" is what the
-- user set), and from a day on it is days and hours - the minutes at that
-- distance are noise on a row the phone already truncates.
function state.remaining_text(minutes, lang)
  minutes = math.floor(tonumber(minutes) or 0)
  if minutes < 1 then
    return i18n.t(lang, "schedule_soon")
  end
  if minutes < 60 then
    return i18n.t(lang, "schedule_remaining", minutes)
  end
  if minutes < 1440 then
    local hours, rest = math.floor(minutes / 60), minutes % 60
    if rest == 0 then
      return i18n.t(lang, "schedule_remaining_h", hours)
    end
    return i18n.t(lang, "schedule_remaining_hm", hours, rest)
  end
  local days = math.floor(minutes / 1440)
  local hours = math.floor((minutes % 1440) / 60)
  if hours == 0 then
    return i18n.t(lang, "schedule_remaining_d", days)
  end
  return i18n.t(lang, "schedule_remaining_dh", days, hours)
end

function state.schedule_summary(schedule, lang)
  schedule = schedule or {}
  if schedule.active ~= true then
    -- Always a sentence: the row is shown regardless (visibleCondition is not
    -- honoured for capability presentations on the phone).
    return i18n.t(lang, "schedule_idle")
  end
  local parts = {}
  local command = i18n.command(lang, schedule.command)
  if command ~= "" then
    parts[#parts + 1] = command
  end
  local seconds = math.floor(tonumber(schedule.remaining_seconds) or 0)
  if seconds >= 60 then
    -- Rounded up: "in 2 min" is friendlier than "in 1 min" at 100 seconds.
    parts[#parts + 1] = state.remaining_text(math.ceil(seconds / 60), lang)
  else
    -- Under a minute the row says "곧", never "1분 후".
    parts[#parts + 1] = i18n.t(lang, "schedule_soon")
  end
  return table.concat(parts, " · ")
end

--- `pcUser.summary` (#78, reworded in #87): "In use", "Locked · 20 min", or
--- "Off" while the session block is not exposed.
--
-- #87: the idle minutes ride on "Locked" alone and only from a full minute on.
-- Someone sitting at the PC is "In use" whatever the idle counter says, and
-- "Locked · 0 min" reads like a fault rather than like "just now".
-- The user name is appended only when the service sent one (§3.2: it is a
-- separate opt-in from `exposed`).
function state.session_summary(session, lang)
  session = session or {}
  if session.exposed ~= true then
    return i18n.t(lang, "session_off")
  end
  local parts = {}
  if session.locked == true then
    local locked = i18n.t(lang, "session_locked")
    local minutes = math.floor((tonumber(session.idle_seconds) or 0) / 60)
    if minutes >= 1 then
      locked = locked .. " · " .. i18n.t(lang, "session_idle", minutes)
    end
    parts[#parts + 1] = locked
  else
    parts[#parts + 1] = i18n.t(lang, "session_unlocked")
  end
  if type(session.user) == "string" and session.user ~= "" then
    parts[#parts + 1] = session.user
  end
  return table.concat(parts, " · ")
end

-- `pcInfo.message` shows one sentence, so several applicable notices need an
-- order. Highest priority first:
--
--   error            a failed request (unauthorized / unreachable / bad request)
--   incompatible     protocol mismatch, service or driver too old
--   wol_not_ready    WoL is off on the PC's adapter, so `switch on` may not land
--   update_available a newer service release is out
--   no_secret        the service accepts unauthenticated calls (§3.1)
--   note             a one-off confirmation from a command handler
--
-- The first two are produced by poll.lua from an err_kind — there is no status
-- body to map when a request fails — and reach this function as `opts.error`,
-- which wins over everything a successful status could say.
state.MESSAGE_ORDER = {
  "error", "incompatible", "wol_not_ready", "update_available", "no_secret", "note",
}

--- The single `pcInfo.message` for a status body (§4), by MESSAGE_ORDER.
-- @param opts `lang`, `error` (a ready-made message that outranks the body),
--   `note` (a confirmation shown only when nothing is wrong)
function state.status_message(status, opts)
  opts = opts or {}
  if type(opts.error) == "string" and opts.error ~= "" then
    return opts.error
  end

  status = status or {}
  local lang = opts.lang

  if (status.wol or {}).ready ~= true then
    -- §6.4: we still send the magic packet, but say why it may not work.
    return i18n.t(lang, "wol_not_ready")
  end
  if (status.update or {}).available == true then
    local latest = status.update.latest
    if type(latest) == "string" and latest ~= "" then
      return i18n.t(lang, "update_available", latest)
    end
    return i18n.t(lang, "update_available_plain")
  end
  if status.secret_set == false then
    -- §3.1: a service with no secret is still a healthy connection, so the
    -- warning goes here instead of into the `connection` enum.
    return i18n.t(lang, "no_secret")
  end

  if type(opts.note) == "string" and opts.note ~= "" then
    return opts.note
  end
  return ""
end

-- #87 removed `state.status_notice`. The summary row used to repeat the
-- `message` ladder in short wording, which put "set a secret" and "an update
-- is out" - neither of them a thing to do right now - on the row the user
-- glances at. The row carries the connection and the WoL warning only; the
-- whole ladder is still in `pcInfo.message`.

-- Every attribute the driver can emit, capability id -> attribute names.
-- capabilities_test.lua checks this against the JSON in `capabilities/`, so a
-- new attribute that is emitted but never defined fails the suite.
--
-- `apply_status` produces all of them except `lastAction` and `planCommand`,
-- which no status body carries: the first is the placeholder the command row
-- rests on (poll.ensure_action, #84) and the second is the user's own choice
-- (poll.emit_plan_command). #85 moved `planCommand` to the schedule capability:
-- the app groups detail rows by the capability that owns them, so the row that
-- picks what a schedule runs has to belong to the schedule card.
local ATTRIBUTES = {
  [state.CAP_SWITCH] = { switch = true },
  [caps.POWER_STATE] = { powerState = true },
  [caps.COMMAND] = { lastCommand = true, lastAction = true },
  [caps.SCHEDULE] = {
    active = true, status = true, command = true, remainingSeconds = true,
    executeAt = true, origin = true, summary = true, planCommand = true,
    -- #88: the value the 예약 시간 list rests on. No status body carries it -
    -- it never changes - but it has to be emitted, or the row reads "-" and
    -- the list does not open (poll.answer_minutes_pick, state.initial_rows).
    minutesPick = true,
  },
  [caps.STATUS] = {
    connection = true, serviceVersion = true, updateAvailable = true,
    wolReady = true, lastSeen = true, message = true, summary = true,
    versions = true,
  },
  [caps.SESSION] = {
    locked = true, idleMinutes = true, user = true,
    summary = true, exposed = true,
  },
  -- #86: the version row, moved onto a capability of its own so that it is not
  -- drawn in a narrow half-width column next to `pcInfo.summary`.
  [caps.VERSION] = { versions = true },
}

--- The set above. Read-only: it is a constant, not a copy.
function state.attributes_used()
  return ATTRIBUTES
end

--- #85: the resting value of every pcExec / pcDelay attribute that a
--- status body does not carry on its own (plus the `pcInfo.versions` row, which
--- says something useful even before the first poll), for a device that has
--- never been polled successfully.
--
-- An attribute that was never emitted reads as "-" on the phone (platform notes "상세 화면(detailView) 위젯") and
-- keeps the app saying not all of the device's state has been reported. A
-- device that has just been added - or one that was just migrated onto the new
-- capability ids, where every attribute starts out unset - therefore gets the
-- whole set painted once before the first poll answers.
--
-- `lastAction` and `planCommand` are not here: they are the two rows the user
-- (and the `offAction` preference) owns, so poll.lua emits them through
-- `emit_action` / `emit_plan_command`, which also persist the choice.
function state.initial_rows(lang)
  local events = {}
  -- #86: "없음 (None)", never "": an empty `state` row reads as "-" (platform notes "상세 화면(detailView) 위젯").
  ev(events, caps.COMMAND, "lastCommand", state.format_last_command(nil, lang))
  ev(events, caps.SCHEDULE, "active", false)
  ev(events, caps.SCHEDULE, "status", state.IDLE)
  -- Never "": the cloud records an empty string as null (platform notes).
  ev(events, caps.SCHEDULE, "command", state.NONE)
  ev(events, caps.SCHEDULE, "remainingSeconds", 0)
  ev(events, caps.SCHEDULE, "executeAt", state.NONE)
  ev(events, caps.SCHEDULE, "origin", state.NONE)
  ev(events, caps.SCHEDULE, "summary", state.schedule_summary(nil, lang))
  -- #88: the 예약 시간 row's resting value. A list whose attribute was never
  -- emitted shows "-" and does not open (platform notes "상세 화면(detailView) 위젯"), and this one never
  -- moves off "-1", so this is the only place a new device is told it.
  ev(events, caps.SCHEDULE, "minutesPick", state.MINUTES_PICK)
  -- The service version is not known yet, so the row says "?" for it and the
  -- driver/screen halves - the two that matter for "is my update live?" - are
  -- right from the start. #86: on the row's own capability and, unchanged, on
  -- pcInfo, whose definition still declares the attribute.
  ev(events, caps.VERSION, "versions", state.versions(nil, lang))
  ev(events, caps.STATUS, "versions", state.versions(nil, lang))
  return events
end

--- Turn a `GET /st/v1/status` body into capability events (§3.2 -> §4).
--
-- `device_state` supplies powerState (already advanced with `transition`), so
-- this function never decides power on its own. `opts.now` is the formatted
-- clock time used for `lastSeen`, `opts.lang` selects the ko/en strings, and
-- `opts.error` / `opts.note` feed the `message` ladder (state.MESSAGE_ORDER);
-- they are all passed in to keep the function pure and the tests deterministic.
function state.apply_status(device_state, status, opts)
  device_state = device_state or state.new()
  status = status or {}
  opts = opts or {}
  local lang = opts.lang

  local events = {}
  local power = device_state.power_state or state.UNKNOWN

  ev(events, state.CAP_SWITCH, "switch", state.switch_for(power))
  ev(events, caps.POWER_STATE, "powerState", power)

  ev(events, caps.COMMAND, "lastCommand", state.format_last_command(status.last_command, lang))

  local schedule = status.schedule or {}
  local active = schedule.active == true
  ev(events, caps.SCHEDULE, "active", active)
  -- #83: the same fact as a string enum. A detailView `list` whose `state`
  -- points at a boolean attribute does not render at all (the row shows "-"
  -- with no chevron, measured 2026-09-22), so the schedule list reads
  -- `status` and `active` stays for the automation condition.
  ev(events, caps.SCHEDULE, "status", active and state.SCHEDULED or state.IDLE)
  ev(events, caps.SCHEDULE, "command", active and i18n.command(lang, schedule.command) or state.NONE)
  ev(events, caps.SCHEDULE, "remainingSeconds",
    active and math.floor(tonumber(schedule.remaining_seconds) or 0) or 0)
  -- `execute_at` is RFC3339 with the PC's offset; the app shows the local time.
  ev(events, caps.SCHEDULE, "executeAt", active and hhmm(schedule.execute_at) or state.NONE)
  ev(events, caps.SCHEDULE, "origin", active and i18n.origin(lang, schedule.origin) or state.NONE)
  -- #78: the one row the detail view shows, and only while `active` is true.
  ev(events, caps.SCHEDULE, "summary", state.schedule_summary(schedule, lang))

  local wol = status.wol or {}
  local update = status.update or {}

  ev(events, caps.STATUS, "connection", "ok")
  ev(events, caps.STATUS, "serviceVersion", status.service_version or "")
  ev(events, caps.STATUS, "updateAvailable", update.available == true)
  ev(events, caps.STATUS, "wolReady", wol.ready == true)
  ev(events, caps.STATUS, "lastSeen", opts.now or "")
  -- #85: the bottom row of the bottom card, refreshed on every poll so a
  -- service update shows up without touching the driver. #86: the row itself
  -- is `pcVersion.versions`; `pcInfo.versions` keeps being emitted because the
  -- attribute is still defined there (platform notes "허브의 정의 캐시").
  local versions = state.versions(status.service_version, lang, update)
  ev(events, caps.VERSION, "versions", versions)
  ev(events, caps.STATUS, "versions", versions)
  local message = state.status_message(status, { lang = lang, error = opts.error, note = opts.note })
  ev(events, caps.STATUS, "message", message)
  -- #78/#82/#87: "Connected", or "Connected · WoL off" when the switch cannot
  -- do what the row above it offers. A successful status is always `ok` here;
  -- the failure wording comes from poll.emit_connection.
  ev(events, caps.STATUS, "summary", state.status_summary("ok", lang, wol.ready ~= true))

  -- §3.2: the session block is opt-in. `exposed` is emitted either way so the
  -- detail view can hide the session row again when the user opts out; the
  -- values themselves stay untouched when it is off, so the tiles keep what
  -- they last showed rather than flipping to a made-up "unlocked, 0 minutes,
  -- nobody".
  local session = status.session or {}
  ev(events, caps.SESSION, "exposed", session.exposed == true)
  if session.exposed == true then
    -- `locked` and `idle_seconds` are absent when the service cannot read the
    -- session (nobody logged in, WTS refused); false/0 is the honest default
    -- for an attribute that has no "unknown".
    ev(events, caps.SESSION, "locked", session.locked == true)
    ev(events, caps.SESSION, "idleMinutes",
      math.floor((tonumber(session.idle_seconds) or 0) / 60))
    ev(events, caps.SESSION, "user", session.user or "")
  end
  -- #87: `session_summary` reads `exposed` itself and says "Off" when the
  -- block is not exposed, so the row always has a word.
  ev(events, caps.SESSION, "summary", state.session_summary(session, lang))

  return events
end

--- The first MAC of a WoL-capable adapter in a status body (§6.4: the
--- `macAddress` preference is auto-filled from it).
function state.wol_mac(status)
  local adapters = (status or {}).wol and status.wol.adapters
  if type(adapters) ~= "table" then
    return nil
  end
  local fallback
  for _, a in ipairs(adapters) do
    if type(a) == "table" and type(a.mac) == "string" and a.mac ~= "" then
      if a.wol_enabled == true then
        return a.mac
      end
      fallback = fallback or a.mac
    end
  end
  return fallback
end

return state
