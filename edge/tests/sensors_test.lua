-- The PC app v1.3.0 cards (src/sensors.lua, handlers/monitor.lua,
-- handlers/audio.lua): status -> rows, the commands and their refusals.
-- The golden bodies are contract_test.lua's; these are the edge cases.

local h = require "helpers"
local caps = require "caps"
local client = require "client"
local poll = require "poll"
local rows = require "device.rows"
local sensors = require "sensors"
local state = require "state"

local T = {}

local PRESENCE = "presence"

-- `NONE` leaves a field out (a nil would not override the default).
local NONE = {}

local function session(overrides)
  local s = { exposed = true, locked = false, idle_seconds = 0, user = "kim" }
  for k, v in pairs(overrides or {}) do
    if v == NONE then
      s[k] = nil
    else
      s[k] = v
    end
  end
  return { protocol = 1, service_version = "v1.3.0", features = {}, session = s }
end

--- What `state.apply_status` paints for `status`, as the poll does.
local function rows_of(status, opts)
  opts = opts or {}
  local s = state.new(state.ON)
  local events = state.apply_status(s, status,
    { lang = opts.lang or "ko", now = "21:00", idle_threshold = opts.idle_threshold })
  return events
end

local function value(events, component, cap, attr)
  return h.component_value(events, component, cap, attr)
end

--------------------------------------------------------------------------------
-- #151: in use, idle, locked
--------------------------------------------------------------------------------

function T.test_the_idle_threshold_preference_is_minutes_with_a_default()
  h.assert_equal(sensors.idle_threshold(nil), 10)
  h.assert_equal(sensors.idle_threshold({}), 10)
  h.assert_equal(sensors.idle_threshold({ idleThreshold = 30 }), 30)
  h.assert_equal(sensors.idle_threshold({ idleThreshold = "45" }), 45)
  h.assert_equal(sensors.idle_threshold({ idleThreshold = 0 }), 1)
  h.assert_equal(sensors.idle_threshold({ idleThreshold = 99999 }), 1440)
end

function T.test_activity_reads_the_session()
  for _, c in ipairs({
    { "a shared session in use", session({ idle_seconds = 120 }), 10, "active" },
    { "ten minutes without input", session({ idle_seconds = 600 }), 10, "idle" },
    { "the threshold is the preference", session({ idle_seconds = 1200 }), 30, "active" },
    { "locked wins over the idle time", session({ locked = true, idle_seconds = 5 }), 10, "locked" },
    { "locked without a heartbeat", session({ locked = true, idle_seconds = NONE }), 10, "locked" },
    -- §3.2: `idle_seconds` only rides with a tray heartbeat.
    { "no heartbeat", session({ idle_seconds = NONE }), 10, "unknown" },
    { "not shared", { session = { exposed = false } }, 10, "unknown" },
    { "a service without the block", {}, 10, "unknown" },
  }) do
    h.assert_equal(sensors.activity(c[2], c[3]), c[4], c[1])
  end
  h.assert_equal(sensors.activity(nil), "unknown")
end

function T.test_the_presence_rows_follow_the_preference()
  local idle = session({ idle_seconds = 1500 })
  local events = rows_of(idle)
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "activity"), "idle", "25 min, default 10")
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "idleMinutes"), 25)
  events = rows_of(idle, { idle_threshold = 30 })
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "activity"), "active", "25 min, 30 wanted")
end

function T.test_idle_minutes_follow_pc_user()
  -- 0 while shared without a reading, nothing while not shared - the routine
  -- condition keeps its value rather than reading a made-up 0 - and 0 on a
  -- repaint, so the row is never "-".
  local events = rows_of(session({ idle_seconds = NONE }))
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "idleMinutes"), 0)
  events = rows_of({ session = { exposed = false } })
  h.assert_nil(value(events, PRESENCE, caps.PRESENCE, "idleMinutes"))
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "activity"), "unknown")
  events = sensors.apply_status({ session = { exposed = false } }, { fill = true })
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "idleMinutes"), 0)
  events = sensors.initial_rows("ko")
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "activity"), "unknown")
  h.assert_equal(value(events, PRESENCE, caps.PRESENCE, "idleMinutes"), 0)
end

function T.test_a_pc_that_is_not_there_has_nobody_at_it()
  for _, power_state in ipairs({ state.OFF, state.SLEEPING, state.HIBERNATED }) do
    local device = h.fake_device({})
    local records = rows.offline_rows(device, state.new(power_state))
    h.assert_equal(value(records, PRESENCE, caps.PRESENCE, "activity"), "unknown", power_state)
    h.assert_nil(value(records, PRESENCE, caps.PRESENCE, "idleMinutes"), "the minutes stay")
  end
  local up = rows.offline_rows(h.fake_device({}), state.new(state.ON))
  h.assert_equal(#up, 0, "a PC that is on has nothing to say here")
end

function T.test_a_poll_judges_idle_by_the_devices_preference()
  local device = h.fake_device({ ipAddress = "192.168.1.20", idleThreshold = 30 })
  device.device_network_id = "pc-control-presence"
  local original = client.get_status
  client.get_status = function()
    return true, session({ idle_seconds = 1500 }), nil
  end
  local ok, err = pcall(poll.once, nil, device)
  client.get_status = original
  h.assert_true(ok, tostring(err))
  h.assert_equal(h.last_value(h.emitted(device), PRESENCE, caps.PRESENCE, "activity"), "active")
  device.preferences.idleThreshold = 20
  client.get_status = function()
    return true, session({ idle_seconds = 1500 }), nil
  end
  ok, err = pcall(poll.once, nil, device)
  client.get_status = original
  h.assert_true(ok, tostring(err))
  h.assert_equal(h.last_value(h.emitted(device), PRESENCE, caps.PRESENCE, "activity"), "idle")
end

return T
