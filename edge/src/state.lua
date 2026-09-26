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

-- pcDefer.status enum (§4, #83): the string twin of `active`.
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
-- string "-1", and the argument has to accept that value as a harmless no-op.
--
-- #91: it was not enough to widen the range to `minimum: -1`. The value a
-- dismissed list sends skips the presentation's `argumentType` conversion and
-- leaves as the STRING "-1", which the cloud rejects against `integer` with a
-- 422 - so `minutes` is a string enum now (`pcDelay` -> `pcDefer`). The driver
-- keeps reading it as a number: `MINUTES_NONE` is what `tonumber` makes of the
-- resting value, and `MINUTES_PICK` is what goes over the wire.
state.MINUTES_NONE = -1
-- The attribute value is a string: the phone sends a list key, and an enum
-- attribute is a string attribute (a list bound to a number does not render).
-- #91: the `minutes` argument is a string enum for the same reason, and this
-- is a member of it.
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
    -- last polled `schedule.active`, so `pcDefer.schedule` can say whether
    -- it replaced an existing schedule (§3.3) without asking the service twice
    schedule_active = false,
    -- #93: and how long it still has to run, plus what it will run. A PC that
    -- was switched off with a grace period looks exactly like a schedule a
    -- minute away - it IS one, made by the service - and that minute is a power
    -- transition the command list has to show as "in progress"
    -- (`state.is_transitioning`).
    schedule_seconds = 0,
    schedule_command = nil,
    -- #93: the PC's own grace period (`grace.seconds`, §3.2), which is how
    -- close a pending schedule has to be to be that grace rather than something
    -- the user asked for (`state.grace_limit`).
    grace_seconds = nil,
  }
end

local function copy(s)
  return {
    power_state = s.power_state,
    unreachable_count = s.unreachable_count or 0,
    last_stopping_reason = s.last_stopping_reason,
    wake_from = s.wake_from,
    schedule_active = s.schedule_active or false,
    schedule_seconds = s.schedule_seconds or 0,
    schedule_command = s.schedule_command,
    -- Survives every event: the PC's configured grace does not change because
    -- it shut down, and a state machine step has no new status body to read.
    grace_seconds = s.grace_seconds,
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
    -- #93: and with it the grace period the command list was showing as "in
    -- progress" - `is_transitioning` reads these two.
    nxt.schedule_seconds = 0
    nxt.schedule_command = nil
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
-- pcRemote.lastAction (#82, #84, renamed #85 and #93)
--------------------------------------------------------------------------------

-- The value the detail-view list rests on. #84: it is also a valid `execute`
-- argument, and the one the driver does nothing for - closing the list without
-- picking anything sends the row's current value (platform notes "상세 화면(detailView) 위젯"), so the value the row
-- shows has to be harmless.
state.ACTION_NONE = "none"

-- #93: the values the list rests on WHILE the PC is in a power transition. The
-- app has no disabled or loading row, so this is how close we get: the row says
-- "종료 진행 중…" instead of "명령 선택…", and the driver refuses the commands
-- that would pile up behind the one already running (init.lua `is_blocked`).
-- Each one is an `execute` argument too, and just as harmless as `none` - the
-- row is resting on it, so a dismissed list sends it back.
state.ACTION_BUSY_OFF = "busyOff"
state.ACTION_BUSY_RESTART = "busyRestart"
state.ACTION_BUSY_WAKE = "busyWake"
state.ACTION_BUSY_SLEEP = "busySleep"
state.ACTION_BUSY_HIBERNATE = "busyHibernate"

state.BUSY_ACTIONS = {
  state.ACTION_BUSY_OFF, state.ACTION_BUSY_RESTART, state.ACTION_BUSY_WAKE,
  state.ACTION_BUSY_SLEEP, state.ACTION_BUSY_HIBERNATE,
}

-- What the list offers, top to bottom (#82): service command names plus `wake`,
-- which is the WoL sequence rather than a service command. `forceshutdown` is
-- deliberately absent - an irreversible command stays in automations - and
-- neither `none` nor the busy values are menu entries: they are what the row
-- rests on. #93: this is also what `supportedCommands` carries when the PC is
-- not going anywhere.
state.EXECUTE_KEYS = {
  "wake", "suspend", "hibernate", "restart", "shutdown", "lock",
  "turnscreenoff", "turnscreenon",
}

-- Every `lastAction` value. #84 made this the same set as the `execute`
-- `command` enum - service command names (§3.3) plus `none` and `wake` - so
-- that whatever the row holds is an argument `execute` accepts. #93 appends the
-- five busy values for the same reason.
-- capabilities_test.lua checks this against the enum in pcRemote.json, in this
-- order.
state.ACTIONS = {
  "none", "wake", "shutdown", "forceshutdown", "restart", "hibernate",
  "suspend", "lock", "turnscreenoff", "turnscreenon",
  "busyOff", "busyRestart", "busyWake", "busySleep", "busyHibernate",
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

--- #93: true when `value` is one of the five "in progress" resting values.
function state.is_busy_action(value)
  for _, action in ipairs(state.BUSY_ACTIONS) do
    if action == value then
      return true
    end
  end
  return false
end

--------------------------------------------------------------------------------
-- #93: power transitions
--------------------------------------------------------------------------------

-- The fallback for how long a pending schedule may still be the PC's own grace
-- period, for a service too old to say (see `grace_limit`).
--
-- `switch off` (and any `execute` in `default`/`grace` mode) is deferred by the
-- service for the grace period it is configured with - 60 seconds by default -
-- and that wait reaches the driver as an ORDINARY schedule: `executed: false`
-- and a `schedule` block (§3.3). powerState is still `on`, because the PC is.
-- So "the PC is on its way out" is only visible as a schedule about to fire,
-- and the bound below is what separates it from the three-day schedule a user
-- set on purpose - that one must keep the whole command list open.
state.GRACE_SECONDS = 120

--- How close a pending schedule has to be before it counts as the PC leaving.
--
-- The service tells us: `grace.seconds` in every status body (§3.2) is the
-- period this PC is configured with, and it goes up to 30 minutes. Guessing
-- would be wrong in both directions - a PC with a five-minute grace would look
-- idle for the first three of them, and a generous fixed bound would swallow
-- the short schedules a user sets on purpose - so the PC's own number is the
-- bound, and `GRACE_SECONDS` is only what a service too old to send one gets.
--
-- `grace.enabled` is deliberately not consulted: `execute(mode = "grace")`
-- forces the wait whatever the PC is configured to do by default, so the
-- length is the useful half of the block and the flag is not.
function state.grace_limit(device_state)
  local seconds = tonumber((device_state or {}).grace_seconds)
  if seconds and seconds > 0 then
    return math.floor(seconds)
  end
  return state.GRACE_SECONDS
end

-- The commands that take the PC away. A schedule running one of these is a
-- transition; `lock` and the screen commands are not (and the service refuses
-- to schedule them anyway).
local STOPPING_COMMANDS = {
  shutdown = true, forceshutdown = true, restart = true,
  suspend = true, hibernate = true,
}

--- True when the last polled schedule is the PC's own grace period (see above).
function state.is_grace(device_state)
  device_state = device_state or {}
  if device_state.schedule_active ~= true then
    return false
  end
  local seconds = tonumber(device_state.schedule_seconds)
  if not seconds or seconds > state.grace_limit(device_state) then
    return false
  end
  return STOPPING_COMMANDS[tostring(device_state.schedule_command or "")] == true
end

--- #93: true while the PC is going away or coming up.
--
-- `shuttingDown` and `waking` are the two states of §6.2 that say "this will be
-- over in a moment, and nothing else can usefully be asked for until it is".
-- The grace period is the third shape of the same fact (see `grace_limit`);
-- a long schedule is NOT one - a PC that shuts down in three days is an
-- ordinary, fully usable PC.
function state.is_transitioning(device_state)
  device_state = device_state or {}
  local power = device_state.power_state
  if power == state.SHUTTING_DOWN or power == state.WAKING then
    return true
  end
  return state.is_grace(device_state)
end

-- The command that is under way -> the value the list rests on.
local BUSY_FOR_COMMAND = {
  restart = state.ACTION_BUSY_RESTART,
  suspend = state.ACTION_BUSY_SLEEP,
  hibernate = state.ACTION_BUSY_HIBERNATE,
  shutdown = state.ACTION_BUSY_OFF,
  forceshutdown = state.ACTION_BUSY_OFF,
}

--- #93: which busy value a transition shows.
--
-- `waking` is unambiguous. Going away, the wording comes from what is actually
-- happening: the `reason` of the `power.stopping` push (the only source that
-- knows sleep from hibernation, §3.5) first, then the command the pending grace
-- period is going to run. Anything else - an `unknown` reason, a PC that went
-- quiet on its own - reads as "종료 진행 중", which is what the user sees happen.
function state.busy_action(device_state)
  device_state = device_state or {}
  if device_state.power_state == state.WAKING then
    return state.ACTION_BUSY_WAKE
  end
  return BUSY_FOR_COMMAND[tostring(device_state.last_stopping_reason or "")]
    or BUSY_FOR_COMMAND[tostring(device_state.schedule_command or "")]
    or state.ACTION_BUSY_OFF
end

--- #93: the value `lastAction` rests on right now - `busyX` while the PC is in
--- a transition, `none` the rest of the time.
function state.resting_action(device_state)
  if state.is_transitioning(device_state) then
    return state.busy_action(device_state)
  end
  return state.ACTION_NONE
end

--- #93: `pcRemote.supportedCommands`, the experiment of the issue.
--
-- The detail-view list binds `supportedValues` to this attribute, so what is in
-- here is what the menu offers. Normally that is the whole menu; during a
-- transition it is the one busy value the row is resting on, which is not a
-- menu entry at all - the hoped-for effect is a list with nothing to pick.
-- Whether the app honours it is "실측 대기" (platform notes); the driver guard
-- of init.lua is what actually enforces the rule either way.
--
-- NOT an empty array: the community reports that an empty `supportedValues`
-- makes the app fall back to the full list rather than to none.
function state.supported_commands(device_state)
  if state.is_transitioning(device_state) then
    return { state.busy_action(device_state) }
  end
  local out = {}
  for i, key in ipairs(state.EXECUTE_KEYS) do
    out[i] = key
  end
  return out
end

--- #93: remember what the last status body said about the pending schedule,
--- and about the grace period that may be what created it.
--
-- `schedule_active` has been here since #85 (so `schedule` can say it replaced
-- something); the countdown and the command come with it now, because that is
-- all the grace period ever shows up as, and `grace.seconds` comes with them
-- because it is what tells the two apart (`grace_limit`). Mutates and returns
-- `device_state`, which is the freshly copied one `transition` just handed back.
--
-- The grace length is only overwritten when the body carries one: a service too
-- old to send it never will, and a body that arrives without it (a truncated
-- push payload) should not cost us a number we already learned.
function state.remember_schedule(device_state, status)
  device_state = device_state or state.new()
  status = status or {}
  local schedule = status.schedule or {}
  local active = schedule.active == true
  device_state.schedule_active = active
  device_state.schedule_seconds = active
    and math.floor(tonumber(schedule.remaining_seconds) or 0) or 0
  device_state.schedule_command = active and schedule.command or nil
  local grace = tonumber((status.grace or {}).seconds)
  if grace and grace > 0 then
    device_state.grace_seconds = math.floor(grace)
  end
  return device_state
end

--------------------------------------------------------------------------------
-- pcDefer.planCommand (#84, moved off the command capability in #85)
--------------------------------------------------------------------------------

-- What the service can schedule (§3.3). `lock` and the screen commands are not
-- in here: the service refuses to schedule them.
state.PLAN_COMMANDS = { "shutdown", "restart", "suspend", "hibernate" }

-- What a device schedules when nothing else says otherwise.
state.PLAN_DEFAULT = "shutdown"

--- True when `value` is a command `pcDefer.schedule` may carry.
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

--- Format `pcRemote.lastCommand` as "Shut down · SmartThings · 23:05" (§4).
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

-- UTF-8 code points, not bytes: a Korean syllable is three bytes, so `#` would
-- call "연결됨 · WoL 꺼짐" a 22-character string. Continuation bytes are
-- 10xxxxxx (0x80-0xBF); dropping them leaves one byte per code point.
local function char_len(s)
  return #(tostring(s):gsub("[\128-\191]", ""))
end

--- How long `pcInfo.summary` may get before the row truncates it (#97). The
--- detail row is narrow and cuts with "…" without saying so (platform notes,
--- "화면 배치"), so an optional part - today only the WoL adapter's name - is
--- added only while the whole line stays inside this.
state.SUMMARY_MAX_CHARS = 24

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
--
-- #97: and the name of the adapter it is off on, when the service named one
-- and the row can still hold it.
-- @param connection a `pcInfo.connection` value; nil counts as `ok`
-- @param lang the resolved `language` preference
-- @param wol_off true when the PC answers but its adapter has WoL disabled
-- @param adapter the chosen adapter's name (state.wol_adapter), optional
function state.status_summary(connection, lang, wol_off, adapter)
  if connection ~= nil and connection ~= "ok" then
    local parts = { i18n.t(lang, "conn_down") }
    local reason = i18n.connection(lang, connection)
    if reason ~= "" then
      parts[#parts + 1] = reason
    end
    return table.concat(parts, " · ")
  end
  if wol_off == true then
    local ok = i18n.t(lang, "conn_ok")
    if type(adapter) == "string" and adapter ~= "" then
      local named = ok .. " · " .. i18n.t(lang, "wol_off_short_on", adapter)
      if char_len(named) <= state.SUMMARY_MAX_CHARS then
        return named
      end
      -- Too long for the row: `pcInfo.message` carries the name instead.
    end
    return ok .. " · " .. i18n.t(lang, "wol_off_short")
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
-- `service_version` is whatever the status body carried - or, off the connected
-- path since #92, the last one a successful poll saw (`poll.SERVICE_VERSION_FIELD`):
-- a PC that is off has not changed its version, so there is no reason for the
-- row to forget it. Only a PC we have never reached has none, and the row still
-- has to say something (an attribute that was never emitted reads as "-",
-- platform notes "상세 화면(detailView) 위젯"), so it becomes "v?".
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

--- `pcDefer.summary` (#78, reworded in #87): "Shut down · in 4 min", or
--- "None" when nothing is scheduled.
--
-- #87: the origin left the line. Who asked for the shutdown is in
-- `pcDefer.origin` and in `pcRemote.lastCommand`; on the summary row it
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

  if state.wol_off(status) then
    -- §6.4: we still send the magic packet, but say why it may not work.
    -- #97: on the adapter the service chose, by name when it gave one - this
    -- is the line with room for it, and it is the adapter to go and open.
    local adapter = state.wol_adapter(status)
    if adapter then
      return i18n.t(lang, "wol_not_ready_on", adapter)
    end
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
  -- #93: `supportedCommands` is derived from the power state rather than from
  -- the body, exactly like `lastAction`'s resting value - but unlike it, it is
  -- part of every `apply_status`, because the menu has to reopen by itself the
  -- moment the transition ends.
  [caps.COMMAND] = { lastCommand = true, lastAction = true, supportedCommands = true },
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

--- #85: the resting value of every pcRemote / pcDefer attribute that a
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
-- @param service_version #92: the last version a successful poll saw
--   (`poll.last_service_version`), or nil for a device that never answered one.
function state.initial_rows(lang, service_version)
  local events = {}
  -- #86: "없음 (None)", never "": an empty `state` row reads as "-" (platform notes "상세 화면(detailView) 위젯").
  ev(events, caps.COMMAND, "lastCommand", state.format_last_command(nil, lang))
  -- #93: a device that has never been polled is not transitioning, so its menu
  -- is the whole menu. An attribute that was never emitted is the app's "not
  -- all of the state has been reported", and a `supportedValues` that was never
  -- emitted could take the list away entirely.
  ev(events, caps.COMMAND, "supportedCommands", state.supported_commands(nil))
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
  -- The driver/screen halves - the two that matter for "is my update live?" -
  -- are right from the start; the service half is the last version we saw
  -- (#92), and "?" only for a PC that has never answered. #86: on the row's own
  -- capability and, unchanged, on pcInfo, whose definition still declares the
  -- attribute. No `update`: an offer to update can only come from a live answer.
  local versions = state.versions(service_version, lang)
  ev(events, caps.VERSION, "versions", versions)
  ev(events, caps.STATUS, "versions", versions)
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
  -- #93: which entries of the command list are worth offering. `device_state`
  -- already carries the pending schedule (`state.remember_schedule`, called by
  -- the poll before this), so the grace period is visible here too.
  ev(events, caps.COMMAND, "supportedCommands", state.supported_commands(device_state))

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

  local update = status.update or {}
  -- #97: one answer for all three rows below - `wolReady`, the summary and the
  -- message - read off the adapter the service chose when it named one.
  local wol_off = state.wol_off(status)
  local wol_adapter = state.wol_adapter(status)

  ev(events, caps.STATUS, "connection", "ok")
  ev(events, caps.STATUS, "serviceVersion", status.service_version or "")
  ev(events, caps.STATUS, "updateAvailable", update.available == true)
  ev(events, caps.STATUS, "wolReady", not wol_off)
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
  ev(events, caps.STATUS, "summary", state.status_summary("ok", lang, wol_off, wol_adapter))

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

--- The first MAC of a WoL-capable adapter in a status body (§6.4), used when
--- the service is too old to have chosen one itself.
local function first_wol_mac(status)
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

--- #96/#97: the adapter the service picked for WoL, or nil when it did not.
--
-- The PC knows which NIC the hub actually reaches; the driver was guessing
-- "the first adapter with WoL on", which lands on a Hyper-V or VPN adapter as
-- easily as on the real one. So the choice moved to the service and the driver
-- reads it back: `wol.selected` is the answer, and `adapters[].selected` is the
-- same answer spelled on the row it belongs to (a service may carry either).
-- Returns the adapter-shaped table (`name`, `mac`, `ip`, `wol_enabled`,
-- `wol_capable`, and on `selected` also `source`).
function state.wol_selected(status)
  local wol = (status or {}).wol
  if type(wol) ~= "table" then
    return nil
  end
  if type(wol.selected) == "table" and next(wol.selected) ~= nil then
    return wol.selected
  end
  if type(wol.adapters) == "table" then
    for _, a in ipairs(wol.adapters) do
      if type(a) == "table" and a.selected == true then
        return a
      end
    end
  end
  return nil
end

--- The MAC to wake this PC on (§6.4). `wol.selected.mac` when the service chose
--- one, else the old first-enabled-adapter guess. The `macAddress` preference
--- still outranks both - that lives in wol.lua, because it is the user's own
--- value and a status body has no say in it.
function state.wol_mac(status)
  local selected = state.wol_selected(status)
  if selected and type(selected.mac) == "string" and selected.mac ~= "" then
    return selected.mac
  end
  return first_wol_mac(status)
end

--- The name of the chosen adapter ("이더넷"), or nil.
function state.wol_adapter(status)
  local selected = state.wol_selected(status)
  local name = selected and selected.name
  if type(name) == "string" and name ~= "" then
    return name
  end
  return nil
end

--- Is the "WoL is off" warning due? (§6.4)
--
-- `wol.ready` is the service's own summary and stays the answer for a service
-- that has no `selected`. When there is one, its `wol_enabled` is the sharper
-- fact: it is about the single adapter the packet will actually be sent to, so
-- another NIC having WoL on does not hide the warning (and does not raise a
-- false one when the chosen adapter is fine).
function state.wol_off(status)
  local selected = state.wol_selected(status)
  if selected ~= nil and selected.wol_enabled ~= nil then
    return selected.wol_enabled ~= true
  end
  return ((status or {}).wol or {}).ready ~= true
end

return state
