-- The PC app v1.3.0 cards (src/sensors.lua, handlers/monitor.lua,
-- handlers/audio.lua): status -> rows, the commands and their refusals.
-- The golden bodies are contract_test.lua's; these are the edge cases.

local h = require "helpers"
local caps = require "caps"
local client = require "client"
local poll = require "poll"
local rows = require "device.rows"
local push = require "push"
local sensors = require "sensors"
local state = require "state"
local fields = require "device.fields"
local features = require "features"

-- The handlers, as the hub calls them.
local driver = require "init"

local function handlers_for(id)
  local handlers = (driver.capability_handlers or {})[id]
  if not handlers then
    error("no capability handlers registered for " .. tostring(id), 0)
  end
  return handlers
end

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

--------------------------------------------------------------------------------
-- #152: the camera and the microphone
--------------------------------------------------------------------------------

local function privacy(camera, microphone, overrides)
  local status = { protocol = 1, service_version = "v1.3.0", features = { "privacy" },
    privacy = { camera = camera, microphone = microphone, since = "" } }
  for k, v in pairs(overrides or {}) do
    status[k] = v
  end
  return status
end

function T.test_the_privacy_rows_say_what_is_in_use()
  for _, c in ipairs({
    { privacy("in_use", "idle"), "inUse", "idle", "카메라 사용 중", "Camera in use" },
    { privacy("idle", "in_use"), "idle", "inUse", "마이크 사용 중", "Mic in use" },
    { privacy("in_use", "in_use"), "inUse", "inUse", "카메라·마이크 사용 중", "Camera & mic in use" },
    { privacy("idle", "idle"), "idle", "idle", "사용 안 함", "Not in use" },
    -- Nobody logged in: the store cannot be read.
    { privacy("unknown", "unknown"), "unknown", "unknown", "알 수 없음", "Unknown" },
    { privacy("unknown", "in_use"), "unknown", "inUse", "마이크 사용 중", "Mic in use" },
    { privacy("what", "idle"), "unknown", "idle", "알 수 없음", "Unknown" },
    -- The opt-in off: no block, no feature (and `privacy.changed` data null).
    { { service_version = "v1.3.0", features = { "audio" } }, "unknown", "unknown", "감지 꺼짐", "Detection off" },
    { privacy("in_use", "idle", { features = { "audio" } }), "unknown", "unknown", "감지 꺼짐", "Detection off" },
    -- An older PC app has no such thing.
    { { service_version = "v1.2.0", features = { "audio" } }, "unknown", "unknown",
      "PC 앱 v1.3.0 필요", "Needs PC app v1.3.0" },
    { { service_version = "v1.1.2" }, "unknown", "unknown", "PC 앱 v1.3.0 필요", "Needs PC app v1.3.0" },
  }) do
    local status, camera, microphone, ko, en = table.unpack(c, 1, 5)
    local events = rows_of(status)
    h.assert_equal(value(events, PRESENCE, caps.PRIVACY, "camera"), camera, ko)
    h.assert_equal(value(events, PRESENCE, caps.PRIVACY, "microphone"), microphone, ko)
    h.assert_equal(value(events, PRESENCE, caps.PRIVACY, "summary"), ko)
    h.assert_equal(sensors.privacy_summary(status, "en"), en)
  end
  -- Before any status: unknown, never "".
  local initial = sensors.initial_rows("ko")
  h.assert_equal(value(initial, PRESENCE, caps.PRIVACY, "summary"), "알 수 없음")
  h.assert_equal(value(initial, PRESENCE, caps.PRIVACY, "camera"), "unknown")
end

function T.test_a_privacy_push_with_no_data_turns_both_unknown()
  -- The opt-in switched off: `data: null`, and the status lost its block.
  local payload = { protocol = 1, type = "privacy.changed", data = nil,
    status = { protocol = 1, service_version = "v1.3.0", features = { "audio" } } }
  local _, events, event = push.apply(state.new(state.ON), payload, { lang = "ko", now = "21:00" })
  h.assert_equal(event, "status_ok")
  h.assert_equal(value(events, PRESENCE, caps.PRIVACY, "camera"), "unknown")
  h.assert_equal(value(events, PRESENCE, caps.PRIVACY, "microphone"), "unknown")
  h.assert_equal(value(events, PRESENCE, caps.PRIVACY, "summary"), "감지 꺼짐")
end

function T.test_a_pc_that_is_not_there_has_no_camera_in_use()
  local device = h.fake_device({ language = "en" })
  local records = rows.offline_rows(device, state.new(state.OFF))
  h.assert_equal(value(records, PRESENCE, caps.PRIVACY, "camera"), "unknown")
  h.assert_equal(value(records, PRESENCE, caps.PRIVACY, "microphone"), "unknown")
  h.assert_equal(value(records, PRESENCE, caps.PRIVACY, "summary"), "PC off")
end

--- A PC device whose last status was `status`, with `client.command` answering
--- `409 device_in_use`.
local function refused_power(fn)
  local device = h.fake_device({ ipAddress = "192.168.1.20", secret = "s" })
  device.device_network_id = "pc-control-in-use"
  fields.set_state(device, features.remember(state.new(state.ON), privacy("in_use", "idle")))
  local original = { command = client.command, once = poll.once }
  local sent = {}
  client.command = function(_, command)
    sent[#sent + 1] = command
    return false, { error = "device_in_use", devices = { "camera" } }, "conflict"
  end
  poll.once = function() return true end
  local ok, err = pcall(fn, device)
  client.command, poll.once = original.command, original.once
  if not ok then
    error(err, 0)
  end
  return device, sent
end

function T.test_a_shutdown_held_back_for_the_camera_says_so()
  -- #152: the PC app's guard on "block". The switch springs back on and both
  -- pcInfo rows say why until the next poll.
  local device, sent = refused_power(function(device)
    handlers_for("switch").off(driver, device, { command = "off", args = {} })
  end)
  h.assert_deep_equal(sent, { "shutdown" })
  local emitted = h.emitted(device)
  h.assert_equal(h.last_value(emitted, nil, caps.STATUS, "summary"), "카메라 사용 중 · 실행 안 함")
  h.assert_equal(h.last_value(emitted, nil, caps.STATUS, "message"), "카메라 사용 중 · 실행 안 함")
  h.assert_equal(h.last_value(emitted, nil, "switch", "switch"), "on")
  h.assert_true(h.event_forced(emitted, "switch", "switch"), "the toggle's answer is forced")
  h.assert_nil(h.event_value(emitted, caps.STATUS, "connection"), "the PC is fine")
end

function T.test_a_command_list_restart_held_back_names_both_devices()
  local device = refused_power(function(device)
    client.command = function()
      return false, { error = "device_in_use", devices = { "camera", "microphone" } }, "conflict"
    end
    handlers_for(caps.COMMAND).execute(driver, device, { args = { command = "restart" } })
  end)
  h.assert_equal(h.last_value(h.emitted(device), nil, caps.STATUS, "summary"),
    "카메라·마이크 사용 중 · 실행 안 함")
  h.assert_equal(sensors.in_use_note("en", { "microphone" }), "Mic in use · not run")
  h.assert_equal(sensors.in_use_note("ko", {}), "카메라·마이크 사용 중 · 실행 안 함")
end

return T
