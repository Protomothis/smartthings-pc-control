-- Every device field the driver keeps, and the helpers that read and write
-- them (#129). A field is a value the hub stores on the device object; a
-- persisted one survives a driver or hub restart (a flash write, so it is only
-- written when its value changes), the others live for one driver run.
--
-- Whether a field is persisted is a property of the field, not of the call:
-- `fields.set` looks it up in `PERSISTED`.

local state = require "state"

local fields = {}

-- The PC's runtime state (model/power.lua `new`): power state, unreachable
-- count, pending schedule, what the last status said about the v1.2.0 features.
fields.STATE = "pc_state"

-- Timers of one driver run.
fields.POLL_TIMER = "poll_timer"
fields.POLL_START_TIMER = "poll_start_timer"
fields.WAKE_TIMER = "wake_timer"
fields.PUSH_RENEW_TIMER = "push_renew_timer"
fields.REPAINT_LATE_TIMERS = "repaint_late_timers"
fields.BURST_TIMER = "burst_resync_timer"
fields.LAST_PRESET_TIMER = "last_preset_timer"

-- The PC's identity and address (design doc §6.5). `machine_id` is the
-- identity; `hostname` notices two PCs sharing one MachineGuid; the address
-- is what SSDP last said (the `ipAddress` preference wins when set).
fields.MACHINE_ID = "machine_id"
fields.HOSTNAME = "hostname"
fields.DISCOVERED_IP = "discovered_ip"
fields.DISCOVERED_PORT = "discovered_port"
fields.LAST_SSDP = "last_ssdp"
-- The short id already written into the device's `model` (once per device).
fields.MODEL_ID = "model_id"

-- What the last successful poll said, kept for while the PC is off.
fields.SERVICE_VERSION = "service_version"
fields.LAST_SEEN = "last_seen"
fields.WOL_MAC = "wol_mac"
fields.WOL_READY = "wol_ready"
fields.WOL_ADAPTER = "wol_adapter"

-- Profiles (design doc §6.6). `device.profile` does not always carry a name,
-- so the one the driver put the device on is kept as well.
fields.PROFILE_NAME = "profile_name"
fields.HAS_BATTERY = "has_battery"
-- The row generation the device was last painted for (poll.ROWS_VERSION).
fields.ROWS_PAINTED = "rows_painted"

-- Rows the user picks (device/rows.lua).
fields.LAST_ACTION = "last_action"
fields.LAST_ACTION_CONFIRM = "last_action_confirm"
fields.PLAN_COMMAND = "plan_command"
fields.LAST_PRESET = "last_preset"
fields.LAST_PRESET_AT = "last_preset_at"
fields.LAST_PRESET_CONFIRM = "last_preset_confirm"
fields.LAST_TOAST = "last_toast"

-- The emit funnel (device/emit.lua).
fields.ROWS_SENT = "rows_sent_this_run"
fields.ROWS_FORCED = "rows_forced_this_run"
fields.ROWS_SEED_OFF = "rows_seed_off"
fields.EMIT_BUDGET = "emit_budget"
fields.PAINT_QUEUE = "paint_queue"
fields.ROTATE_AT = "rotate_at"
fields.ROTATE_CURSOR = "rotate_cursor"

-- Command answers (poll.answer).
fields.ANSWER_WINDOW = "answer_window"
fields.RECENT_COMMANDS = "recent_commands"

-- The push subscription (design doc §3.5).
fields.PUSH_SUB = "push_sub"

-- The last transport failure logged for the device, "transport error: <raw>
-- -> <kind>" (client.note_transport); cleared by any HTTP answer.
fields.TRANSPORT_ERROR = "transport_error"

-- When the last `app_stop` push came, epoch seconds, while its hold window
-- runs (state `app_stopped_at`, model/power.lua `app_stop_holding`). Kept in
-- step by `set_state` - one flash write when the window starts, one when it
-- ends - so a driver restart inside the window keeps it
-- (`poll.restore_app_stop`).
fields.APP_STOP_AT = "app_stop_at"

fields.PERSISTED = {
  [fields.MACHINE_ID] = true,
  [fields.HOSTNAME] = true,
  [fields.DISCOVERED_IP] = true,
  [fields.DISCOVERED_PORT] = true,
  [fields.MODEL_ID] = true,
  [fields.SERVICE_VERSION] = true,
  [fields.LAST_SEEN] = true,
  [fields.WOL_MAC] = true,
  [fields.WOL_ADAPTER] = true,
  [fields.PROFILE_NAME] = true,
  [fields.HAS_BATTERY] = true,
  [fields.ROWS_PAINTED] = true,
  [fields.LAST_ACTION] = true,
  [fields.PLAN_COMMAND] = true,
  [fields.LAST_TOAST] = true,
  [fields.APP_STOP_AT] = true,
}

local PERSIST = { persist = true }

--- The value of field `name`, or nil (also for something that is not a device).
function fields.get(device, name)
  local value
  pcall(function() value = device:get_field(name) end)
  return value
end

--- Write field `name`, persisted when the field is (`PERSISTED`). nil clears it.
function fields.set(device, name, value)
  pcall(function()
    if fields.PERSISTED[name] then
      device:set_field(name, value, PERSIST)
    else
      device:set_field(name, value)
    end
  end)
  return value
end

--- The table in field `name`, created (and stored) when there is none.
function fields.table(device, name)
  local value = fields.get(device, name)
  if type(value) ~= "table" then
    value = {}
    fields.set(device, name, value)
  end
  return value
end

--------------------------------------------------------------------------------
-- typed helpers
--------------------------------------------------------------------------------

--- The runtime state of `device`, a fresh one when there is none yet.
function fields.state(device)
  return fields.get(device, fields.STATE) or state.new()
end

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { info = noop }
end

-- Why an `app_stop` hold window is over, read off the state that ended it.
local function hold_end_reason(s)
  if s.app_stopped == true then
    -- Only the clock ends a window and leaves the outage as it is.
    return "elapsed"
  end
  if s.last_stopping_reason ~= nil then
    return "power.stopping " .. tostring(s.last_stopping_reason)
  end
  return "PC app answered"
end

--- Store the runtime state. Its `app_stopped_at` goes to the persisted
--- `APP_STOP_AT` too when it changed (a window starting or ending - never a
--- write per poll), and a window that ends is logged once, with why.
function fields.set_state(device, s)
  fields.set(device, fields.STATE, s)
  local at = type(s) == "table" and tonumber(s.app_stopped_at) or nil
  local stored = tonumber(fields.get(device, fields.APP_STOP_AT))
  if at == stored then
    return
  end
  fields.set(device, fields.APP_STOP_AT, at)
  if stored and not at then
    logger().info(string.format("app_stop hold on %s ended (%s)",
      tostring((device or {}).id), hold_end_reason(s or {})))
  end
end

--- What the last status said about the v1.2.0 features (features.remember),
--- or nil when no status has been read in this run.
function fields.extras(device)
  return fields.state(device).extras
end

--- The `language` preference.
function fields.lang(device)
  return ((device or {}).preferences or {}).language
end

--- The last `service_version` a successful poll saw, or nil.
function fields.service_version(device)
  local seen = fields.get(device, fields.SERVICE_VERSION)
  if type(seen) == "string" and seen ~= "" then
    return seen
  end
  return nil
end

--- Remember `status.service_version`; true when it changed (written only then).
function fields.remember_service_version(device, body)
  local version = (body or {}).service_version
  if type(version) ~= "string" or version == "" or fields.service_version(device) == version then
    return false
  end
  fields.set(device, fields.SERVICE_VERSION, version)
  return true
end

--- When the PC last answered, in epoch seconds, or nil.
function fields.last_seen(device)
  local seen = tonumber(fields.get(device, fields.LAST_SEEN))
  if seen and seen > 0 then
    return seen
  end
  return nil
end

-- How stale the stored last-seen time may get before it is written again: the
-- row says whole minutes, and every write is a flash write.
fields.LAST_SEEN_STEP = 60

--- Remember that the PC answered at `now` (epoch seconds): written once the
--- stored time is `LAST_SEEN_STEP` old, or in the future (a hub clock set
--- back). True when written.
function fields.remember_last_seen(device, now)
  local seen = fields.last_seen(device)
  if seen and now >= seen and now - seen < fields.LAST_SEEN_STEP then
    return false
  end
  fields.set(device, fields.LAST_SEEN, now)
  return true
end

return fields
