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

-- powerState enum (§5.1)
state.ON = "on"
state.SLEEPING = "sleeping"
state.HIBERNATED = "hibernated"
state.OFF = "off"
state.WAKING = "waking"
state.SHUTTING_DOWN = "shuttingDown"
state.UNKNOWN = "unknown"

-- "switch" is not a custom capability, so it is referenced by its plain id.
state.CAP_SWITCH = "switch"

-- §6.1: two consecutive unreachable polls before we call the PC off.
state.UNREACHABLE_LIMIT = 2

-- §6.2 / §4.5: power.stopping reason -> resulting powerState.
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
    -- last polled `schedule.active`, so `pcTimer.schedule` can say whether
    -- it replaced an existing schedule (§4.3) without asking the service twice
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
      -- Still waking: silence is expected until the 90s timeout (§6.3).
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

--- Format `pcControl.lastCommand` as "Shut down · SmartThings · 23:05" (§5.1).
function state.format_last_command(last, lang)
  if type(last) ~= "table" or not last.command then
    return ""
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

--- `pcHealth.summary` (#78): the one line that replaced the six raw rows in the
--- detail view. "On · Connected · v1.1.0" when the PC answers,
--- "Not connected · Secret mismatch" when it does not.
-- @param power a `powerState` value
-- @param connection a `pcHealth.connection` value; nil counts as `ok`
-- @param service_version `status.service_version`, appended when known
function state.status_summary(power, connection, service_version, lang)
  local parts = {}
  if connection == nil or connection == "ok" then
    local label = i18n.power(lang, power or state.UNKNOWN)
    if label ~= "" then
      parts[#parts + 1] = label
    end
    parts[#parts + 1] = i18n.t(lang, "conn_ok")
    if type(service_version) == "string" and service_version ~= "" then
      parts[#parts + 1] = service_version
    end
  else
    parts[#parts + 1] = i18n.t(lang, "conn_down")
    local reason = i18n.connection(lang, connection)
    if reason ~= "" then
      parts[#parts + 1] = reason
    end
  end
  return table.concat(parts, " · ")
end

--- `pcTimer.summary` (#78): "Shut down · 4 min left · SmartThings", or an
--- empty string when nothing is scheduled (the row is hidden then).
function state.schedule_summary(schedule, lang)
  schedule = schedule or {}
  if schedule.active ~= true then
    return ""
  end
  local parts = {}
  local command = i18n.command(lang, schedule.command)
  if command ~= "" then
    parts[#parts + 1] = command
  end
  local seconds = math.floor(tonumber(schedule.remaining_seconds) or 0)
  if seconds >= 60 then
    -- Rounded up: "1 min left" is friendlier than "0 min left" at 40 seconds.
    parts[#parts + 1] = i18n.t(lang, "schedule_remaining", math.ceil(seconds / 60))
  else
    parts[#parts + 1] = i18n.t(lang, "schedule_soon")
  end
  local origin = i18n.origin(lang, schedule.origin)
  if origin ~= "" then
    parts[#parts + 1] = origin
  end
  return table.concat(parts, " · ")
end

--- `pcUser.summary` (#78): "Locked · idle 20 min · kim". Only composed when
--- the session block is exposed; see apply_status.
function state.session_summary(session, lang)
  session = session or {}
  local parts = {
    i18n.t(lang, session.locked == true and "session_locked" or "session_unlocked"),
    i18n.t(lang, "session_idle", math.floor((tonumber(session.idle_seconds) or 0) / 60)),
  }
  if type(session.user) == "string" and session.user ~= "" then
    parts[#parts + 1] = session.user
  end
  return table.concat(parts, " · ")
end

-- `pcHealth.message` shows one sentence, so several applicable notices need an
-- order. Highest priority first:
--
--   error            a failed request (unauthorized / unreachable / bad request)
--   incompatible     protocol mismatch, service or driver too old
--   wol_not_ready    WoL is off on the PC's adapter, so `switch on` may not land
--   update_available a newer service release is out
--   no_secret        the service accepts unauthenticated calls (§4.1)
--   note             a one-off confirmation from a command handler
--
-- The first two are produced by poll.lua from an err_kind — there is no status
-- body to map when a request fails — and reach this function as `opts.error`,
-- which wins over everything a successful status could say.
state.MESSAGE_ORDER = {
  "error", "incompatible", "wol_not_ready", "update_available", "no_secret", "note",
}

--- The single `pcHealth.message` for a status body (§5.1), by MESSAGE_ORDER.
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
    -- §6.3: we still send the magic packet, but say why it may not work.
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
    -- §4.1: a service with no secret is still a healthy connection, so the
    -- warning goes here instead of into the `connection` enum.
    return i18n.t(lang, "no_secret")
  end

  if type(opts.note) == "string" and opts.note ~= "" then
    return opts.note
  end
  return ""
end

-- Every attribute `apply_status` can emit, capability id -> attribute names.
-- capabilities_test.lua checks this against the JSON in `capabilities/`, so a
-- new attribute that is emitted but never defined fails the suite.
local ATTRIBUTES = {
  [state.CAP_SWITCH] = { switch = true },
  [caps.POWER_STATE] = { powerState = true },
  [caps.COMMAND] = { lastCommand = true },
  [caps.SCHEDULE] = {
    active = true, command = true, remainingSeconds = true,
    executeAt = true, origin = true, summary = true,
  },
  [caps.STATUS] = {
    connection = true, serviceVersion = true, updateAvailable = true,
    wolReady = true, lastSeen = true, message = true, summary = true,
  },
  [caps.SESSION] = {
    locked = true, idleMinutes = true, user = true,
    summary = true, exposed = true,
  },
}

--- The set above. Read-only: it is a constant, not a copy.
function state.attributes_used()
  return ATTRIBUTES
end

--- Turn a `GET /st/v1/status` body into capability events (§4.2 -> §5.1).
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
  ev(events, caps.SCHEDULE, "command", active and i18n.command(lang, schedule.command) or "")
  ev(events, caps.SCHEDULE, "remainingSeconds",
    active and math.floor(tonumber(schedule.remaining_seconds) or 0) or 0)
  -- `execute_at` is RFC3339 with the PC's offset; the app shows the local time.
  ev(events, caps.SCHEDULE, "executeAt", active and hhmm(schedule.execute_at) or "")
  ev(events, caps.SCHEDULE, "origin", active and i18n.origin(lang, schedule.origin) or "")
  -- #78: the one row the detail view shows, and only while `active` is true.
  ev(events, caps.SCHEDULE, "summary", state.schedule_summary(schedule, lang))

  local wol = status.wol or {}
  local update = status.update or {}

  ev(events, caps.STATUS, "connection", "ok")
  ev(events, caps.STATUS, "serviceVersion", status.service_version or "")
  ev(events, caps.STATUS, "updateAvailable", update.available == true)
  ev(events, caps.STATUS, "wolReady", wol.ready == true)
  ev(events, caps.STATUS, "lastSeen", opts.now or "")
  ev(events, caps.STATUS, "message",
    state.status_message(status, { lang = lang, error = opts.error, note = opts.note }))
  -- #78: "On · Connected · v1.1.0". A successful status is always `ok` here;
  -- the failure wording comes from poll.emit_connection.
  ev(events, caps.STATUS, "summary", state.status_summary(power, "ok", status.service_version, lang))

  -- §4.2: the session block is opt-in. `exposed` is emitted either way so the
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
    ev(events, caps.SESSION, "summary", state.session_summary(session, lang))
  end

  return events
end

--- The first MAC of a WoL-capable adapter in a status body (§5.4: the
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
