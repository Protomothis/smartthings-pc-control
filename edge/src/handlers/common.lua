-- What every command handler shares (#129): the transition guard, the answer
-- to a command that did not go out, and the path of the v1.2.0 commands.
--
-- A command from the app is always answered on the row it came from, whatever
-- happens to it: the app spins until an event on that row arrives (platform
-- notes "상세 화면(detailView) 위젯").

local client = require "client"
local features = require "features"
local fields = require "device.fields"
local i18n = require "i18n"
local log = require "log"
local poll = require "poll"
local rows = require "device.rows"
local state = require "state"

local common = {}

--- Report a failed request on the pcInfo rows (§1). A rate-limited one (§3.1)
--- says nothing about the connection: logged, the rows stay.
function common.report_error(device, kind, body)
  local connection = poll.connection_for(kind)
  if not connection then
    log.warn(string.format("command refused (%s) on %s", tostring(kind), device.id))
    return
  end
  rows.emit_connection(device, connection, poll.message_for(kind, body, fields.lang(device)), nil, kind)
end

--- The busy value of the transition `device` is in, or nil (§6.9). While the
--- PC shuts down, goes to sleep or comes up, a second power command either
--- races the one running or lands on a PC that is gone; the app cannot grey a
--- row out, so the list says "진행 중…" and the handlers hold it back.
function common.transition_of(device)
  local s = fields.state(device)
  if not state.is_transitioning(s) then
    return nil
  end
  return state.busy_action(s)
end

--- Answer a command the transition swallowed: `answer(device)` re-sends the
--- row it arrived on, and both pcInfo rows say why until the next poll.
function common.refuse(device, busy_action, answer)
  if answer then
    answer(device)
  end
  rows.emit_note(device, i18n.busy(fields.lang(device), busy_action))
  log.info(string.format("command held back on %s: %s",
    tostring(device.id), tostring(busy_action)))
end

--- Run one v1.2.0 command (media-notify.md §3, §5).
--
-- These need what the PC may not have - a new enough service, the feature,
-- for audio and media a logged-in user - and the last status said which
-- (`features.remember`), so a command that cannot work is not sent: the row
-- is answered with its current value and both pcInfo rows say why. A refusal
-- from the service (`409 no_user_session`, `403 media_disabled`) is answered
-- the same way. A power transition holds none of them back.
-- @param answer re-sends, forced, the row the command arrived on
-- @param keys the row keys the answer poll after a success sends forced
function common.run_feature(driver, device, service_command, value, answer, keys)
  local lang = fields.lang(device)
  if fields.extras(device) == nil then
    -- Nothing read in this run yet (the hub has just restarted): ask once,
    -- rather than calling a v1.2.0 PC too old.
    poll.once(driver, device)
  end
  local refusal = features.refusal(fields.extras(device), service_command)
  if refusal then
    if answer then
      answer(device)
    end
    rows.emit_note(device, features.note_text(lang, refusal))
    log.info(string.format("%s not sent on %s: %s", tostring(service_command),
      tostring(device.id), refusal))
    return false
  end
  local ok, body, kind = client.action(device, service_command, value)
  if not ok then
    if answer then
      answer(device)
    end
    local note = features.error_note(kind, body)
    if note then
      rows.emit_note(device, i18n.t(lang, note))
      return false
    end
    common.report_error(device, kind, body)
    return false
  end
  -- Commands close together share one answer poll (poll.answer).
  poll.answer(driver, device, keys)
  return true
end

return common
