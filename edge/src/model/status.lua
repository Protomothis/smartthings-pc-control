-- Status JSON -> capability event records (design doc §3.2 -> §4). Pure:
-- nothing here emits; device/emit.lua sends the records.

local caps = require "caps"
local commands = require "model.commands"
local features = require "features"
local i18n = require "i18n"
local power = require "model.power"
local text = require "model.text"
local wolinfo = require "model.wol"

local status = {}

local function ev(list, cap, attr, value)
  list[#list + 1] = { cap = cap, attr = attr, value = value }
end

-- Every attribute the driver can emit, capability id -> attribute names.
-- capabilities_test.lua checks it against the JSON in `capabilities/`, so an
-- attribute that is emitted but never defined fails the suite. `apply_status`
-- makes all of them but the rows no status body carries: `lastAction`,
-- `planCommand`, `minutesPick`, `lastPreset` and `lastMessage`
-- (device/rows.lua).
local ATTRIBUTES = {
  [power.CAP_SWITCH] = { switch = true },
  [caps.POWER_STATE] = { powerState = true },
  [caps.COMMAND] = { lastCommand = true, lastAction = true, supportedCommands = true },
  [caps.SCHEDULE] = {
    active = true, status = true, command = true, remainingSeconds = true,
    executeAt = true, origin = true, summary = true, planCommand = true,
    minutesPick = true,
  },
  [caps.STATUS] = {
    connection = true, serviceVersion = true, updateAvailable = true,
    wolReady = true, lastSeen = true, message = true, summary = true,
    versions = true,
  },
  [caps.SESSION] = {
    locked = true, idleMinutes = true, user = true,
    summary = true, exposed = true,
  },
  -- The version row has a capability of its own so it is not drawn half-width
  -- beside `pcInfo.summary`; pcInfo still defines (and gets) `versions`.
  [caps.VERSION] = { versions = true },
  -- Standard capabilities (features.lua): not checked against a JSON file,
  -- listed so this stays "everything emitted".
  [features.CAP_VOLUME] = { volume = true },
  [features.CAP_MUTE] = { mute = true },
  [features.CAP_PLAYBACK] = { supportedPlaybackCommands = true, playbackStatus = true },
  [features.CAP_TRACK] = { supportedTrackControlCommands = true },
  [features.CAP_TRACK_DATA] = { audioTrackData = true },
  [caps.PRESET] = { lastPreset = true, names = true, supportedSlots = true },
  [caps.WATCH] = {
    summary = true, names = true,
    slotOne = true, slotTwo = true, slotThree = true, slotFour = true, slotFive = true,
  },
  [caps.TOAST] = { lastMessage = true },
  [features.CAP_BATTERY] = { battery = true },
  [features.CAP_POWER_SOURCE] = { powerSource = true },
}

--- The set above. Read-only: it is a constant, not a copy.
function status.attributes_used()
  return ATTRIBUTES
end

--- The resting value of every row a status body does not carry on its own
--- (plus the version row), for a device that has not been polled - or one
--- migrated onto new capability ids, where every attribute starts unset.
--- `lastAction` and `planCommand` are not here: device/rows.lua emits them
--- and keeps the choice.
-- @param service_version the last version a successful poll saw, or nil
-- @param last_status the last status body this run read, or nil: with one,
--   the v1.2.0 rows come from it rather than from their defaults
-- @param watch #123: the watch card's slot values to keep (`extras.watch`, or
--   what the hub last had), or nil; a slot with none reads `empty`
function status.initial_rows(lang, service_version, last_status, watch)
  local events = {}
  ev(events, caps.COMMAND, "lastCommand", text.format_last_command(nil, lang))
  -- Not transitioning, so the whole menu (an unsent `supportedValues` could
  -- take the list away entirely).
  ev(events, caps.COMMAND, "supportedCommands", commands.supported_commands(nil))
  ev(events, caps.SCHEDULE, "active", false)
  ev(events, caps.SCHEDULE, "status", commands.IDLE)
  ev(events, caps.SCHEDULE, "command", commands.NONE)
  ev(events, caps.SCHEDULE, "remainingSeconds", 0)
  ev(events, caps.SCHEDULE, "executeAt", commands.NONE)
  ev(events, caps.SCHEDULE, "origin", commands.NONE)
  ev(events, caps.SCHEDULE, "summary", text.schedule_summary(nil, lang))
  -- The delay list's one value: a list never sent does not open.
  ev(events, caps.SCHEDULE, "minutesPick", commands.MINUTES_PICK)
  -- No `update`: an offer to update can only come from a live answer.
  local versions = text.versions(service_version, lang)
  ev(events, caps.VERSION, "versions", versions)
  ev(events, caps.STATUS, "versions", versions)
  local extra_rows
  if type(last_status) == "table" then
    extra_rows = features.apply_status(last_status, { lang = lang, watch = watch, fill = true })
  else
    extra_rows = features.initial_rows(lang, watch)
  end
  for _, e in ipairs(extra_rows) do
    events[#events + 1] = e
  end
  return events
end

--- Turn a `GET /st/v1/status` body into event records (§3.2 -> §4).
--
-- `device_state` supplies powerState (already advanced with `transition`), so
-- this never decides power on its own. `opts.now` is the formatted clock time
-- for `lastSeen`, `opts.lang` the language, `opts.error` / `opts.note` feed the
-- `message` ladder - all passed in to keep this pure.
function status.apply_status(device_state, body, opts)
  device_state = device_state or power.new()
  body = body or {}
  opts = opts or {}
  local lang = opts.lang

  local events = {}
  local p = device_state.power_state or power.UNKNOWN

  ev(events, power.CAP_SWITCH, "switch", power.switch_for(p))
  ev(events, caps.POWER_STATE, "powerState", p)

  ev(events, caps.COMMAND, "lastCommand", text.format_last_command(body.last_command, lang))
  -- `device_state` already carries the pending schedule (remember_schedule),
  -- so a grace period narrows the menu here too.
  ev(events, caps.COMMAND, "supportedCommands", commands.supported_commands(device_state))

  local schedule = body.schedule or {}
  local active = schedule.active == true
  ev(events, caps.SCHEDULE, "active", active)
  ev(events, caps.SCHEDULE, "status", active and commands.SCHEDULED or commands.IDLE)
  ev(events, caps.SCHEDULE, "command", active and i18n.command(lang, schedule.command) or commands.NONE)
  ev(events, caps.SCHEDULE, "remainingSeconds",
    active and math.floor(tonumber(schedule.remaining_seconds) or 0) or 0)
  -- `execute_at` is RFC3339 with the PC's offset; the app shows the local time.
  ev(events, caps.SCHEDULE, "executeAt", active and text.hhmm(schedule.execute_at) or commands.NONE)
  ev(events, caps.SCHEDULE, "origin", active and i18n.origin(lang, schedule.origin) or commands.NONE)
  ev(events, caps.SCHEDULE, "summary", text.schedule_summary(schedule, lang))

  local update = body.update or {}
  -- One answer for `wolReady`, the summary and the message.
  local wol_off = wolinfo.wol_off(body)
  local wol_adapter = wolinfo.wol_adapter(body)

  ev(events, caps.STATUS, "connection", "ok")
  ev(events, caps.STATUS, "serviceVersion", body.service_version or "")
  ev(events, caps.STATUS, "updateAvailable", update.available == true)
  ev(events, caps.STATUS, "wolReady", not wol_off)
  ev(events, caps.STATUS, "lastSeen", opts.now or "")
  local versions = text.versions(body.service_version, lang, update)
  ev(events, caps.VERSION, "versions", versions)
  ev(events, caps.STATUS, "versions", versions)
  local message = text.status_message(body, { lang = lang, error = opts.error, note = opts.note })
  ev(events, caps.STATUS, "message", message)
  -- A successful status is always `ok` here; the failure wording is
  -- device/rows.lua's `emit_connection`.
  ev(events, caps.STATUS, "summary", text.status_summary("ok", lang, wol_off, wol_adapter,
    { uptime_seconds = body.uptime_seconds, app_update = features.needs_app_update(body) }))

  -- §3.2: the session block is opt-in. `exposed` goes out either way; the
  -- values themselves are left alone while it is off, so the tiles keep what
  -- they showed rather than a made-up "unlocked, 0 minutes, nobody".
  local session = body.session or {}
  ev(events, caps.SESSION, "exposed", session.exposed == true)
  if session.exposed == true then
    -- Absent when the service cannot read the session; false/0 is the honest
    -- default for an attribute that has no "unknown".
    ev(events, caps.SESSION, "locked", session.locked == true)
    ev(events, caps.SESSION, "idleMinutes",
      math.floor((tonumber(session.idle_seconds) or 0) / 60))
    ev(events, caps.SESSION, "user", session.user or "")
  end
  ev(events, caps.SESSION, "summary", text.session_summary(session, lang))

  -- #123: the slot values `features.remember` settled on (a list edit holds
  -- "running"); without them, the body's own.
  for _, e in ipairs(features.apply_status(body,
      { lang = lang, watch = (device_state.extras or {}).watch })) do
    events[#events + 1] = e
  end

  return events
end

return status
