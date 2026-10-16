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
local emit = require "device.emit"

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

--------------------------------------------------------------------------------
-- #153: the primary monitor
--------------------------------------------------------------------------------

local M = "monitor"

local function dell(overrides)
  local m = { id = "m1", name = "DELL U2720Q", primary = true, ddc = true, brightness = 40,
    input = "hdmi1", inputs = { "dp1", "hdmi1", "hdmi2" }, power = "on" }
  for k, v in pairs(overrides or {}) do
    if v == NONE then
      m[k] = nil
    else
      m[k] = v
    end
  end
  return m
end

local function monitors(list, overrides)
  local status = { protocol = 1, service_version = "v1.3.0", features = { "monitor", "audio" }, monitors = list }
  for k, v in pairs(overrides or {}) do
    status[k] = v
  end
  return status
end

function T.test_the_monitor_card_follows_the_primary_monitor()
  local tv = { id = "m2", name = "LG TV", primary = false, ddc = false, inputs = {}, power = "unknown" }
  local events = rows_of(monitors({ tv, dell() }))
  h.assert_equal(value(events, M, caps.MONITOR, "summary"), "HDMI 1 · 40% · DELL U2720Q")
  h.assert_equal(value(events, M, caps.MONITOR, "input"), "hdmi1")
  h.assert_equal(value(events, M, "switch", "switch"), "on")
  h.assert_equal(value(events, M, "switchLevel", "level"), 40)
  -- No primary flag at all: the first one.
  h.assert_equal(sensors.primary_monitor(monitors({ dell({ primary = false }), tv })).id, "m1")
end

function T.test_the_monitor_summary_says_why_there_is_nothing_to_control()
  for _, c in ipairs({
    { monitors({ dell({ power = "standby" }) }), "대기 · DELL U2720Q", "Standby · DELL U2720Q" },
    { monitors({ dell({ ddc = false, brightness = NONE, input = NONE, power = "unknown" }) }),
      "지원 안 함 (DDC/CI) · DELL U2720Q", "Not supported (DDC/CI) · DELL U2720Q" },
    { monitors({ dell({ input = "vcp:0x1b", brightness = 72.4 }) }), "vcp:0x1b · 72% · DELL U2720Q",
      "vcp:0x1b · 72% · DELL U2720Q" },
    -- Before the first read: `monitors: []`.
    { monitors({}), "모니터 정보 없음", "No monitor info" },
    -- The opt-in off: no `monitors`, no feature.
    { { service_version = "v1.3.0", features = { "audio" } }, "모니터 제어 꺼짐", "Monitor control is off" },
    { { service_version = "v1.2.0", features = { "audio" } }, "PC 앱 v1.3.0 필요", "Needs PC app v1.3.0" },
  }) do
    h.assert_equal(sensors.monitor_summary(c[1], "ko"), c[2])
    h.assert_equal(sensors.monitor_summary(c[1], "en"), c[3])
  end
  h.assert_equal(sensors.monitor_summary(nil, "ko"), "모니터 정보 없음")
end

function T.test_the_toggle_and_the_slider_move_only_on_a_reading()
  -- An opt-in switched off, a monitor without DDC/CI or one whose power is
  -- not known is not a monitor switched off: a routine on "꺼지면" must not
  -- fire. A repaint paints "off" and 0 where nothing was read ("-" else).
  for _, status in ipairs({
    { service_version = "v1.3.0", features = { "audio" } },
    monitors({}),
    monitors({ dell({ ddc = false, brightness = NONE, power = "unknown" }) }),
  }) do
    local events = rows_of(status)
    h.assert_nil(value(events, M, "switch", "switch"))
    h.assert_nil(value(events, M, "switchLevel", "level"))
    local filled = sensors.monitor_events(status, "ko", true)
    h.assert_equal(value(filled, M, "switch", "switch"), "off")
    h.assert_equal(value(filled, M, "switchLevel", "level"), 0)
    -- The range goes with every level: never a null row in the cloud.
    h.assert_deep_equal(value(filled, M, "switchLevel", "levelRange"), { minimum = 0, maximum = 100 })
  end
  h.assert_nil(value(rows_of(monitors({})), M, "switchLevel", "levelRange"), "no level, no range")
  local standby = rows_of(monitors({ dell({ power = "standby" }) }))
  h.assert_equal(value(standby, M, "switch", "switch"), "off")
  local unknown_power = rows_of(monitors({ dell({ power = "unknown" }) }))
  h.assert_nil(value(unknown_power, M, "switch", "switch"))
  h.assert_equal(value(unknown_power, M, "switchLevel", "level"), 40)
end

function T.test_inputs_without_a_name_take_the_other_places_in_order()
  local m = dell({ input = "vcp:0x1b", inputs = { "vcp:0x1b", "DP1", "composite1", "hdmi1", "vcp:0xd0", "vcp:0xd1" } })
  local map = sensors.input_map(m)
  h.assert_deep_equal(map.keys, { "other1", "dp1", "other2", "hdmi1", "other3" }, "three places, the fourth left out")
  h.assert_equal(map.raw.other1, "vcp:0x1b")
  h.assert_equal(map.raw.other2, "composite1")
  h.assert_equal(map.raw.other3, "vcp:0xd0")
  local status = monitors({ m })
  h.assert_equal(sensors.monitor_input(status), "other1")
  h.assert_deep_equal(sensors.supported_inputs(status), map.keys)
  -- The capability string could not be read: only the current input.
  local only = monitors({ dell({ inputs = {} }) })
  h.assert_deep_equal(sensors.supported_inputs(only), { "hdmi1" })
  -- Nothing to offer: never an empty list.
  h.assert_deep_equal(sensors.supported_inputs(monitors({})), { "unknown" })
  h.assert_equal(sensors.monitor_input(monitors({})), "unknown")
end

--- A PC device whose last status was `status`, its monitor card painted from
--- it, with `client.action` and `poll.once` recorded.
local function on_monitor(status, fn, answer)
  local device = h.fake_device({ ipAddress = "192.168.1.20", secret = "s" })
  device.device_network_id = "pc-control-monitor"
  local s = features.remember(state.new(state.ON), status)
  fields.set_state(device, s)
  emit.rows(device, sensors.monitor_events(status, "ko"))
  local sent = {}
  local original = { action = client.action, once = poll.once }
  client.action = function(_, command, v)
    sent[#sent + 1] = { command = command, value = v }
    if answer then
      return table.unpack(answer)
    end
    return true, {}, nil
  end
  poll.once = function() return true end
  local ok, err = pcall(fn, device)
  client.action, poll.once = original.action, original.once
  if not ok then
    error(err, 0)
  end
  return device, sent
end

function T.test_the_monitor_toggle_is_the_monitors_power_not_the_pcs()
  local _, sent = on_monitor(monitors({ dell() }), function(device)
    handlers_for("switch").off(driver, device, { command = "off", component = M, args = {} })
    handlers_for("switch").on(driver, device, { command = "on", component = M, args = {} })
  end)
  h.assert_deep_equal(sent, { { command = "monitor_power", value = "off" }, { command = "monitor_power", value = "on" } })
end

function T.test_a_monitor_that_does_not_wake_says_to_use_its_button()
  local device = on_monitor(monitors({ dell({ power = "standby" }) }), function(device)
    handlers_for("switch").on(driver, device, { command = "on", component = M, args = {} })
  end, { false, { error = "monitor_failed", message = "some monitors stop listening" }, "unreachable" })
  local emitted = h.emitted(device)
  h.assert_equal(h.last_value(emitted, nil, caps.STATUS, "summary"), "모니터 전원 버튼으로 켜세요")
  h.assert_equal(h.last_value(emitted, M, "switch", "switch"), "off", "the toggle springs back")
  h.assert_nil(h.event_value(emitted, caps.STATUS, "connection"), "the PC answered")
  device = on_monitor(monitors({ dell() }), function(d)
    handlers_for("switchLevel").setLevel(driver, d, { component = M, args = { level = 50 } })
  end, { false, { error = "monitor_failed" }, "unreachable" })
  h.assert_equal(h.last_value(h.emitted(device), nil, caps.STATUS, "summary"), "모니터가 명령을 거절함")
end

function T.test_the_brightness_is_clamped_and_rounded()
  local _, sent = on_monitor(monitors({ dell() }), function(device)
    handlers_for("switchLevel").setLevel(driver, device, { component = M, args = { level = 70.6, rate = 0 } })
    handlers_for("switchLevel").setLevel(driver, device, { component = M, args = { level = 140 } })
  end)
  h.assert_deep_equal(sent, { { command = "monitor_brightness", value = 71 }, { command = "monitor_brightness", value = 100 } })
end

function T.test_a_dismissed_input_list_does_nothing_and_other_places_send_the_service_word()
  local m = dell({ inputs = { "hdmi1", "vcp:0x1b" } })
  local device, sent = on_monitor(monitors({ m }), function(device)
    -- Closed without a pick: the row's own value comes back.
    handlers_for(caps.MONITOR).setInput(driver, device, { args = { input = "hdmi1" } })
    handlers_for(caps.MONITOR).setInput(driver, device, { args = { input = "unknown" } })
    -- USB-C on this monitor is the first input MCCS has no name for.
    handlers_for(caps.MONITOR).setInput(driver, device, { args = { input = "other1" } })
    -- A place the monitor does not fill is not sent.
    handlers_for(caps.MONITOR).setInput(driver, device, { args = { input = "other2" } })
  end)
  h.assert_deep_equal(sent, { { command = "monitor_input", value = "vcp:0x1b" } })
  local emitted = h.emitted(device)
  h.assert_true(h.component_forced(emitted, M, caps.MONITOR, "input"), "the row is answered")
  h.assert_equal(h.last_value(emitted, nil, caps.STATUS, "summary"), "이 모니터에 없는 입력")
end

function T.test_a_monitor_command_with_the_opt_in_off_is_not_sent()
  local device, sent = on_monitor({ service_version = "v1.3.0", features = { "audio" } }, function(device)
    handlers_for(caps.MONITOR).setInput(driver, device, { args = { input = "dp1" } })
  end)
  h.assert_equal(#sent, 0)
  h.assert_equal(h.last_value(h.emitted(device), nil, caps.STATUS, "summary"), "모니터 제어 꺼짐")
  h.assert_equal(h.last_value(h.emitted(device), M, caps.MONITOR, "input"), "unknown")
end

function T.test_a_pc_that_is_not_there_keeps_the_monitor_toggle()
  local records = rows.offline_rows(h.fake_device({}), state.new(state.OFF))
  h.assert_equal(value(records, M, caps.MONITOR, "summary"), "PC 꺼짐")
  h.assert_nil(value(records, M, "switch", "switch"))
  h.assert_nil(value(records, M, "switchLevel", "level"))
end

--------------------------------------------------------------------------------
-- #154: the audio device slots
--------------------------------------------------------------------------------

local A = "audio"

local function slots(outputs, inputs, overrides)
  local status = { protocol = 1, service_version = "v1.3.0",
    features = { "audio", "media", "audio_output", "audio_input" },
    audio = { available = true, volume = 30, muted = false, outputs = outputs or {}, inputs = inputs or {} } }
  for k, v in pairs(overrides or {}) do
    status[k] = v
  end
  return status
end

local SPEAKER = { slot = 1, name = "스피커 (Realtek(R) Audio)", present = true, default = true }
local HEADSET = { slot = 3, name = "헤드셋 (WH-1000XM4)", present = true, default = false }
local TV = { slot = 4, name = "LG TV (NVIDIA High Definition Audio)", present = false, default = false }

function T.test_the_names_row_names_each_slot_shortly()
  h.assert_equal(sensors.audio_names(slots({ SPEAKER, HEADSET, TV }), "output", "ko"), "1 스피커 · 3 헤드셋 · 4 LG TV")
  -- Two devices of the same kind keep their adapters to stay apart.
  local usb = { slot = 2, name = "스피커 (USB Audio)", present = true, default = false }
  h.assert_equal(sensors.audio_names(slots({ SPEAKER, usb }), "output", "ko"),
    "1 스피커 (Realtek… · 2 스피커 (USB Aud…")
  -- Malformed entries are left out; the list is by slot.
  local odd = { { slot = 9, name = "x" }, { slot = 2, name = "" }, HEADSET, { slot = 3, name = "again" }, SPEAKER }
  h.assert_equal(sensors.audio_names(slots(odd), "output", "en"), "1 스피커 · 3 헤드셋")
  h.assert_equal(sensors.audio_names(slots({}), "output", "ko"), "없음")
  h.assert_equal(sensors.audio_names({ service_version = "v1.2.0", features = { "audio" } }, "input", "en"),
    "Needs PC app v1.3.0")
  h.assert_equal(sensors.audio_names(nil, "output", "ko"), "없음")
end

function T.test_the_list_rests_on_the_default_devices_slot()
  local events = rows_of(slots({ SPEAKER, HEADSET }, { { slot = 2, name = "마이크", default = true } }))
  h.assert_equal(value(events, A, caps.AUDIO_OUT, "output"), "1")
  h.assert_deep_equal(value(events, A, caps.AUDIO_OUT, "supportedOutputs"), { "1", "3" })
  h.assert_equal(value(events, A, caps.AUDIO_IN, "input"), "2")
  -- The default device is in no slot (switched on the PC itself).
  local other = { slot = 1, name = "스피커", present = true, default = false }
  events = rows_of(slots({ other }))
  h.assert_equal(value(events, A, caps.AUDIO_OUT, "output"), "none")
  h.assert_deep_equal(value(events, A, caps.AUDIO_IN, "supportedInputs"), { "none" }, "never empty")
end

--- A PC device whose last status was `status`, its audio card painted from
--- it, with `client.action` and `poll.once` recorded.
local function on_audio(status, fn, answer)
  local device = h.fake_device({ ipAddress = "192.168.1.20", secret = "s" })
  device.device_network_id = "pc-control-audio"
  fields.set_state(device, features.remember(state.new(state.ON), status))
  emit.rows(device, sensors.audio_events(status, "output", "ko"))
  emit.rows(device, sensors.audio_events(status, "input", "ko"))
  local sent = {}
  local original = { action = client.action, once = poll.once }
  client.action = function(_, command, v)
    sent[#sent + 1] = { command = command, value = v }
    if answer then
      return table.unpack(answer)
    end
    return true, {}, nil
  end
  poll.once = function() return true end
  local ok, err = pcall(fn, device)
  client.action, poll.once = original.action, original.once
  if not ok then
    error(err, 0)
  end
  return device, sent
end

local function summary_of(device)
  return h.last_value(h.emitted(device), nil, caps.STATUS, "summary")
end

function T.test_switching_sends_the_slot_and_a_dismissed_list_does_nothing()
  local device, sent = on_audio(slots({ SPEAKER, HEADSET }, { { slot = 2, name = "마이크", default = true } }),
    function(device)
      handlers_for(caps.AUDIO_OUT).setOutput(driver, device, { args = { slot = "1" } })
      handlers_for(caps.AUDIO_OUT).setOutput(driver, device, { args = { slot = "none" } })
      handlers_for(caps.AUDIO_OUT).setOutput(driver, device, { args = { slot = "3" } })
      handlers_for(caps.AUDIO_IN).setInput(driver, device, { args = { slot = "2" } })
    end)
  h.assert_deep_equal(sent, { { command = "audio_output", value = 3 } })
  h.assert_true(h.component_forced(h.emitted(device), A, caps.AUDIO_OUT, "output"), "the row is answered")
end

function T.test_an_empty_slot_is_not_sent()
  local device, sent = on_audio(slots({ SPEAKER, HEADSET }), function(device)
    handlers_for(caps.AUDIO_OUT).setOutput(driver, device, { args = { slot = "2" } })
  end)
  h.assert_equal(#sent, 0)
  h.assert_equal(summary_of(device), "출력 2 비어 있음")
  h.assert_equal(h.last_value(h.emitted(device), A, caps.AUDIO_OUT, "output"), "1", "the list goes back")
  -- The PC app lists audio_input only while it has an input slot.
  device, sent = on_audio(slots({ SPEAKER }, {}, { features = { "audio", "media", "audio_output" } }), function(d)
    handlers_for(caps.AUDIO_IN).setInput(driver, d, { args = { slot = "5" } })
  end)
  h.assert_equal(#sent, 0)
  h.assert_equal(summary_of(device), "입력 칸 없음 · PC 앱에서 설정", "media control on, no input slot")
end

function T.test_the_services_refusals_name_the_slot()
  for _, c in ipairs({
    { { error = "device_missing", message = "LG TV is not connected" }, "conflict", "출력 4 연결 안 됨" },
    { { error = "switch_failed" }, "unreachable", "Windows가 장치를 못 바꿈" },
    { { error = "no_user_session" }, "conflict", "사용자 없음" },
    { { error = "slot_empty" }, "incompatible", "출력 4 비어 있음" },
  }) do
    local device, sent = on_audio(slots({ SPEAKER, HEADSET, TV }), function(device)
      handlers_for(caps.AUDIO_OUT).setOutput(driver, device, { args = { slot = "4" } })
    end, { false, c[1], c[2] })
    h.assert_deep_equal(sent, { { command = "audio_output", value = 4 } }, c[3])
    h.assert_equal(summary_of(device), c[3])
    h.assert_nil(h.event_value(h.emitted(device), caps.STATUS, "connection"), c[3] .. ": the PC answered")
  end
end

function T.test_a_switch_the_pc_cannot_do_is_not_sent()
  for _, c in ipairs({
    { slots({ SPEAKER, HEADSET }, nil, { features = { "awake" } }), "미디어 제어 꺼짐" },
    { slots({ SPEAKER, HEADSET }, nil, { service_version = "v1.2.0", features = { "audio", "media" } }),
      "PC 앱 v1.3.0 필요" },
    { slots({ SPEAKER, HEADSET }, nil, { audio = { available = false, outputs = { SPEAKER, HEADSET } } }),
      "사용자 없음" },
  }) do
    local device, sent = on_audio(c[1], function(device)
      handlers_for(caps.AUDIO_OUT).setOutput(driver, device, { args = { slot = "3" } })
    end)
    h.assert_equal(#sent, 0, c[2])
    h.assert_equal(summary_of(device), c[2])
  end
end

return T
