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
  }
end

local function copy(s)
  return {
    power_state = s.power_state,
    unreachable_count = s.unreachable_count or 0,
    last_stopping_reason = s.last_stopping_reason,
    wake_from = s.wake_from,
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

--- Format `pcCommand.lastCommand` as "Shut down · SmartThings · 23:05" (§5.1).
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

--- Turn a `GET /st/v1/status` body into capability events (§4.2 -> §5.1).
--
-- `device_state` supplies powerState (already advanced with `transition`), so
-- this function never decides power on its own. `opts.now` is the ISO string
-- used for `lastSeen` and `opts.lang` selects the ko/en strings; both are passed
-- in to keep the function pure and the tests deterministic.
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
  ev(events, caps.SCHEDULE, "executeAt", active and (schedule.execute_at or "") or "")
  ev(events, caps.SCHEDULE, "origin", active and i18n.origin(lang, schedule.origin) or "")

  local wol = status.wol or {}
  local update = status.update or {}
  local wol_ready = wol.ready == true
  -- §4.1: a service with no secret still counts as a healthy connection; the
  -- warning goes into `message` so automations on `connection == ok` keep working.
  local warnings = {}
  if status.secret_set == false then
    warnings[#warnings + 1] = i18n.t(lang, "no_secret")
  end
  if not wol_ready then
    -- §6.3: we still try WoL, but say why it may not land.
    warnings[#warnings + 1] = i18n.t(lang, "wol_not_ready")
  end

  ev(events, caps.STATUS, "connection", "ok")
  ev(events, caps.STATUS, "serviceVersion", status.service_version or "")
  ev(events, caps.STATUS, "updateAvailable", update.available == true)
  ev(events, caps.STATUS, "wolReady", wol_ready)
  ev(events, caps.STATUS, "lastSeen", opts.now or "")
  ev(events, caps.STATUS, "message", table.concat(warnings, " · "))

  local session = status.session or {}
  if session.exposed == true then
    ev(events, caps.SESSION, "locked", session.locked == true)
    ev(events, caps.SESSION, "idleMinutes",
      math.floor((tonumber(session.idle_seconds) or 0) / 60))
    ev(events, caps.SESSION, "user", session.user or "")
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
