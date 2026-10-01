-- #125: the /st/v1 contract, against the golden files the service's Go tests
-- write (testdata/st-v1, service/contract_golden_test.go).
--
-- The Go side generates status.*.json and push.*.json from its real handlers
-- and checks command.*.json requests with its real decoders. This side feeds
-- the same files through the driver's real paths and asserts what comes out
-- with LITERALS - never with a value read back from the fixture, or a field
-- renamed on both sides at once would still pass. A renamed field on the Go
-- side changes the golden (after `-update`) and fails here; one renamed in the
-- driver fails here against the unchanged golden.
--
--   status.full.json         state.apply_status + features.remember
--   status.minimal-1.0.json  the same, for a pre-v1.2.0 service
--   push.*.json              push.deliver (raw bytes, routing) + push.apply
--   command.*.json           the capability handlers' request bodies
--
-- testdata/st-v1/README.md says how to regenerate.

local h = require "helpers"
local Driver = require "st.driver"
local caps = require "caps"
local client = require "client"
local discovery = require "discovery"
local features = require "features"
local i18n = require "i18n"
local json = require "st.json"
local poll = require "poll"
local push = require "push"
local state = require "state"
local VERSION = require "driver_version"

-- The handlers, as the hub calls them.
local driver = require "init"
local fields = require "device.fields"

local T = {}

local LANG = "ko"
local MACHINE_ID = "4c4c4544-0042-3510-8052-b4c04f4a3732"

local function rows_of(status)
  local s = state.new(state.ON)
  state.remember_schedule(s, status)
  features.remember(s, status)
  return state.apply_status(s, status, { lang = LANG, now = "21:00" }), s
end

local function value(events, cap, attr, component)
  return h.component_value(events, component, cap, attr)
end

--------------------------------------------------------------------------------
-- status.full.json: every v1.2.0 block
--------------------------------------------------------------------------------

function T.test_full_status_core_rows()
  local status = h.fixture("status.full.json")
  local events = rows_of(status)

  h.assert_equal(value(events, caps.STATUS, "connection"), "ok")
  h.assert_equal(value(events, caps.STATUS, "serviceVersion"), "v1.2.0", "service_version")
  h.assert_true(value(events, caps.STATUS, "updateAvailable"), "update.available")
  h.assert_contains(value(events, caps.STATUS, "message"), "v1.2.1", "update.latest")
  h.assert_true(value(events, caps.STATUS, "wolReady"), "wol.selected.wol_enabled")
  h.assert_contains(value(events, caps.STATUS, "summary"), "1일", "uptime_seconds (93784s)")
  h.assert_contains(value(events, caps.COMMAND, "lastCommand"), "20:55", "last_command.at")
  h.assert_contains(value(events, caps.COMMAND, "lastCommand"), i18n.origin(LANG, "smartthings"),
    "last_command.origin")
end

function T.test_full_status_identity_and_wol()
  local status = h.fixture("status.full.json")
  h.assert_equal(status.protocol, client.PROTOCOL, "protocol")
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  poll.remember_identity(device, status)
  h.assert_equal(device:get_field(fields.MACHINE_ID), MACHINE_ID, "machine_id")
  h.assert_equal(device:get_field(fields.HOSTNAME), "GOLDEN-PC", "hostname")
  -- #96/#97: the adapter the service chose, not the driver's own guess.
  h.assert_equal(state.wol_mac(status), "B4-2E-99-45-B4-F5", "wol.selected.mac")
  h.assert_equal(state.wol_adapter(status), "이더넷", "wol.selected.name")
  h.assert_false(state.wol_off(status))
end

function T.test_full_status_schedule_rows()
  local status = h.fixture("status.full.json")
  local events, s = rows_of(status)
  h.assert_true(value(events, caps.SCHEDULE, "active"))
  h.assert_equal(value(events, caps.SCHEDULE, "status"), state.SCHEDULED)
  h.assert_equal(value(events, caps.SCHEDULE, "command"), i18n.command(LANG, "shutdown"), "schedule.command")
  h.assert_equal(value(events, caps.SCHEDULE, "remainingSeconds"), 1800, "schedule.remaining_seconds")
  h.assert_equal(value(events, caps.SCHEDULE, "executeAt"), "21:30", "schedule.execute_at")
  h.assert_equal(value(events, caps.SCHEDULE, "origin"), i18n.origin(LANG, "smartthings"), "schedule.origin")
  -- #93: grace.seconds is what tells a 30-minute schedule from a grace period.
  h.assert_equal(s.grace_seconds, 120, "grace.seconds")
  h.assert_false(state.is_grace(s))
end

function T.test_full_status_session_rows()
  local events = rows_of(h.fixture("status.full.json"))
  h.assert_true(value(events, caps.SESSION, "exposed"))
  h.assert_false(value(events, caps.SESSION, "locked"), "session.locked")
  h.assert_equal(value(events, caps.SESSION, "idleMinutes"), 12, "session.idle_seconds (754)")
  h.assert_equal(value(events, caps.SESSION, "user"), "golden", "session.user")
end

function T.test_full_status_media_rows()
  local events = rows_of(h.fixture("status.full.json"))
  h.assert_equal(value(events, features.CAP_VOLUME, "volume"), 35, "audio.volume")
  h.assert_equal(value(events, features.CAP_MUTE, "mute"), "unmuted", "audio.muted")
  h.assert_equal(value(events, features.CAP_PLAYBACK, "playbackStatus"), "playing", "media.status")
  h.assert_deep_equal(value(events, features.CAP_TRACK_DATA, "audioTrackData"),
    { title = "Hype Boy", artist = "NewJeans", album = "New Jeans" }, "media.title/artist/album")
end

function T.test_full_status_preset_awake_battery_rows()
  local events = rows_of(h.fixture("status.full.json"))
  h.assert_equal(value(events, caps.PRESET, "names"), "1 대시보드 · 3 게임 모드", "presets[].slot/name")
  h.assert_deep_equal(value(events, caps.PRESET, "supportedSlots"), { "1", "3" })
  h.assert_equal(value(events, features.CAP_SWITCH, "switch", features.AWAKE_COMPONENT), "on", "awake.on")
  h.assert_equal(value(events, features.CAP_BATTERY, "battery", features.BATTERY_COMPONENT), 76, "battery.percent")
  h.assert_equal(value(events, features.CAP_POWER_SOURCE, "powerSource", features.BATTERY_COMPONENT), "mains",
    "battery.ac")
end

function T.test_full_status_is_remembered_for_the_commands()
  local status = h.fixture("status.full.json")
  local s = features.remember(state.new(state.ON), status)
  for _, name in ipairs({ "awake", "battery", "activity", "audio", "media", "nowplaying", "notify", "presets" }) do
    h.assert_true(features.has(s.extras, name), "features lists " .. name)
  end
  h.assert_nil(features.refusal(s.extras, "volume"), "a volume command may go out")
  h.assert_true(s.extras.audio.available, "audio.available")
  h.assert_equal(s.extras.audio.volume, 35)
  h.assert_false(s.extras.audio.muted)
  h.assert_deep_equal(s.extras.preset_slots, { ["1"] = true, ["3"] = true })
  h.assert_true(s.extras.awake_on)
  h.assert_equal(s.extras.playback, "playing")
  h.assert_true(features.battery_present(status), "battery.present")
end

-- activity (#123) ---------------------------------------------------------------
-- `activity = { enabled, apps = [ { id, label, running } ], top }`: one child
-- device per app (apps.lua) and one summary row on the PC. The Go side is
-- goldenActivity* in service/contract_golden_test.go.

function T.test_full_status_activity_rows()
  local status = h.fixture("status.full.json")
  local events = rows_of(status)
  h.assert_equal(value(events, caps.APPS, "summary"), "Steam 실행 중", "activity.top / apps[].label")
  h.assert_equal(features.apps_summary(status, "en"), "Steam running")
  h.assert_nil(value(events, caps.APP, "running"), "the children's rows are not the PC's")
  h.assert_equal(features.apps_mode(status), features.APPS_ON, "activity.enabled + features")
  h.assert_deep_equal(features.apps_of(status), {
    { id = "steam.exe", label = "Steam", running = true },
    { id = "obs64.exe", label = "OBS", running = false },
  }, "activity.apps[].id/label/running, in priority order")
  local top, others = features.apps_top(status, features.apps_of(status))
  h.assert_equal(top.id, "steam.exe", "activity.top")
  h.assert_equal(others, 0)
end

function T.test_full_status_activity_children()
  -- One EDGE_CHILD per apps[] entry, keyed by `id`, labelled with `label`.
  local apps = require "apps"
  apps.reset()
  local status = h.fixture("status.full.json")
  local pc = h.fake_device({})
  pc.id = "device-golden"
  local hub = Driver("contract", {})
  hub.devices = { pc }
  local plan = apps.sync(hub, pc, status, { now = function() return 1 end })
  h.assert_equal(#plan.create, 2)
  local first, second = hub.created[1], hub.created[2]
  h.assert_equal(first.parent_assigned_child_key, "steam.exe", "apps[0].id")
  h.assert_equal(first.label, "Steam", "apps[0].label")
  h.assert_equal(second.parent_assigned_child_key, "obs64.exe", "apps[1].id")
  h.assert_equal(second.label, "OBS", "apps[1].label")
  h.assert_equal(features.app_running(features.apps_of(status)[1]), "running", "apps[0].running")
  h.assert_equal(features.app_running(features.apps_of(status)[2]), "stopped", "apps[1].running")
end

--------------------------------------------------------------------------------
-- status.minimal-1.0.json: a service older than v1.2.0
--------------------------------------------------------------------------------

function T.test_minimal_status_still_paints_the_v1_rows()
  local status = h.fixture("status.minimal-1.0.json")
  local events = rows_of(status)
  h.assert_equal(value(events, caps.STATUS, "connection"), "ok")
  h.assert_equal(value(events, caps.STATUS, "serviceVersion"), "v1.1.2")
  h.assert_false(value(events, caps.STATUS, "updateAvailable"))
  h.assert_true(value(events, caps.STATUS, "wolReady"))
  h.assert_equal(state.wol_mac(status), "B4-2E-99-45-B4-F5")
  h.assert_false(value(events, caps.SCHEDULE, "active"))
  h.assert_false(value(events, caps.SESSION, "exposed"))
  h.assert_nil(value(events, caps.SESSION, "locked"), "the session values stay untouched while not exposed")
  h.assert_contains(value(events, caps.COMMAND, "lastCommand"), i18n.command(LANG, "restart"))
  h.assert_contains(value(events, caps.COMMAND, "lastCommand"), "20:40")
end

function T.test_minimal_status_has_no_v1_2_features()
  local status = h.fixture("status.minimal-1.0.json")
  local events, s = rows_of(status)
  h.assert_false(features.parse(status), "no features key")
  h.assert_equal(features.refusal(s.extras, "volume"), "needs_service")
  h.assert_equal(features.refusal(s.extras, "preset"), "needs_service")
  h.assert_equal(features.refusal(s.extras, nil, features.NOTIFY), "needs_service")
  h.assert_equal(value(events, caps.PRESET, "names"), i18n.t(LANG, "needs_service"))
  -- #123: no watch list on a v1.1 service - the children keep what they had.
  h.assert_equal(value(events, caps.APPS, "summary"), "서비스 v1.2.0 필요")
  h.assert_equal(features.apps_mode(status), features.APPS_OLD)
  h.assert_deep_equal(features.apps_of(status), {})
  h.assert_equal(value(events, features.CAP_SWITCH, "switch", features.AWAKE_COMPONENT), "off")
  h.assert_nil(value(events, features.CAP_VOLUME, "volume"), "no reading, no slider move")
  h.assert_false(h.has_capability(events, features.CAP_BATTERY), "no battery card on a v1.0 PC")
  h.assert_false(features.battery_present(status))
end

--------------------------------------------------------------------------------
-- push.*.json
--------------------------------------------------------------------------------

local function pc_device()
  local device = h.fake_device({ ipAddress = "192.168.1.20", port = 5001, secret = "golden-secret" })
  device.device_network_id = discovery.DNI_PREFIX .. MACHINE_ID
  device.id = "device-golden"
  return device
end

-- What each push must do to the rows. `events` and `nxt` come from push.apply;
-- the routing through push.deliver is checked for every file.
local PUSHES = {
  ["push.audio.changed.json"] = function(events)
    h.assert_equal(value(events, features.CAP_VOLUME, "volume"), 50)
    h.assert_equal(value(events, features.CAP_MUTE, "mute"), "muted")
  end,
  ["push.media.changed.json"] = function(events)
    h.assert_equal(value(events, features.CAP_PLAYBACK, "playbackStatus"), "paused")
  end,
  -- activity (#123): `data` is the status block itself.
  ["push.activity.changed.json"] = function(events, nxt, event, payload)
    h.assert_equal(event, "status_ok")
    h.assert_equal(value(events, caps.APPS, "summary"), "Steam 실행 중 · 외 1개")
    h.assert_deep_equal(nxt.extras.apps, {
      { id = "steam.exe", label = "Steam", running = true },
      { id = "obs64.exe", label = "OBS", running = true },
    }, "status.activity.apps")
    local data = payload.data
    h.assert_true(data.enabled, "data.enabled is a boolean")
    h.assert_equal(data.top, "steam.exe", "data.top")
    h.assert_equal(#data.apps, 2, "data.apps")
    h.assert_equal(data.apps[1].id, "steam.exe", "data.apps[].id")
    h.assert_equal(data.apps[2].label, "OBS", "data.apps[].label")
    h.assert_true(data.apps[2].running, "data.apps[].running")
  end,
  ["push.awake.changed.json"] = function(events)
    h.assert_equal(value(events, features.CAP_SWITCH, "switch", features.AWAKE_COMPONENT), "off")
  end,
  ["push.battery.changed.json"] = function(events)
    h.assert_equal(value(events, features.CAP_BATTERY, "battery", features.BATTERY_COMPONENT), 75)
    h.assert_equal(value(events, features.CAP_POWER_SOURCE, "powerSource", features.BATTERY_COMPONENT), "battery")
  end,
  ["push.display.changed.json"] = function(events, nxt)
    -- The driver has no display row: any push is the PC answering.
    h.assert_equal(nxt.power_state, state.ON)
    h.assert_equal(value(events, caps.STATUS, "connection"), "ok")
  end,
  ["push.session.locked.json"] = function(events)
    h.assert_true(value(events, caps.SESSION, "locked"))
    h.assert_equal(value(events, caps.SESSION, "user"), "golden")
  end,
  ["push.schedule.created.json"] = function(events, nxt)
    h.assert_true(value(events, caps.SCHEDULE, "active"))
    h.assert_equal(value(events, caps.SCHEDULE, "command"), i18n.command(LANG, "restart"))
    h.assert_equal(value(events, caps.SCHEDULE, "executeAt"), "21:30")
    h.assert_true(nxt.schedule_active)
  end,
  ["push.schedule.cancelled.json"] = function(events, nxt, event)
    h.assert_equal(event, "schedule_cancelled")
    h.assert_false(value(events, caps.SCHEDULE, "active"))
    h.assert_false(nxt.schedule_active)
  end,
  ["push.power.stopping.json"] = function(_, nxt, event)
    h.assert_equal(event, "stopping")
    h.assert_equal(nxt.last_stopping_reason, "suspend", "data.reason")
    h.assert_equal(nxt.power_state, state.SLEEPING)
  end,
}

function T.test_every_push_fixture_has_a_driver_case()
  local names = h.fixture_names("^push%..+%.json$")
  h.assert_true(#names > 0, "no push fixtures found in " .. h.FIXTURE_DIR)
  for _, name in ipairs(names) do
    h.assert_true(PUSHES[name] ~= nil, name .. " has no case in contract_test.lua")
  end
  for name in pairs(PUSHES) do
    local found = false
    for _, n in ipairs(names) do
      found = found or n == name
    end
    h.assert_true(found, name .. " is missing from testdata/st-v1")
  end
end

function T.test_pushes_route_and_paint()
  for name, check in pairs(PUSHES) do
    local raw = h.read_file(h.FIXTURE_DIR .. "/" .. name)
    local payload = json.decode(raw)
    h.assert_equal(payload.type, name:match("^push%.(.+)%.json$"), name .. " type")
    h.assert_equal(payload.machine_id, MACHINE_ID, name .. " machine_id")

    -- §3.5/§6.7: the raw body, decoded and routed by machine_id.
    local device = pc_device()
    local ok, why = push.deliver(Driver("contract", {}), raw, { devices = { device } })
    h.assert_true(ok, name .. " was not delivered: " .. tostring(why))
    h.assert_equal(device.health, "online", name)

    local nxt, events, event = push.apply(state.new(state.ON), payload, { lang = LANG, now = "21:00" })
    h.assert_true(events ~= nil, name .. " carried no status")
    local check_ok, err = pcall(check, events, nxt, event, payload)
    if not check_ok then
      error(name .. ": " .. tostring(err), 0)
    end
  end
end

--------------------------------------------------------------------------------
-- command.*.json: what the handlers send
--------------------------------------------------------------------------------

local function handlers_for(id)
  local handlers = (driver.capability_handlers or {})[id]
  if not handlers then
    error("no capability handlers registered for " .. tostring(id), 0)
  end
  return handlers
end

local function commanded_device()
  local device = h.fake_device({ ipAddress = "192.168.1.20", secret = "golden-secret", awakeMinutes = 90 })
  device.device_network_id = discovery.DNI_PREFIX .. MACHINE_ID
  -- The PC is on and the last poll was status.full.json, so no command is
  -- refused before it goes out.
  poll.set_state(device, features.remember(state.new(state.ON), h.fixture("status.full.json")))
  return device
end

--- Run `fn(device)` with the HTTP layer replaced by a recorder; returns what
--- went out (`method`, `path`, `body` decoded back from the encoded JSON) and
--- what each poll was asked for. `response` is the body the service answers.
local function capture(fn, response)
  local sent, polls = {}, {}
  local original = { request = client.request, once = poll.once }
  client.request = function(_device, opts)
    local body
    if opts.body ~= nil then
      body = json.decode(json.encode(opts.body))
    end
    sent[#sent + 1] = { method = opts.method or "GET", path = client.BASE_PATH .. (opts.path or ""), body = body }
    return true, response or {}, nil
  end
  poll.once = function(_driver, _device, opts)
    polls[#polls + 1] = opts or {}
    return true
  end
  local device = commanded_device()
  local ok, err = pcall(fn, device)
  client.request, poll.once = original.request, original.once
  if not ok then
    error(err, 0)
  end
  return sent, polls
end

local SENDS = {
  ["command.shutdown.json"] = function(device)
    handlers_for("switch").off(driver, device, { command = "off", args = {} })
  end,
  ["command.schedule.json"] = function(device)
    handlers_for(caps.SCHEDULE).schedule(driver, device, { args = { minutes = "30", command = "restart" } })
  end,
  ["command.cancel.json"] = function(device)
    handlers_for(caps.SCHEDULE).cancel(driver, device, { args = {} })
  end,
  ["command.volume.json"] = function(device)
    handlers_for("audioVolume").setVolume(driver, device, { args = { volume = 30 } })
  end,
  ["command.mute.json"] = function(device)
    handlers_for("audioMute").mute(driver, device, { args = {} })
  end,
  ["command.unmute.json"] = function(device)
    handlers_for("audioMute").unmute(driver, device, { args = {} })
  end,
  ["command.next.json"] = function(device)
    handlers_for("mediaTrackControl").nextTrack(driver, device, { args = {} })
  end,
  ["command.preset.json"] = function(device)
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "3" } })
  end,
  ["command.awake.json"] = function(device)
    handlers_for("switch").on(driver, device, { command = "on", component = features.AWAKE_COMPONENT, args = {} })
  end,
  ["command.awakeoff.json"] = function(device)
    handlers_for("switch").off(driver, device, { command = "off", component = features.AWAKE_COMPONENT, args = {} })
  end,
  ["command.notify.json"] = function(device)
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "빨래가 끝났습니다" } })
  end,
  ["command.subscribe.json"] = function(device)
    client.subscribe(device, "http://192.168.1.20:39500/pc/evt", client.DEFAULT_TTL)
  end,
}

-- command.<name>.<error>.json: the same request, refused. What the driver
-- makes of the code and body (`features.error_note`, `client.classify`).
local REFUSALS = {
  ["command.volume.no-user.json"] = { same_as = "command.volume.json", kind = "conflict", note = "no_user" },
  ["command.next.media-disabled.json"] = { same_as = "command.next.json", kind = "forbidden", note = "media_disabled" },
  ["command.notify.disabled.json"] = {
    same_as = "command.notify.json", kind = "forbidden", note = "notify_disabled", notify = true,
  },
}

function T.test_every_command_fixture_has_a_driver_case()
  local names = h.fixture_names("^command%..+%.json$")
  h.assert_true(#names > 0, "no command fixtures found in " .. h.FIXTURE_DIR)
  for _, name in ipairs(names) do
    h.assert_true(SENDS[name] ~= nil or REFUSALS[name] ~= nil, name .. " has no case in contract_test.lua")
  end
end

function T.test_handlers_send_the_fixture_requests()
  for name, send in pairs(SENDS) do
    local fx = h.fixture(name)
    local sent = capture(send)
    h.assert_equal(#sent, 1, name .. ": requests sent")
    local want = fx.request
    if name == "command.subscribe.json" then
      -- The one value that follows the driver's release, not the protocol.
      want.driver_version = VERSION
    end
    h.assert_equal(sent[1].method, fx.method, name .. " method")
    h.assert_equal(sent[1].path, fx.path, name .. " path")
    h.assert_deep_equal(sent[1].body, want, name .. " body")
  end
end

function T.test_refused_requests_are_the_same_requests()
  for name, refusal in pairs(REFUSALS) do
    local fx, base = h.fixture(name), h.fixture(refusal.same_as)
    h.assert_deep_equal(fx.request, base.request, name)
    h.assert_equal(fx.path, base.path, name)
  end
end

function T.test_refusals_are_read_as_the_service_meant()
  for name, refusal in pairs(REFUSALS) do
    local fx = h.fixture(name)
    local kind = client.classify(fx.response.code)
    h.assert_equal(kind, refusal.kind, name .. " kind")
    local note = refusal.notify and features.notify_error_note(kind, fx.response.body)
      or features.error_note(kind, fx.response.body)
    h.assert_equal(note, refusal.note, name .. " note")
  end
end

function T.test_cancel_reads_the_cancelled_answer()
  local fx = h.fixture("command.cancel.json")
  h.assert_equal(fx.response.code, 200)
  local _, polls = capture(SENDS["command.cancel.json"], fx.response.body)
  h.assert_equal(polls[#polls].note, i18n.t(nil, "schedule_cancelled"), "response.body.cancelled")
end

function T.test_successful_answers_parse_as_successes()
  for name in pairs(SENDS) do
    local fx = h.fixture(name)
    h.assert_equal(fx.response.code, 200, name)
    h.assert_nil(client.classify(fx.response.code), name)
    h.assert_true(type(fx.response.body) == "table", name .. " body")
  end
end

return T
