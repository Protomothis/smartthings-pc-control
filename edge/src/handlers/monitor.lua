-- #153: the `monitor` component - the primary monitor's power (standard
-- `switch`), brightness (standard `switchLevel`) and input (`pcMonitor`),
-- over the PC app's DDC/CI (`monitor_power`, `monitor_brightness`,
-- `monitor_input`; docs/design/v1.3-sensors.md). init.lua hands this
-- component's `switch` here by `command.component`.

local common = require "handlers.common"
local emit = require "device.emit"
local fields = require "device.fields"
local i18n = require "i18n"
local log = require "log"
local rows = require "device.rows"
local sensors = require "sensors"

local monitor = {}

local C = sensors.MONITOR_COMPONENT

local function row(cap, attr)
  return { cap = cap, attr = attr, component = C }
end

local SWITCH = row(sensors.CAP_SWITCH, "switch")
local LEVEL = row(sensors.CAP_LEVEL, "level")
local INPUT = row(sensors.CAP_MONITOR, "input")

local function keys_of(...)
  local keys = {}
  for _, record in ipairs({ ... }) do
    keys[emit.row_key(record)] = true
  end
  return keys
end

-- The rows a command's answer poll forces: the one it came on and the
-- summary, which says what the monitor did.
local SUMMARY = row(sensors.CAP_MONITOR, "summary")
local SWITCH_ROWS = keys_of(SWITCH, SUMMARY)
local LEVEL_ROWS = keys_of(LEVEL, SUMMARY)
local INPUT_ROWS = keys_of(INPUT, SUMMARY)

--- The last status this run read, if any.
local function last_status(device)
  return (fields.extras(device) or {}).last_status
end

-- Re-send what a row shows, forced: the value this run sent (or the hub
-- had), else what the last status says, else the resting value.
local function answer(record, from_status, resting)
  return function(device)
    local value = emit.last_value(device, record)
    if value == nil then
      value = from_status(last_status(device))
    end
    if value == nil then
      value = resting
    end
    emit.rows(device, { { cap = record.cap, attr = record.attr, value = value, component = C } },
      { reason = "answer" })
  end
end

local answer_switch = answer(SWITCH, sensors.monitor_switch, "off")
local answer_level = answer(LEVEL, sensors.monitor_level, 0)
local answer_input = answer(INPUT, sensors.monitor_input, sensors.INPUT_UNKNOWN)

--- True when a `switch` command is for the `monitor` component.
function monitor.is_monitor(cmd)
  return type(cmd) == "table" and cmd.component == C
end

-- A monitor in DPM off often stops listening to DDC/CI, so a failed "on"
-- says to use the monitor's own button (v1.3-sensors.md "끈 뒤 다시 켜기").
local function power_words(on)
  return function(lang, note)
    if on and note == "monitor_failed" then
      return i18n.t(lang, "monitor_wake_failed")
    end
    return nil
  end
end

function monitor.on(driver, device)
  return common.run_feature(driver, device, "monitor_power", "on", answer_switch, SWITCH_ROWS,
    power_words(true))
end

function monitor.off(driver, device)
  return common.run_feature(driver, device, "monitor_power", "off", answer_switch, SWITCH_ROWS,
    power_words(false))
end

--- switchLevel.setLevel(level, rate): the brightness, 0-100. `rate` is
--- ignored - DDC/CI has no fade.
function monitor.set_level(driver, device, cmd)
  local level = tonumber(((cmd or {}).args or {}).level)
  if not level then
    return answer_level(device)
  end
  level = math.max(0, math.min(100, math.floor(level + 0.5)))
  return common.run_feature(driver, device, "monitor_brightness", level, answer_level, LEVEL_ROWS)
end

--- pcMonitor.setInput(input). `unknown` and the input the row shows are what
--- a list closed without a pick sends: they only answer the row. An
--- `otherN` is the service word the last status put in that place; one
--- the monitor does not have is not sent.
function monitor.set_input(driver, device, cmd)
  local key = tostring((((cmd or {}).args or {}).input) or sensors.INPUT_UNKNOWN)
  local shown = emit.last_value(device, INPUT)
  if key == sensors.INPUT_UNKNOWN or key == shown then
    return answer_input(device)
  end
  local word = key
  local is_other = false
  for _, other in ipairs(sensors.OTHER_INPUTS) do
    is_other = is_other or other == key
  end
  if is_other then
    word = sensors.input_map(sensors.primary_monitor(last_status(device))).raw[key]
    if not word then
      answer_input(device)
      rows.emit_note(device, i18n.t(fields.lang(device), "monitor_input_missing"))
      log.info(string.format("monitor input %s not sent on %s: not on this monitor",
        key, tostring(device.id)))
      return false
    end
  end
  return common.run_feature(driver, device, "monitor_input", word, answer_input, INPUT_ROWS)
end

return monitor
