-- `pcToast.send(text)` -> `POST /st/v1/notify {text}` (#108).

local client = require "client"
local common = require "handlers.common"
local features = require "features"
local fields = require "device.fields"
local i18n = require "i18n"
local log = require "log"
local poll = require "poll"
local rows = require "device.rows"

local toast = {}

--- The row is bound to `lastMessage` (a row bound to nothing ends in "네트워크
--- 오류", platform notes "표준 capability"), so every `send` is answered there:
--- a text that went out becomes the row's value, any other path re-sends what
--- it shows. The outcome in words goes to `pcInfo.message` only - a routine
--- may send several a minute, and the summary is the PC's state line. Gated
--- like the other v1.2.0 commands; the text is cleaned and cut to the
--- service's 200 characters first.
function toast.send(driver, device, cmd)
  local text = ((cmd or {}).args or {}).text
  local lang = fields.lang(device)
  local cleaned = features.notify_text(text)
  if not cleaned then
    rows.answer_toast(device)
    rows.emit_message(device, i18n.t(lang, "notify_empty"))
    return false
  end
  if fields.extras(device) == nil then
    poll.once(driver, device)
  end
  local refusal = features.refusal(fields.extras(device), nil, features.NOTIFY)
  if refusal then
    rows.answer_toast(device)
    rows.emit_message(device, features.note_text(lang, refusal))
    log.info(string.format("notification not sent on %s: %s", tostring(device.id), refusal))
    return false
  end
  local ok, body, kind = client.notify(device, cleaned)
  if not ok then
    rows.answer_toast(device)
    local note = features.notify_error_note(kind, body)
    if note then
      rows.emit_message(device, i18n.t(lang, note))
      return false
    end
    common.report_error(device, kind, body)
    return false
  end
  rows.emit_toast(device, cleaned)
  rows.emit_message(device, i18n.t(lang, "notify_sent"))
  return true
end

return toast
