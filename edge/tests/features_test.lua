-- The v1.2.0 additions (docs/design/media-notify.md): what the service's status
-- blocks become on screen, and whether - and how - a command that needs one of
-- them goes out.
--
-- The pure half is `features.lua`; the handlers are in init.lua and are driven
-- here exactly as the hub calls them, with the HTTP layer swapped for a
-- recorder (the same shape init_test.lua uses).

local h = require "helpers"
local caps = require "caps"
local client = require "client"
local features = require "features"
local i18n = require "i18n"
local poll = require "poll"
local state = require "state"

local driver = require "init"

local T = {}

--------------------------------------------------------------------------------
-- fixtures
--------------------------------------------------------------------------------

--- A v1.2.0 status body with a logged-in user.
local function status_v12(overrides)
  local status = {
    protocol = 1,
    service_version = "v1.2.0",
    features = { "audio", "media", "notify", "presets", "activity", "awake" },
    audio = { available = true, volume = 30, muted = false, device = "스피커",
      updated_at = "2026-09-30T10:00:00+09:00" },
  }
  for k, v in pairs(overrides or {}) do
    status[k] = v
  end
  return status
end

local function device_with(status)
  local device = h.fake_device({ ipAddress = "192.168.1.20", secret = "s" })
  device.device_network_id = "pc-control-features"
  local s = state.new(state.ON)
  if status ~= nil then
    features.remember(s, status)
  end
  poll.set_state(device, s)
  return device
end

local function handlers_for(id)
  local handlers = (driver.capability_handlers or {})[id]
  if not handlers then
    error("no capability handlers registered for " .. tostring(id), 0)
  end
  return handlers
end

--- Run `fn(calls)` with `client.action` and `poll.once` recorded.
-- @param opts `ok` (default true), `body`, `kind`
local function with_service(opts, fn)
  opts = opts or {}
  local calls = { actions = {}, polls = 0, poll_opts = {} }
  local original = { action = client.action, once = poll.once }
  client.action = function(_, command, value)
    calls.actions[#calls.actions + 1] = { command = command, value = value }
    if opts.ok == false then
      return false, opts.body, opts.kind
    end
    return true, opts.body or {}, nil
  end
  poll.once = function(_driver, _device, poll_opts)
    calls.polls = calls.polls + 1
    calls.poll_opts[#calls.poll_opts + 1] = poll_opts or {}
    if opts.on_poll then
      opts.on_poll(_device)
    end
    return true
  end
  local ok, err = pcall(fn, calls)
  client.action, poll.once = original.action, original.once
  if not ok then
    error(err, 0)
  end
  return calls
end

local function info_summary(device)
  return h.last_value(h.emitted(device), nil, caps.STATUS, "summary")
end

local function info_message(device)
  return h.last_value(h.emitted(device), nil, caps.STATUS, "message")
end

--------------------------------------------------------------------------------
-- pure: what the PC offers
--------------------------------------------------------------------------------

function T.test_a_service_without_features_is_older_than_v1_2()
  h.assert_false(features.parse({ service_version = "v1.1.0" }))
  h.assert_false(features.parse(nil))
  local set = features.parse({ features = { "audio", "media", "" } })
  h.assert_deep_equal(set, { audio = true, media = true })
  h.assert_deep_equal(features.parse({ features = {} }), {})
end

function T.test_remember_keeps_the_features_and_the_audio_reading()
  local s = features.remember(state.new(), status_v12())
  h.assert_true(features.has(s.extras, "audio"))
  h.assert_false(features.has(s.extras, "battery"))
  h.assert_deep_equal(s.extras.audio, { available = true, volume = 30, muted = false })
  -- A push without a status block leaves the last answer alone.
  local kept = s.extras
  features.remember(s, nil)
  h.assert_true(s.extras == kept)
end

function T.test_the_runtime_state_carries_the_extras_through_every_transition()
  local s = features.remember(state.new(state.ON), status_v12())
  for _, event in ipairs({ "status_ok", "unreachable", "switch_on", "wake_timeout", "schedule_cancelled" }) do
    s = state.transition(s, event)
    h.assert_true(s.extras ~= nil, event .. " dropped the extras")
  end
  s = state.transition(s, "stopping", "suspend")
  h.assert_true(features.has(s.extras, "media"))
end

function T.test_refusal_says_why_a_command_cannot_go_out()
  local v12 = features.remember(state.new(), status_v12()).extras
  h.assert_nil(features.refusal(v12, "volume"))
  h.assert_nil(features.refusal(v12, "play"))
  h.assert_nil(features.refusal(v12, "shutdown"), "a power command needs no feature")

  local old = features.remember(state.new(), { service_version = "v1.1.0" }).extras
  h.assert_equal(features.refusal(old, "volume"), "needs_service")
  h.assert_equal(features.refusal(old, "next"), "needs_service")

  local no_media = features.remember(state.new(), status_v12({ features = { "audio" } })).extras
  h.assert_equal(features.refusal(no_media, "pause"), "feature_missing")
  h.assert_nil(features.refusal(no_media, "mute"))

  local nobody = features.remember(state.new(),
    status_v12({ audio = { available = false, volume = 0, muted = false } })).extras
  h.assert_equal(features.refusal(nobody, "volumeup"), "no_user")
  h.assert_equal(features.refusal(nobody, "play"), "no_user")

  h.assert_equal(features.refusal(nil, "volume"), "unreachable",
    "without any status in this run there is nothing to go on")
end

function T.test_error_note_reads_the_services_codes()
  h.assert_equal(features.error_note("conflict", { error = "no_user_session" }), "no_user")
  h.assert_equal(features.error_note("conflict", nil), "no_user")
  h.assert_equal(features.error_note("forbidden", { error = "media_disabled" }), "media_disabled")
  -- A 403 without a code is the hub allow-list, which report_error explains.
  h.assert_nil(features.error_note("forbidden", { error = "forbidden" }))
  h.assert_nil(features.error_note("unreachable", nil))
end

function T.test_a_409_is_not_an_unreachable_pc()
  h.assert_equal(client.classify(409), "conflict")
end

--- Every JSON body `client.*` posted through `deps`, decoded, plus the paths.
local function recording_http(answer)
  local json = require "st.json"
  local sent = { bodies = {}, urls = {} }
  local http = function(req)
    local parts = {}
    while req.source do
      local chunk = req.source()
      if not chunk then
        break
      end
      parts[#parts + 1] = chunk
    end
    sent.bodies[#sent.bodies + 1] = json.decode(table.concat(parts))
    sent.urls[#sent.urls + 1] = req.url
    req.sink(answer or "{}")
    return 1, 200
  end
  return { http = http }, sent
end

function T.test_the_action_body_carries_a_value_only_when_given()
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  local deps, sent = recording_http()
  client.action(device, "volume", 42, deps)
  client.action(device, "volumeup", nil, deps)
  h.assert_equal(sent.urls[1], "http://192.168.1.20:5001/st/v1/command")
  h.assert_deep_equal(sent.bodies[1], { command = "volume", value = 42 })
  h.assert_deep_equal(sent.bodies[2], { command = "volumeup" }, "no value: the service steps by its default")
end

--------------------------------------------------------------------------------
-- pure: status -> rows
--------------------------------------------------------------------------------

function T.test_the_audio_block_becomes_volume_and_mute()
  local events = features.audio_events(status_v12({ audio = { available = true, volume = 72.6, muted = true } }))
  h.assert_equal(h.event_value(events, "audioVolume", "volume"), 73)
  h.assert_equal(h.event_value(events, "audioMute", "mute"), "muted")
  events = features.audio_events(status_v12())
  h.assert_equal(h.event_value(events, "audioMute", "mute"), "unmuted")
  h.assert_equal(h.event_value(events, "audioVolume", "volume"), 30)
end

function T.test_a_volume_out_of_range_is_clamped()
  h.assert_equal(features.volume_of({ volume = 140 }), 100)
  h.assert_equal(features.volume_of({ volume = -3 }), 0)
  h.assert_nil(features.volume_of({ volume = "loud" }))
end

function T.test_no_reading_paints_nothing()
  -- An old service, and a PC nobody has logged in to since boot: a volume the
  -- service never measured must not move the slider.
  h.assert_equal(#features.audio_events({ service_version = "v1.1.0" }), 0)
  h.assert_equal(#features.audio_events({ audio = { available = false, volume = 0, muted = false } }), 0)
  -- A reading that is older than the session going away is still a reading.
  local events = features.audio_events({ audio = { available = false, volume = 55, muted = false,
    updated_at = "2026-09-30T09:00:00+09:00" } })
  h.assert_equal(h.event_value(events, "audioVolume", "volume"), 55)
end

function T.test_the_media_rows_offer_play_pause_stop_and_the_two_tracks()
  local events = features.media_events()
  h.assert_deep_equal(h.event_value(events, "mediaPlayback", "supportedPlaybackCommands"),
    { "play", "pause", "stop" })
  h.assert_deep_equal(h.event_value(events, "mediaTrackControl", "supportedTrackControlCommands"),
    { "nextTrack", "previousTrack" })
  -- media-notify.md §5: the playback status is not reported (실측 대기).
  h.assert_nil(features.PLAYBACK_RESTING)
  h.assert_nil(h.event_value(events, "mediaPlayback", "playbackStatus"))
end

function T.test_a_status_and_the_initial_rows_both_carry_the_media_rows()
  local initial = state.initial_rows("ko")
  h.assert_true(h.event_value(initial, "mediaPlayback", "supportedPlaybackCommands") ~= nil)
  local polled = state.apply_status(state.new(state.ON), status_v12(), { lang = "ko" })
  h.assert_equal(h.event_value(polled, "audioVolume", "volume"), 30)
  h.assert_true(h.event_value(polled, "mediaTrackControl", "supportedTrackControlCommands") ~= nil)
end

--------------------------------------------------------------------------------
-- the handlers
--------------------------------------------------------------------------------

function T.test_every_audio_and_media_command_sends_its_service_command()
  local cases = {
    { cap = "audioVolume", command = "setVolume", args = { volume = 45 }, sends = "volume", value = 45 },
    { cap = "audioVolume", command = "volumeUp", sends = "volumeup" },
    { cap = "audioVolume", command = "volumeDown", sends = "volumedown" },
    { cap = "audioMute", command = "mute", sends = "mute" },
    { cap = "audioMute", command = "unmute", sends = "unmute" },
    { cap = "audioMute", command = "setMute", args = { state = "muted" }, sends = "mute" },
    { cap = "audioMute", command = "setMute", args = { state = "unmuted" }, sends = "unmute" },
    { cap = "mediaPlayback", command = "play", sends = "play" },
    { cap = "mediaPlayback", command = "pause", sends = "pause" },
    { cap = "mediaPlayback", command = "stop", sends = "stop" },
    { cap = "mediaPlayback", command = "setPlaybackStatus", args = { status = "paused" }, sends = "pause" },
    { cap = "mediaTrackControl", command = "nextTrack", sends = "next" },
    { cap = "mediaTrackControl", command = "previousTrack", sends = "prev" },
  }
  for _, case in ipairs(cases) do
    local device = device_with(status_v12())
    local calls = with_service(nil, function()
      handlers_for(case.cap)[case.command](driver, device, { command = case.command, args = case.args or {} })
    end)
    local where = case.cap .. "." .. case.command
    h.assert_equal(#calls.actions, 1, where .. " sent nothing")
    h.assert_equal(calls.actions[1].command, case.sends, where)
    h.assert_equal(calls.actions[1].value, case.value, where .. " value")
    h.assert_equal(calls.polls, 1, where .. " did not refresh")
    -- The poll after it answers the rows the app is watching, forced.
    h.assert_true(type(calls.poll_opts[1].force) == "table", where .. " answered no rows")
  end
end

function T.test_the_poll_after_a_volume_change_forces_the_audio_rows()
  local device = device_with(status_v12())
  local calls = with_service(nil, function()
    handlers_for("audioVolume").setVolume(driver, device, { args = { volume = 100 } })
  end)
  local rows = calls.poll_opts[1].force
  h.assert_true(rows["audioVolume.volume"] == true)
  h.assert_true(rows["audioMute.mute"] == true)
  -- ... which poll.force_rows then applies to exactly those records.
  local events = poll.force_rows(features.audio_events(status_v12()), rows)
  for _, e in ipairs(events) do
    h.assert_true(e.force == true, poll.row_key(e) .. " not forced")
  end
end

function T.test_an_old_service_gets_no_command_and_a_note()
  local device = device_with({ service_version = "v1.1.0" })
  local calls = with_service(nil, function()
    handlers_for("audioVolume").volumeUp(driver, device, { args = {} })
  end)
  h.assert_equal(#calls.actions, 0, "a v1.1.0 service does not know volumeup")
  h.assert_equal(info_summary(device), "서비스 v1.2.0 필요")
  h.assert_equal(info_message(device), "서비스 v1.2.0 필요")
  h.assert_true(h.event_forced(h.emitted(device), caps.STATUS, "summary"))
end

function T.test_no_user_session_gets_no_command()
  local device = device_with(status_v12({ audio = { available = false, volume = 20, muted = true,
    updated_at = "2026-09-30T09:00:00+09:00" } }))
  local calls = with_service(nil, function()
    handlers_for("audioVolume").setVolume(driver, device, { args = { volume = 60 } })
  end)
  h.assert_equal(#calls.actions, 0)
  h.assert_equal(info_summary(device), "사용자 없음")
  -- The slider is answered with the value it had, so it springs back.
  local emitted = h.emitted(device)
  h.assert_equal(h.last_value(emitted, nil, "audioVolume", "volume"), 20)
  h.assert_true(h.event_forced(emitted, "audioVolume", "volume"))
end

function T.test_a_409_from_the_service_says_no_user()
  local device = device_with(status_v12())
  local calls = with_service({ ok = false, kind = "conflict", body = { error = "no_user_session" } }, function()
    handlers_for("mediaPlayback").play(driver, device, { args = {} })
  end)
  h.assert_equal(#calls.actions, 1)
  h.assert_equal(calls.polls, 0)
  h.assert_equal(info_summary(device), "사용자 없음")
  -- Not an unreachable PC: the connection row is left alone.
  h.assert_nil(h.event_value(h.emitted(device), caps.STATUS, "connection"))
end

function T.test_media_disabled_on_the_pc_says_so()
  local device = device_with(status_v12())
  with_service({ ok = false, kind = "forbidden", body = { error = "media_disabled" } }, function()
    handlers_for("mediaTrackControl").nextTrack(driver, device, { args = {} })
  end)
  h.assert_equal(info_summary(device), "미디어 제어 꺼짐")
end

function T.test_a_403_without_a_code_is_still_the_allow_list()
  local device = device_with(status_v12())
  with_service({ ok = false, kind = "forbidden", body = { error = "forbidden" } }, function()
    handlers_for("audioMute").mute(driver, device, { args = {} })
  end)
  h.assert_equal(h.last_value(h.emitted(device), nil, caps.STATUS, "connection"), "unauthorized")
  h.assert_equal(info_message(device), i18n.t("ko", "forbidden"))
end

function T.test_a_command_before_any_status_asks_first()
  -- The hub restarted and nothing has been read yet: poll once, then decide.
  local device = device_with(nil)
  local calls = with_service({ on_poll = function(d)
    local s = poll.get_state(d)
    features.remember(s, status_v12())
    poll.set_state(d, s)
  end }, function()
    handlers_for("audioMute").unmute(driver, device, { args = {} })
  end)
  h.assert_equal(calls.polls, 2, "one poll to learn the features, one after the command")
  h.assert_equal(#calls.actions, 1)
  h.assert_equal(calls.actions[1].command, "unmute")
end

function T.test_a_command_to_a_pc_that_never_answered_is_not_sent()
  local device = device_with(nil)
  local calls = with_service(nil, function()
    handlers_for("mediaPlayback").pause(driver, device, { args = {} })
  end)
  h.assert_equal(#calls.actions, 0)
  h.assert_equal(info_summary(device), i18n.t("ko", "unreachable"))
end

function T.test_the_english_notes()
  local device = device_with({ service_version = "v1.1.0" })
  device.preferences.language = "en"
  with_service(nil, function()
    handlers_for("audioMute").mute(driver, device, { args = {} })
  end)
  h.assert_equal(info_summary(device), "Requires service v1.2.0")
end

--------------------------------------------------------------------------------
-- #113: presets
--------------------------------------------------------------------------------

local function with_presets(list)
  return status_v12({ presets = list })
end

local PRESETS = {
  { slot = 2, name = "방송 시작" },
  { slot = 1, name = "게임 모드" },
  { slot = 11, name = "out of range" },
  { slot = 1, name = "duplicate" },
  { name = "no slot" },
  { slot = 5, name = "" },
}

function T.test_presets_are_read_by_slot_and_malformed_ones_dropped()
  local presets = features.presets_of(with_presets(PRESETS))
  h.assert_deep_equal(presets, {
    { slot = 1, name = "게임 모드" }, { slot = 2, name = "방송 시작" }, { slot = 5, name = "" },
  })
  h.assert_deep_equal(features.presets_of({}), {})
end

function T.test_the_names_row_lists_the_presets_by_slot()
  h.assert_equal(features.preset_names(with_presets(PRESETS), "ko"), "1 게임 모드 · 2 방송 시작 · 5 이름 없음")
  h.assert_equal(features.preset_names(with_presets({}), "ko"), "없음")
  h.assert_equal(features.preset_names(with_presets({}), "en"), "None")
  h.assert_equal(features.preset_names({ service_version = "v1.1.0" }, "ko"), "서비스 v1.2.0 필요")
end

function T.test_a_long_names_row_is_cut_on_a_character()
  local list = {}
  for slot = 1, 10 do
    list[#list + 1] = { slot = slot, name = string.rep("가", 30) }
  end
  local text = features.preset_names(with_presets(list), "ko")
  local count = select(2, text:gsub("[\1-\127\194-\244][\128-\191]*", ""))
  h.assert_equal(count, features.NAMES_MAX_CHARS)
  h.assert_equal(text:sub(-3), "…")
  h.assert_equal(features.truncate("짧다", 5), "짧다")
  h.assert_equal(features.truncate("abcdef", 4), "abc…")
end

function T.test_supported_slots_is_never_empty()
  h.assert_deep_equal(features.supported_slots(with_presets(PRESETS)), { "1", "2", "5" })
  h.assert_deep_equal(features.supported_slots(with_presets({})), { "none" })
  h.assert_deep_equal(features.supported_slots({}), { "none" })
end

function T.test_the_initial_rows_rest_the_preset_list()
  local initial = state.initial_rows("ko")
  h.assert_equal(h.event_value(initial, caps.PRESET, "names"), "없음")
  h.assert_deep_equal(h.event_value(initial, caps.PRESET, "supportedSlots"), { "none" })
end

local function last_preset(device)
  return h.last_value(h.emitted(device), nil, caps.PRESET, "lastPreset")
end

function T.test_a_dismissed_preset_list_does_nothing()
  local device = device_with(with_presets(PRESETS))
  local calls = with_service(nil, function()
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "none" } })
  end)
  h.assert_equal(#calls.actions, 0)
  h.assert_equal(last_preset(device), "none")
  h.assert_true(h.event_forced(h.emitted(device), caps.PRESET, "lastPreset"),
    "the dismissed list is answered forced, or the app spins (#86)")
end

function T.test_running_a_preset_sends_its_slot_and_shows_it()
  local device = device_with(with_presets(PRESETS))
  local calls = with_service(nil, function()
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "2" } })
  end)
  h.assert_deep_equal(calls.actions, { { command = "preset", value = 2 } })
  h.assert_equal(calls.polls, 1)
  h.assert_equal(last_preset(device), "2")
  h.assert_true(h.event_forced(h.emitted(device), caps.PRESET, "lastPreset"))
  h.assert_equal(poll.shown_preset(device), "2")
end

function T.test_a_dismissed_list_resting_on_the_preset_that_just_ran_does_not_run_it_again()
  local device = device_with(with_presets(PRESETS))
  local calls = with_service(nil, function()
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "1" } })
    -- The row now rests on "1"; closing the list sends it back.
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "1" } })
  end)
  h.assert_equal(#calls.actions, 1, "the second run(1) is the dismissed picker")
  -- Another slot is a real pick.
  calls = with_service(nil, function()
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "2" } })
  end)
  h.assert_equal(#calls.actions, 1)
end

function T.test_an_empty_slot_is_not_sent()
  local device = device_with(with_presets(PRESETS))
  local calls = with_service(nil, function()
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "7" } })
  end)
  h.assert_equal(#calls.actions, 0)
  h.assert_equal(info_summary(device), "프리셋 7 비어 있음")
  h.assert_equal(last_preset(device), "none")
end

function T.test_a_preset_on_an_old_service_or_without_a_user()
  local old = device_with({ service_version = "v1.1.0" })
  local calls = with_service(nil, function()
    handlers_for(caps.PRESET).run(driver, old, { args = { slot = "1" } })
  end)
  h.assert_equal(#calls.actions, 0)
  h.assert_equal(info_summary(old), "서비스 v1.2.0 필요")

  local nobody = device_with(with_presets(PRESETS))
  with_service({ ok = false, kind = "conflict", body = { error = "no_user_session" } }, function()
    handlers_for(caps.PRESET).run(driver, nobody, { args = { slot = "1" } })
  end)
  h.assert_equal(info_summary(nobody), "사용자 없음")
  h.assert_equal(last_preset(nobody), "none", "a preset that did not start is not shown as started")
end

function T.test_the_preset_row_returns_to_none_after_the_hold()
  local device = device_with(with_presets(PRESETS))
  local now = 1000
  local deps = { now = function() return now end }
  poll.emit_preset(device, "3", true, deps)
  device.emitted = {}
  -- The poll right after the command: still showing it.
  now = 1002
  h.assert_false(poll.ensure_preset(device, deps))
  h.assert_equal(#device.emitted, 0)
  -- The next scheduled poll: back to "none", forced, and once more after.
  now = 1030
  h.assert_true(poll.ensure_preset(device, deps))
  h.assert_equal(last_preset(device), "none")
  h.assert_true(h.event_forced(h.emitted(device), caps.PRESET, "lastPreset"))
  h.assert_true(poll.ensure_preset(device, deps), "the one repeat")
  h.assert_equal(#device.emitted, 2)
  h.assert_false(poll.ensure_preset(device, deps), "and then nothing")
  h.assert_equal(#device.emitted, 2)
end

--------------------------------------------------------------------------------
-- components (#107: the emit glue)
--------------------------------------------------------------------------------

function T.test_an_event_for_another_component_goes_there()
  local device = h.fake_device({})
  poll.emit(device, { { cap = "switch", attr = "switch", value = "on", component = "awake" } })
  local emitted = h.emitted(device)
  h.assert_equal(#emitted, 1)
  h.assert_equal(emitted[1].component, "awake")
  h.assert_nil(h.event_value(emitted, "switch", "switch"), "the main switch was not touched")
  h.assert_equal(h.component_value(emitted, "awake", "switch", "switch"), "on")
end

function T.test_an_event_for_a_component_the_profile_lacks_is_skipped()
  -- A desktop's profile has no battery component, and a v1 profile no awake one.
  local device = h.fake_device({})
  poll.emit(device, { { cap = "battery", attr = "battery", value = 80, component = "battery" } })
  h.assert_equal(#h.emitted(device), 0)
  device.profile = { components = { { id = "main" } } }
  poll.emit(device, { { cap = "switch", attr = "switch", value = "on", component = "awake" } })
  h.assert_equal(#h.emitted(device), 0)
end

function T.test_the_row_key_names_the_component()
  h.assert_equal(poll.row_key({ cap = "switch", attr = "switch" }), "switch.switch")
  h.assert_equal(poll.row_key({ cap = "switch", attr = "switch", component = "main" }), "switch.switch")
  h.assert_equal(poll.row_key({ cap = "switch", attr = "switch", component = "awake" }), "awake/switch.switch")
end

return T
