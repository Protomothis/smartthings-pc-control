-- The rows the driver writes itself rather than from a status body (#129):
-- power, the status sentence, and the rows the user picks from - the command
-- list (`lastAction`), the schedule's command and delay, the preset list and
-- the message row.
--
-- A list row rests on a value that is also a harmless argument, because a
-- list closed without a pick sends the row's current value (platform notes
-- "상세 화면(detailView) 위젯"). Two patterns cover every such row:
--
--   answer   a command arrived on the row: re-send what it shows, forced, so
--            the app's spinner ends even though nothing changed
--   settle   the resting value moved (a transition began or ended, a preset
--            flash is over): send it forced, and once more on the next call -
--            an unchanged value is never re-sent, because a burst of forced
--            events is what loses them (platform notes "강제 이벤트를 연발하면")

local caps = require "caps"
local clock = require "device.clock"
local emit = require "device.emit"
local features = require "features"
local fields = require "device.fields"
local i18n = require "i18n"
local state = require "state"

local rows = {}

local function answer(device, cap, attr, value)
  emit.rows(device, { { cap = cap, attr = attr, value = value } }, { reason = "answer" })
  return value
end

-- `shown` is what the row says, `target` where it should rest; `send(value)`
-- emits it forced. `owed_field` remembers the one repeat. True when sent.
local function settle(device, owed_field, shown, target, send)
  if shown ~= target then
    send(target)
    fields.set(device, owed_field, target)
    return true
  end
  local owed = fields.get(device, owed_field)
  if owed ~= nil and owed == target then
    fields.set(device, owed_field, nil)
    send(target)
    return true
  end
  return false
end

--------------------------------------------------------------------------------
-- power and the status rows
--------------------------------------------------------------------------------

--- `switch` + `powerState` for a runtime state (§6.2). `force` answers a
--- `switch off` the driver refused: the switch still reads "on" while the PC
--- shuts down, and the toggle waits for an event.
function rows.emit_power(device, s, force)
  local power = (s or {}).power_state or state.UNKNOWN
  emit.rows(device, {
    { cap = state.CAP_SWITCH, attr = "switch", value = state.switch_for(power) },
    { cap = caps.POWER_STATE, attr = "powerState", value = power },
  }, { reason = force == true and "answer" or "poll" })
end

--- `pcInfo.message` alone.
function rows.emit_message(device, message)
  emit.rows(device, { { cap = caps.STATUS, attr = "message", value = message or "" } })
end

--- A one-off notice on both pcInfo rows, as the answer to a command the driver
--- did not forward ("종료 진행 중 · 끝난 뒤 다시 시도"). The next poll writes the
--- normal wording back.
function rows.emit_note(device, note)
  emit.rows(device, {
    { cap = caps.STATUS, attr = "message", value = note or "" },
    { cap = caps.STATUS, attr = "summary", value = note or "" },
  }, { reason = "answer" })
end

--- `pcInfo.connection`, `message` and `summary` for a failed request, plus the
--- version row. The service half of the versions is the last one a successful
--- poll saw (a PC that is off has not changed its version); the summary says
--- when the PC last answered.
-- @param deps optional; `deps.now` replaces the clock
-- @param kind optional, the err_kind (client.lua): `app_down` (connection
--   refused, `connection` is "unreachable") says "PC 앱 응답 없음" instead of
--   when the PC was last seen
function rows.emit_connection(device, connection, message, deps, kind)
  local lang = fields.lang(device)
  local versions = state.versions(fields.service_version(device), lang)
  local seen = fields.last_seen(device)
  local seen_ago = seen and (clock.epoch(deps) - seen) or nil
  emit.rows(device, {
    { cap = caps.STATUS, attr = "connection", value = connection },
    { cap = caps.STATUS, attr = "message", value = message or "" },
    { cap = caps.STATUS, attr = "summary",
      value = state.status_summary(connection, lang, nil, nil,
        { seen_ago = seen_ago, app_down = kind == "app_down" }) },
    -- The row is pcVersion's; pcInfo still defines the attribute.
    { cap = caps.VERSION, attr = "versions", value = versions },
    { cap = caps.STATUS, attr = "versions", value = versions },
  })
end

--------------------------------------------------------------------------------
-- pcRemote.lastAction: the command list
--------------------------------------------------------------------------------

--- Emit `lastAction` and remember it. It rests on `none`, or on a `busyX`
--- value during a transition; what ran is `lastCommand`'s. A value outside
--- the enum becomes `none` (the hub rejects it).
function rows.emit_action(device, action, force)
  local value = state.is_action(action) and action or state.ACTION_NONE
  fields.set(device, fields.LAST_ACTION, value)
  emit.rows(device, { { cap = caps.COMMAND, attr = "lastAction", value = value } },
    { reason = force == true and "answer" or "poll" })
  return value
end

--- The value the command list should rest on now.
function rows.resting_action(device)
  return state.resting_action(fields.state(device))
end

--- Answer an `execute` on the command list. Settles a repeat `ensure_action`
--- still owed: this event is that repeat.
function rows.answer_action(device)
  local resting = rows.resting_action(device)
  rows.owe_action_repeat(device, nil)
  return rows.emit_action(device, resting, true)
end

--- Keep `lastAction` on its resting value (every poll, push and wake).
--- Returns true when something was emitted.
function rows.ensure_action(device)
  return settle(device, fields.LAST_ACTION_CONFIRM, fields.get(device, fields.LAST_ACTION),
    rows.resting_action(device), function(value) rows.emit_action(device, value, true) end)
end

--- Remember that one forced re-emit of `value` is owed (nil clears it).
function rows.owe_action_repeat(device, value)
  fields.set(device, fields.LAST_ACTION_CONFIRM, value)
  return value
end

--------------------------------------------------------------------------------
-- pcDefer.planCommand and minutesPick: the schedule rows
--------------------------------------------------------------------------------

-- The rows the schedule list is bound to; `schedule` and `cancel` are answered
-- on them.
rows.SCHEDULE_ROWS = {
  [caps.SCHEDULE .. ".status"] = true,
  [caps.SCHEDULE .. ".active"] = true,
  [caps.SCHEDULE .. ".summary"] = true,
}

--- Emit `planCommand` (what a schedule without a command runs, the user's
--- pick) and remember it. An unschedulable value is coerced (§3.3).
function rows.emit_plan_command(device, command, force)
  local value = state.plan_command_for(command)
  fields.set(device, fields.PLAN_COMMAND, value)
  emit.rows(device, { { cap = caps.SCHEDULE, attr = "planCommand", value = value } },
    { reason = force == true and "answer" or "poll" })
  return value
end

--- Answer a `schedule` on the delay list, which rests on "-1" for good.
function rows.answer_minutes_pick(device)
  return answer(device, caps.SCHEDULE, "minutesPick", state.MINUTES_PICK)
end

--- The command this device schedules when none is given: the pick, else a
--- schedulable `offAction` preference, else `shutdown`.
function rows.plan_command(device)
  return state.plan_command_for(fields.get(device, fields.PLAN_COMMAND),
    ((device or {}).preferences or {}).offAction)
end

--- Paint `planCommand` on a device that has never picked one: a list whose
--- attribute was never emitted does not open.
function rows.ensure_plan_command(device)
  if state.is_plan_command(fields.get(device, fields.PLAN_COMMAND)) then
    return false
  end
  rows.emit_plan_command(device, rows.plan_command(device))
  return true
end

--------------------------------------------------------------------------------
-- pcPreset.lastPreset: the preset list
--------------------------------------------------------------------------------

-- How long the row says "프리셋 3 실행함" before it rests on "none" again. It
-- has to outlast the poll that answers the command (a shared answer window
-- closes 1.5 s later, plus the PC's reply), and the clock counts whole seconds.
rows.PRESET_HOLD_SECONDS = 5
-- The owed repeat of the return to "none" follows this much later.
rows.PRESET_REPEAT_SECONDS = 2

--- The value the preset list shows now.
function rows.shown_preset(device)
  local shown = fields.get(device, fields.LAST_PRESET)
  if shown == features.PRESET_NONE or features.is_preset_slot(shown) then
    return shown
  end
  return features.PRESET_NONE
end

--- Emit `lastPreset` and remember it, with when a slot started showing. Not
--- persisted: a restarted driver repaints the row on "none".
function rows.emit_preset(device, value, force, deps)
  if not features.is_preset_slot(value) then
    value = features.PRESET_NONE
  end
  fields.set(device, fields.LAST_PRESET, value)
  fields.set(device, fields.LAST_PRESET_AT, value ~= features.PRESET_NONE and clock.epoch(deps) or nil)
  emit.rows(device, { { cap = caps.PRESET, attr = "lastPreset", value = value } },
    { reason = force == true and "answer" or "poll" })
  return value
end

--- Answer a `run`: the value the list shows, forced.
function rows.answer_preset(device)
  fields.set(device, fields.LAST_PRESET_CONFIRM, nil)
  return answer(device, caps.PRESET, "lastPreset", rows.shown_preset(device))
end

--- Put the preset list back on "none" once a slot has shown for
--- `PRESET_HOLD_SECONDS` (from `hold_preset`'s timer, and from every poll and
--- push as the fallback). `held`: the timer says the hold is over, whatever the
--- clock's whole seconds say. Returns true when something was emitted.
function rows.ensure_preset(device, deps, held)
  local shown = rows.shown_preset(device)
  if shown ~= features.PRESET_NONE and not held then
    local at = tonumber(fields.get(device, fields.LAST_PRESET_AT))
    local now = clock.epoch(deps)
    if at and now - at < rows.PRESET_HOLD_SECONDS and now >= at then
      return false
    end
  end
  return settle(device, fields.LAST_PRESET_CONFIRM, shown, features.PRESET_NONE,
    function(value) rows.emit_preset(device, value, true, deps) end)
end

-- Make `fn` after `delay` the device's one pending preset timer, cancelling
-- the one it replaces; `fn` runs only while it is still the current one.
local function preset_timer(driver, device, delay, name, fn)
  local previous = fields.get(device, fields.LAST_PRESET_TIMER)
  if type(previous) == "table" and previous.timer then
    pcall(function() driver:cancel_timer(previous.timer) end)
  end
  fields.set(device, fields.LAST_PRESET_TIMER, nil)
  local token = {}
  local ok, timer = pcall(function()
    return driver:call_with_delay(delay, function()
      if fields.get(device, fields.LAST_PRESET_TIMER) ~= token then
        return
      end
      fields.set(device, fields.LAST_PRESET_TIMER, nil)
      fn()
    end, name)
  end)
  if not ok then
    return false
  end
  token.timer = timer
  fields.set(device, fields.LAST_PRESET_TIMER, token)
  return true
end

--- After a preset ran: back on "none" `PRESET_HOLD_SECONDS` from now, and the
--- owed repeat `PRESET_REPEAT_SECONDS` after that, instead of at the first poll
--- after the hold. Another run replaces the pending timer. False when no timer
--- could be set (the polls and pushes then do the same, later).
function rows.hold_preset(driver, device)
  if not driver then
    return false
  end
  return preset_timer(driver, device, rows.PRESET_HOLD_SECONDS, "preset-reset", function()
    rows.ensure_preset(device, nil, true)
    -- A poll that got there first leaves only the repeat, just sent above.
    if fields.get(device, fields.LAST_PRESET_CONFIRM) then
      preset_timer(driver, device, rows.PRESET_REPEAT_SECONDS, "preset-repeat", function()
        rows.ensure_preset(device)
      end)
    end
  end)
end

--------------------------------------------------------------------------------
-- pcToast.lastMessage: the message row
--------------------------------------------------------------------------------

--- The last text `send` delivered, else "없음" - never "" (the cloud stores
--- that as null and the row reads "-").
function rows.shown_toast(device)
  local sent = fields.get(device, fields.LAST_TOAST)
  if type(sent) == "string" and sent ~= "" then
    return sent
  end
  return i18n.t(fields.lang(device), "toast_none")
end

--- The `lastMessage` record for `value` (default: what the row shows now).
function rows.toast_row(device, value)
  return { cap = caps.TOAST, attr = "lastMessage", value = value or rows.shown_toast(device) }
end

--- A text `send` delivered: remember it (persisted - a restart must not put
--- "없음" back over it) and put it on the row, forced.
function rows.emit_toast(device, text)
  if type(text) == "string" and text ~= "" then
    fields.set(device, fields.LAST_TOAST, text)
  end
  local value = rows.shown_toast(device)
  emit.rows(device, { rows.toast_row(device, value) }, { reason = "answer" })
  return value
end

--- Answer a `send` that did not go out: the row keeps what it shows, forced.
function rows.answer_toast(device)
  local value = rows.shown_toast(device)
  emit.rows(device, { rows.toast_row(device, value) }, { reason = "answer" })
  return value
end

--- Keep the row painted with every poll and push: unforced, so only the first
--- emit of a run goes out.
function rows.ensure_toast(device)
  emit.rows(device, { rows.toast_row(device) })
end

return rows
