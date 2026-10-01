-- The PC's power: the main `switch`, `refresh`, and `pcRemote` - the command
-- list (`execute`) and the no-argument commands (design doc §4, §6.2, §6.9).

local apps = require "apps"
local caps = require "caps"
local client = require "client"
local common = require "handlers.common"
local fields = require "device.fields"
local poll = require "poll"
local rows = require "device.rows"
local state = require "state"
local wol = require "wol"

local power = {}

--- switch.on: the WoL sequence, the device goes to `waking` (§6.2/§6.4).
--- Allowed during a transition: while `waking` a second magic packet is free,
--- while `shuttingDown` it is "switch on cancels the grace period" (§6.2).
function power.switch_on(driver, device)
  local nxt = state.transition(fields.state(device), "switch_on")
  fields.set_state(device, nxt)
  rows.emit_power(device, nxt)
  -- The list says "켜는 중…" now: the polls fail until the PC is up.
  rows.ensure_action(device)
  wol.wake(driver, device)
end

-- The rows `switch off` is answered on.
local POWER_ROWS = {
  [state.CAP_SWITCH .. ".switch"] = true,
  [caps.POWER_STATE .. ".powerState"] = true,
}

--- switch.off: the `offAction` preference with the service's own grace. Not
--- during a transition: the toggle cannot be greyed out, so the off is
--- answered with the switch value the power state implies - still "on" while
--- shutting down, so the toggle springs back.
function power.switch_off(driver, device)
  local busy_action = common.transition_of(device)
  if busy_action then
    return common.refuse(device, busy_action, function(d)
      rows.emit_power(d, fields.state(d), true)
    end)
  end
  local prefs = device.preferences or {}
  local command = prefs.offAction or "shutdown"
  local ok, body, kind = client.command(device, command, "default", 0)
  if not ok then
    common.report_error(device, kind, body)
    return
  end
  -- With a grace period the PC is still "on"; the toggle waits for an event.
  poll.answer(driver, device, POWER_ROWS)
end

--- refresh. An app child asks its PC.
function power.refresh(driver, device)
  if apps.is_child(device) then
    local parent = apps.parent_of(driver, device)
    if not parent then
      return false
    end
    return poll.once(driver, parent)
  end
  poll.once(driver, device)
end

--- The no-argument commands of §4 -> the service command (§3.3). `wake` is the
--- WoL sequence. The detail view sends `execute(command)` from one list
--- instead; the buttons stay for devices on an older profile and for
--- hub-local automations and scenes.
power.BUTTONS = {
  wake = "wake",
  suspend = "suspend",
  hibernate = "hibernate",
  restart = "restart",
  shutdown = "shutdown",
  lock = "lock",
  screenOff = "turnscreenoff",
  screenOn = "turnscreenon",
}

--- The §3.3 `mode` of a command sent without one: the `buttonMode`
--- preference (§7). `default` follows the PC's grace period (the toast stays
--- cancellable), `immediate` skips it.
local function button_mode(device)
  local mode = (device.preferences or {}).buttonMode
  if mode == "immediate" then
    return "immediate"
  end
  return "default"
end

--- Run one service command - the path every command row takes.
--
-- The command list is answered first, with its resting value, whatever
-- happens next. `none` and the five `busyX` values are the resting values, so
-- they are what a list closed without a pick sends: they only refresh the
-- tiles. During a transition everything else - `lock` and the screen commands
-- too, a PC leaving or not up cannot do them - is held back, except `wake`.
-- What ran shows on `lastCommand`, from the service's own `last_command`.
local function run_command(driver, device, service_command, mode, minutes)
  rows.answer_action(device)
  if service_command == nil or service_command == "" or service_command == state.ACTION_NONE
      or state.is_busy_action(service_command) then
    return poll.answer(driver, device, nil)
  end
  if service_command == "wake" then
    return power.switch_on(driver, device)
  end
  local busy_action = common.transition_of(device)
  if busy_action then
    -- The command list was already answered above.
    return common.refuse(device, busy_action)
  end
  minutes = math.floor(tonumber(minutes) or 0)
  local ok, body, kind = client.command(device, service_command, mode, minutes)
  if not ok then
    common.report_error(device, kind, body)
    return
  end
  poll.answer(driver, device, nil)
end

--- The handler for one no-argument command.
function power.button(service_command)
  return function(driver, device)
    return run_command(driver, device, service_command, button_mode(device), 0)
  end
end

--- pcRemote.execute(command, mode, minutes). `minutes > 0` turns the same
--- endpoint into a schedule (§3.3). The list sends `command` alone: the mode
--- then follows `buttonMode` and the delay is 0; an automation fills all three.
function power.execute(driver, device, cmd)
  local args = (cmd or {}).args or {}
  return run_command(driver, device, args.command,
    args.mode or button_mode(device), args.minutes or 0)
end

return power
