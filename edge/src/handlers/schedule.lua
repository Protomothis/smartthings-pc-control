-- `pcDefer`: the schedule card - the delay list (`schedule`), its command
-- (`setPlanCommand`) and `cancel` (design doc §3.3, §3.4, §4).

local client = require "client"
local common = require "handlers.common"
local fields = require "device.fields"
local i18n = require "i18n"
local poll = require "poll"
local rows = require "device.rows"
local state = require "state"

local schedule = {}

--- setPlanCommand(command): what a schedule without a command of its own
--- runs. A device field, nothing goes to the PC; forced, because re-picking
--- the value the row shows changes nothing. Not during a transition: the row
--- belongs to the schedule that is about to run.
function schedule.set_plan_command(_driver, device, cmd)
  local args = (cmd or {}).args or {}
  local busy_action = common.transition_of(device)
  if busy_action then
    return common.refuse(device, busy_action, function(d)
      rows.emit_plan_command(d, rows.plan_command(d), true)
    end)
  end
  rows.emit_plan_command(device, args.command, true)
end

--- cancel(): DELETE /st/v1/schedule (§3.4). `{"cancelled": false}` when there
--- was nothing to cancel - and then every schedule row is exactly as it was,
--- which is when the forced answer matters most.
function schedule.cancel(driver, device)
  local ok, body, kind = client.cancel(device)
  if not ok then
    common.report_error(device, kind, body)
    return
  end
  local cancelled = (body or {}).cancelled == true
  -- §6.2: cancelling the grace period brings the switch back on.
  local nxt = state.transition(fields.state(device), "schedule_cancelled")
  fields.set_state(device, nxt)
  rows.emit_power(device, nxt)
  poll.answer(driver, device, rows.SCHEDULE_ROWS,
    i18n.t(fields.lang(device), cancelled and "schedule_cancelled" or "schedule_none"))
end

--- schedule(minutes, command?): the same endpoint with minutes > 0 (§3.3).
--
-- The delay list rests on "-1", so that is what a list closed without a pick
-- sends: it only refreshes the tiles. 0 is the list's Cancel entry. The
-- argument is a string enum (a closed list sends the key unconverted,
-- platform notes "상세 화면(detailView) 위젯"); `tonumber` also takes the
-- integers an older profile's automation may carry. The command comes from the
-- automation, else the user's pick, else `offAction` (`rows.plan_command`).
-- Cancelling stays open during a transition; a new schedule does not.
function schedule.schedule(driver, device, cmd)
  local args = (cmd or {}).args or {}
  rows.answer_minutes_pick(device)
  local minutes = math.floor(tonumber(args.minutes) or state.MINUTES_NONE)
  if minutes < 0 then
    return poll.answer(driver, device, nil)
  end
  if minutes == 0 then
    return schedule.cancel(driver, device)
  end
  local busy_action = common.transition_of(device)
  if busy_action then
    return common.refuse(device, busy_action)
  end
  local had_schedule = fields.state(device).schedule_active == true
  local command = state.plan_command_for(args.command, rows.plan_command(device))
  local ok, body, kind = client.command(device, command, "default", minutes)
  if not ok then
    common.report_error(device, kind, body)
    return
  end
  poll.answer(driver, device, rows.SCHEDULE_ROWS,
    had_schedule and i18n.t(fields.lang(device), "schedule_replaced") or nil)
end

return schedule
