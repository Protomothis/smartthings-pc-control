-- The values the command and schedule lists rest on and send (design doc §4,
-- §6.9). Every value a list row can hold is also an argument its command
-- accepts, because a list closed without a pick sends the row's current value
-- (platform notes "상세 화면(detailView) 위젯").

local power = require "model.power"

local commands = {}

-- pcDefer.status enum: the string twin of `active` (a list bound to a bool
-- attribute does not render).
commands.IDLE = "idle"
commands.SCHEDULED = "scheduled"
-- The resting value of automation-only string attributes ("" is stored as
-- null by the cloud).
commands.NONE = "none"

-- The delay list rests on `minutesPick`, a one-value enum holding "-1", and
-- `schedule(minutes)` takes that string as a harmless no-op. `MINUTES_NONE` is
-- what `tonumber` makes of it. The argument is a string enum because a closed
-- list sends the value unconverted (platform notes, 2026-09-26).
commands.MINUTES_NONE = -1
commands.MINUTES_PICK = "-1"

--------------------------------------------------------------------------------
-- pcRemote.lastAction and execute
--------------------------------------------------------------------------------

-- What the command list rests on, and the one `execute` argument that does
-- nothing.
commands.ACTION_NONE = "none"

-- What it rests on while the PC is in a transition: the app has no disabled
-- row, so the row says "종료 진행 중…" and the driver holds the commands back.
-- Each is an `execute` argument too, and just as harmless as `none`.
commands.ACTION_BUSY_OFF = "busyOff"
commands.ACTION_BUSY_RESTART = "busyRestart"
commands.ACTION_BUSY_WAKE = "busyWake"
commands.ACTION_BUSY_SLEEP = "busySleep"
commands.ACTION_BUSY_HIBERNATE = "busyHibernate"

commands.BUSY_ACTIONS = {
  commands.ACTION_BUSY_OFF, commands.ACTION_BUSY_RESTART, commands.ACTION_BUSY_WAKE,
  commands.ACTION_BUSY_SLEEP, commands.ACTION_BUSY_HIBERNATE,
}

-- The list's entries, top to bottom: service commands plus `wake` (the WoL
-- sequence). `forceshutdown` stays in automations; `none` and the busy values
-- are resting values, not entries.
commands.EXECUTE_KEYS = {
  "wake", "suspend", "hibernate", "restart", "shutdown", "lock",
  "turnscreenoff", "turnscreenon",
}

-- Every `lastAction` value - the same set, in the same order, as the
-- `execute` command enum in pcRemote.json (capabilities_test.lua checks).
commands.ACTIONS = {
  "none", "wake", "shutdown", "forceshutdown", "restart", "hibernate",
  "suspend", "lock", "turnscreenoff", "turnscreenon",
  "busyOff", "busyRestart", "busyWake", "busySleep", "busyHibernate",
}

local function member(list, value)
  for _, item in ipairs(list) do
    if item == value then
      return true
    end
  end
  return false
end

--- True when `value` is a `lastAction` enum value.
function commands.is_action(value)
  return member(commands.ACTIONS, value)
end

--- True when `value` is one of the five "in progress" resting values.
function commands.is_busy_action(value)
  return member(commands.BUSY_ACTIONS, value)
end

-- The command under way -> the value the list rests on.
local BUSY_FOR_COMMAND = {
  restart = commands.ACTION_BUSY_RESTART,
  suspend = commands.ACTION_BUSY_SLEEP,
  hibernate = commands.ACTION_BUSY_HIBERNATE,
  shutdown = commands.ACTION_BUSY_OFF,
  forceshutdown = commands.ACTION_BUSY_OFF,
}

--- Which busy value a transition shows: `waking` is unambiguous; going away,
--- the `power.stopping` reason first (the only source that knows sleep from
--- hibernation, §3.5), then the command of the pending grace period, else
--- "종료 진행 중".
function commands.busy_action(device_state)
  device_state = device_state or {}
  if device_state.power_state == power.WAKING then
    return commands.ACTION_BUSY_WAKE
  end
  return BUSY_FOR_COMMAND[tostring(device_state.last_stopping_reason or "")]
    or BUSY_FOR_COMMAND[tostring(device_state.schedule_command or "")]
    or commands.ACTION_BUSY_OFF
end

--- The value `lastAction` rests on right now.
function commands.resting_action(device_state)
  if power.is_transitioning(device_state) then
    return commands.busy_action(device_state)
  end
  return commands.ACTION_NONE
end

--- `pcRemote.supportedCommands`, which the list's `supportedValues` reads: the
--- whole menu, or during a transition the one busy value the row rests on -
--- never an empty array, which the app reads as "no restriction" (platform
--- notes "supportedValues"; whether the app honours it is still unmeasured,
--- the driver's guard is what enforces the rule).
function commands.supported_commands(device_state)
  if power.is_transitioning(device_state) then
    return { commands.busy_action(device_state) }
  end
  local out = {}
  for i, key in ipairs(commands.EXECUTE_KEYS) do
    out[i] = key
  end
  return out
end

--------------------------------------------------------------------------------
-- pcDefer.planCommand
--------------------------------------------------------------------------------

-- What the service can schedule (§3.3).
commands.PLAN_COMMANDS = { "shutdown", "restart", "suspend", "hibernate" }
commands.PLAN_DEFAULT = "shutdown"

--- True when `value` is a command `pcDefer.schedule` may carry.
function commands.is_plan_command(value)
  return member(commands.PLAN_COMMANDS, value)
end

--- The first schedulable command among the arguments, else `shutdown`. Callers
--- pass their order of preference: the automation's argument, the user's pick,
--- the `offAction` preference.
function commands.plan_command_for(...)
  for i = 1, select("#", ...) do
    local candidate = select(i, ...)
    if commands.is_plan_command(candidate) then
      return candidate
    end
  end
  return commands.PLAN_DEFAULT
end

return commands
