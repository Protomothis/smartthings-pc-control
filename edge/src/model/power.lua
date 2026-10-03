-- The power state machine (design doc §6.2) and the transitions a command
-- list has to show as "in progress" (§6.9). Pure: no st.*, no device.

local power = {}

-- powerState enum (§4)
power.ON = "on"
power.SLEEPING = "sleeping"
power.HIBERNATED = "hibernated"
power.OFF = "off"
power.WAKING = "waking"
power.SHUTTING_DOWN = "shuttingDown"
power.UNKNOWN = "unknown"

-- "switch" is a standard capability, referenced by its plain id.
power.CAP_SWITCH = "switch"

-- §6.2: two consecutive unreachable polls before the PC counts as off.
power.UNREACHABLE_LIMIT = 2

-- §6.2 / §3.5: power.stopping reason -> resulting powerState.
local STOPPING_STATE = {
  suspend = power.SLEEPING,
  hibernate = power.HIBERNATED,
  shutdown = power.SHUTTING_DOWN,
  restart = power.SHUTTING_DOWN,
  unknown = power.SHUTTING_DOWN,
}

--- A fresh device state: a plain table, so it can live in a device field.
function power.new(power_state)
  return {
    power_state = power_state or power.UNKNOWN,
    -- consecutive failed polls classified as "unreachable"
    unreachable_count = 0,
    -- true while the last poll was refused (`app_down`): the PC is on, the PC
    -- app is not answering
    app_down = false,
    -- the reason of the last power.stopping push, so a PC that said it was
    -- suspending is not reported as off once it stops answering
    last_stopping_reason = nil,
    -- the powerState a timed-out wake falls back to
    wake_from = nil,
    -- the last polled schedule (`remember_schedule`): whether one is pending,
    -- how long it still runs and what it runs - the PC's own grace period is
    -- such a schedule - plus the grace length the PC is configured with
    schedule_active = false,
    schedule_seconds = 0,
    schedule_command = nil,
    grace_seconds = nil,
    -- what the last status said about the v1.2.0 features (features.remember);
    -- nil until a status has been read in this driver run
    extras = nil,
  }
end

local function copy(s)
  return {
    power_state = s.power_state,
    unreachable_count = s.unreachable_count or 0,
    app_down = s.app_down == true,
    last_stopping_reason = s.last_stopping_reason,
    wake_from = s.wake_from,
    schedule_active = s.schedule_active or false,
    schedule_seconds = s.schedule_seconds or 0,
    schedule_command = s.schedule_command,
    -- A power event changes neither the configured grace nor the features
    -- the PC offers; `extras` is replaced whole with every status, so the
    -- table can be shared.
    grace_seconds = s.grace_seconds,
    extras = s.extras,
  }
end

--- §6.2: `switch` is derived from powerState, never tracked on its own - that
--- is what keeps it from sticking "on" after the PC shut down.
function power.switch_for(power_state)
  if power_state == power.ON or power_state == power.WAKING
      or power_state == power.SHUTTING_DOWN then
    return "on"
  end
  return "off"
end

--- Apply one event to a device state, returning a NEW state table.
--
-- Events (§6.2): `status_ok`, `unreachable`, `app_down`, `app_answered`,
-- `stopping` (+reason), `switch_on`, `wake_timeout`, `schedule_cancelled`. The event may be a string with the
-- reason as third argument, or a table `{ type = ..., reason = ... }`.
function power.transition(s, event, arg)
  s = s or power.new()
  local reason = arg
  if type(event) == "table" then
    reason = event.reason or reason
    event = event.type
  end

  local cur = s.power_state
  local nxt = copy(s)

  if event == "status_ok" then
    -- The service answered, so the PC is up regardless of what we believed.
    nxt.power_state = power.ON
    nxt.unreachable_count = 0
    nxt.app_down = false
    nxt.last_stopping_reason = nil
    nxt.wake_from = nil
  elseif event == "app_down" then
    -- The connection was refused: the PC itself answered
    -- (client.transport_kind), so it is on and only the PC app is missing.
    -- Never a step towards `off`.
    nxt.unreachable_count = 0
    nxt.app_down = true
    if cur ~= power.SHUTTING_DOWN then
      -- `on` stays `on`. From `off`, `sleeping`, `hibernated`, `unknown` or
      -- `waking` the PC has demonstrably come up (booted, woken by hand or by
      -- this driver's WoL) and the app has not started yet: `on`, so the
      -- switch tells the truth and a wake is over (poll.lua cancels its timer).
      -- `shuttingDown` stays: Windows stops the service first, the PC refuses
      -- until its network goes, and then `unreachable` takes it to `off`.
      nxt.power_state = power.ON
      nxt.last_stopping_reason = nil
      nxt.wake_from = nil
    end
  elseif event == "app_answered" then
    -- The PC app answered with an error (401, 404, …): it is not down, and
    -- nothing else is known about the PC.
    nxt.app_down = false
  elseif event == "unreachable" then
    nxt.unreachable_count = (s.unreachable_count or 0) + 1
    -- No answer at all now, not even a refusal.
    nxt.app_down = false
    if cur == power.WAKING then
      -- Still waking: silence is expected until the 90s timeout (§6.4).
      nxt.power_state = power.WAKING
    elseif cur == power.SLEEPING or cur == power.HIBERNATED then
      -- A sleeping PC is supposed to be unreachable; keep the finer state.
      nxt.power_state = cur
    elseif nxt.unreachable_count >= power.UNREACHABLE_LIMIT then
      local preserved = STOPPING_STATE[s.last_stopping_reason or ""]
      if preserved == power.SLEEPING or preserved == power.HIBERNATED then
        nxt.power_state = preserved
      else
        nxt.power_state = power.OFF
      end
    end
  elseif event == "stopping" then
    nxt.last_stopping_reason = reason or "unknown"
    nxt.unreachable_count = 0
    -- Only a running PC app pushes.
    nxt.app_down = false
    nxt.wake_from = nil
    nxt.power_state = STOPPING_STATE[nxt.last_stopping_reason] or power.SHUTTING_DOWN
  elseif event == "switch_on" then
    nxt.unreachable_count = 0
    if cur ~= power.ON then
      if cur ~= power.WAKING then
        nxt.wake_from = cur
      end
      nxt.power_state = power.WAKING
    end
  elseif event == "wake_timeout" then
    if cur == power.WAKING then
      -- §6.2: back to the previous state; the message says what happened.
      nxt.power_state = s.wake_from or power.OFF
      nxt.wake_from = nil
    end
  elseif event == "schedule_cancelled" then
    -- With the schedule goes the grace period `is_transitioning` reads.
    nxt.schedule_active = false
    nxt.schedule_seconds = 0
    nxt.schedule_command = nil
    -- §6.2: cancelling the grace period brings the switch back on.
    if cur == power.SHUTTING_DOWN then
      nxt.power_state = power.ON
      nxt.last_stopping_reason = nil
      nxt.unreachable_count = 0
    end
  end

  return nxt
end

--- Why the display-only rows say the PC is not there (design doc §6.2 "꺼진
--- PC의 표시 줄"), or nil when they keep what the last status said:
---
---   "app_down"    the last poll was refused: the PC is on, its app is not
---   "off"         two unreachable polls (or a wake that gave up)
---   "sleeping"    unreachable after it said it was going to sleep
---   "hibernated"  the same for hibernation
---
--- A first unreachable poll of a PC that was on is none of them (one lost
--- answer is not a PC that is gone), nor are `waking` and `shuttingDown`.
function power.offline_mode(s)
  s = s or {}
  if s.app_down == true then
    return "app_down"
  end
  local p = s.power_state
  if p == power.OFF or p == power.SLEEPING or p == power.HIBERNATED then
    return p
  end
  return nil
end

--------------------------------------------------------------------------------
-- transitions (§6.9)
--------------------------------------------------------------------------------

-- How close a pending schedule has to be to count as the PC's own grace
-- period, for a service too old to send `grace.seconds`. A `switch off` with a
-- grace period reaches the driver as nothing but an ordinary schedule a minute
-- away, while powerState is still `on`.
power.GRACE_SECONDS = 120

--- How close a pending schedule has to be before it counts as the PC leaving:
--- the PC's own `grace.seconds` (§3.2), else `GRACE_SECONDS`. A guessed bound
--- would be wrong both ways - a five-minute grace looks idle, a generous one
--- swallows the short schedules a user sets on purpose. `grace.enabled` is not
--- read: `execute(mode = "grace")` waits whatever the default is.
function power.grace_limit(device_state)
  local seconds = tonumber((device_state or {}).grace_seconds)
  if seconds and seconds > 0 then
    return math.floor(seconds)
  end
  return power.GRACE_SECONDS
end

-- The commands that take the PC away. `lock` and the screen commands do not
-- (and the service refuses to schedule them).
local STOPPING_COMMANDS = {
  shutdown = true, forceshutdown = true, restart = true,
  suspend = true, hibernate = true,
}

--- True when the last polled schedule is the PC's own grace period.
function power.is_grace(device_state)
  device_state = device_state or {}
  if device_state.schedule_active ~= true then
    return false
  end
  local seconds = tonumber(device_state.schedule_seconds)
  if not seconds or seconds > power.grace_limit(device_state) then
    return false
  end
  return STOPPING_COMMANDS[tostring(device_state.schedule_command or "")] == true
end

--- True while the PC is going away or coming up: `shuttingDown`, `waking`, or
--- a grace period. A long schedule is not one - a PC that shuts down in three
--- days is an ordinary, fully usable PC.
function power.is_transitioning(device_state)
  device_state = device_state or {}
  local p = device_state.power_state
  if p == power.SHUTTING_DOWN or p == power.WAKING then
    return true
  end
  return power.is_grace(device_state)
end

--- §6.9: remember what a status body says about the pending schedule and the
--- PC's grace length. Mutates and returns `device_state` (the copy
--- `transition` just made). The grace length is only overwritten when the body
--- carries one, so a body without it does not cost a number already learned.
function power.remember_schedule(device_state, status)
  device_state = device_state or power.new()
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

return power
