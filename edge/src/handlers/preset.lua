-- `pcPreset.run(slot)` -> `/st/v1/command {command: "preset", value: N}`
-- (#113).

local client = require "client"
local common = require "handlers.common"
local features = require "features"
local fields = require "device.fields"
local i18n = require "i18n"
local log = require "log"
local poll = require "poll"
local rows = require "device.rows"

local preset = {}

--- The list rests on "none", so `run("none")` is a list closed without a
--- pick: it only answers the row. A preset that ran shows as "프리셋 3 실행함"
--- for `rows.PRESET_HOLD_SECONDS`, the row resting on "3" meanwhile - so a
--- `run` of the slot the row shows is the same no-op, and must not start the
--- preset twice. Picking it again works once the row is back on "none".
function preset.run(driver, device, cmd)
  local slot = tostring((((cmd or {}).args or {}).slot) or features.PRESET_NONE)
  if slot == features.PRESET_NONE or not features.is_preset_slot(slot)
      or slot == rows.shown_preset(device) then
    return rows.answer_preset(device)
  end
  local lang = fields.lang(device)
  if fields.extras(device) == nil then
    poll.once(driver, device)
  end
  local extras = fields.extras(device)
  local refusal = features.refusal(extras, "preset")
  if not refusal and features.has(extras, features.PRESETS)
      and not ((extras or {}).preset_slots or {})[slot] then
    refusal = "preset_empty"
  end
  if refusal then
    rows.answer_preset(device)
    rows.emit_note(device, features.note_text(lang, refusal, slot))
    log.info(string.format("preset %s not sent on %s: %s", slot, tostring(device.id), refusal))
    return false
  end
  local ok, body, kind = client.action(device, "preset", tonumber(slot))
  if not ok then
    rows.answer_preset(device)
    local note = features.error_note(kind, body)
    if note then
      rows.emit_note(device, i18n.t(lang, note))
      return false
    end
    common.report_error(device, kind, body)
    return false
  end
  -- The row changes value, forced as every answer is, and goes back on
  -- "none" a few seconds later rather than at the next scheduled poll.
  rows.emit_preset(device, slot, true)
  rows.hold_preset(driver, device)
  poll.answer(driver, device, nil)
  return true
end

return preset
