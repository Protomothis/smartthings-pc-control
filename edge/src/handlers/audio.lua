-- #154: the `audio` component - `pcAudioOut.setOutput(slot)` and
-- `pcAudioIn.setInput(slot)` -> `/st/v1/command {command: "audio_output" |
-- "audio_input", value: N}` (docs/design/v1.3-sensors.md). The slots are the
-- PC app's (`audio.outputs`/`inputs` in its settings), by number.

local common = require "handlers.common"
local emit = require "device.emit"
local features = require "features"
local fields = require "device.fields"
local i18n = require "i18n"
local log = require "log"
local rows = require "device.rows"
local sensors = require "sensors"

local audio = {}

local C = sensors.AUDIO_COMPONENT

local function record(kind, attr)
  return { cap = sensors.AUDIO_KINDS[kind].cap, attr = attr, component = C }
end

-- The list's row, and the rows its answer poll forces.
local function list_row(kind)
  return record(kind, sensors.AUDIO_KINDS[kind].attr)
end

local function answer_rows(kind)
  return {
    [emit.row_key(list_row(kind))] = true,
    [emit.row_key(record(kind, "names"))] = true,
  }
end

--- The slot the list shows now: this run's value, the hub's, else the last
--- status's.
local function shown(device, kind)
  local value = emit.last_value(device, list_row(kind))
  if value == nil then
    value = sensors.audio_current((fields.extras(device) or {}).last_status, kind)
  end
  return value
end

local function answer(kind)
  return function(device)
    local r = list_row(kind)
    emit.rows(device, { { cap = r.cap, attr = r.attr, value = shown(device, kind), component = C } },
      { reason = "answer" })
  end
end

-- The notes that name the slot: "출력 2 비어 있음", "출력 4 연결 안 됨".
local function words(kind, slot)
  return function(lang, note)
    if note == "audio_slot_empty" or note == "audio_device_missing" then
      return i18n.t(lang, note, i18n.t(lang, sensors.AUDIO_KINDS[kind].word), slot)
    end
    return nil
  end
end

local function is_slot(value)
  local n = tonumber(value)
  return n ~= nil and n == math.floor(n) and n >= 1 and n <= sensors.AUDIO_SLOTS
    and tostring(n) == value
end

--- Switch the default device of `kind` to `slot`. `none` and the slot the
--- list shows are what a list closed without a pick sends: they only answer
--- the row. An empty slot is not sent ("출력 2 비어 있음").
local function switch(kind, driver, device, slot)
  slot = tostring(slot or sensors.SLOT_NONE)
  local k = sensors.AUDIO_KINDS[kind]
  if not is_slot(slot) or slot == shown(device, kind) then
    return answer(kind)(device)
  end
  local extras = fields.extras(device)
  if extras and features.refusal(extras, k.command) == nil then
    local filled = false
    for _, entry in ipairs(sensors.audio_slots(extras.last_status, kind)) do
      filled = filled or tostring(entry.slot) == slot
    end
    if not filled then
      answer(kind)(device)
      local lang = fields.lang(device)
      rows.emit_note(device, i18n.t(lang, "audio_slot_empty", i18n.t(lang, k.word), slot))
      log.info(string.format("%s %s not sent on %s: slot empty", k.command, slot, tostring(device.id)))
      return false
    end
  end
  return common.run_feature(driver, device, k.command, tonumber(slot), answer(kind), answer_rows(kind),
    words(kind, slot))
end

--- pcAudioOut.setOutput(slot)
function audio.set_output(driver, device, cmd)
  return switch("output", driver, device, (((cmd or {}).args or {}).slot))
end

--- pcAudioIn.setInput(slot)
function audio.set_input(driver, device, cmd)
  return switch("input", driver, device, (((cmd or {}).args or {}).slot))
end

return audio
