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
-- #114: activity
--------------------------------------------------------------------------------

local function with_activity(block, list)
  local status = status_v12({ activity = block })
  if list then
    status.features = list
  end
  return status
end

function T.test_activity_reads_the_kind_and_the_labels()
  local status = with_activity({ enabled = true, kind = "game", labels = { "Steam" } })
  local events = features.activity_events(status, "ko")
  h.assert_equal(h.event_value(events, caps.ACTIVITY, "activity"), "game")
  h.assert_equal(h.event_value(events, caps.ACTIVITY, "summary"), "게임 중 · Steam")
  h.assert_equal(features.activity_summary(status, "en"), "Gaming · Steam")
end

function T.test_nothing_running_is_none_and_the_opt_in_off_is_off()
  local idle = with_activity({ enabled = true, kind = "none", labels = {} })
  h.assert_equal(features.activity_kind(idle), "none")
  h.assert_equal(features.activity_summary(idle, "ko"), "없음")
  -- The service lists "activity" only while the opt-in is on (#110).
  local off = with_activity({ enabled = false, kind = "none", labels = {} },
    { "audio", "media", "awake" })
  h.assert_equal(features.activity_kind(off), "none")
  h.assert_equal(features.activity_summary(off, "ko"), "꺼짐")
  -- A block that claims to be on without the feature is not trusted either.
  local stray = with_activity({ enabled = true, kind = "game", labels = { "Steam" } }, { "audio" })
  h.assert_equal(features.activity_summary(stray, "ko"), "꺼짐")
  h.assert_equal(features.activity_summary({ service_version = "v1.1.0" }, "ko"), "꺼짐")
end

function T.test_an_unknown_kind_is_other()
  local status = with_activity({ enabled = true, kind = "vr", labels = { "SteamVR" } })
  h.assert_equal(features.activity_kind(status), "other")
  h.assert_equal(features.activity_summary(status, "ko"), "실행 중 · SteamVR")
end

function T.test_a_long_activity_line_drops_labels_until_it_fits()
  local many = with_activity({ enabled = true, kind = "work",
    labels = { "VS Code", "Figma", "Slack" } })
  h.assert_equal(features.activity_summary(many, "ko"), "작업 중 · VS Code 외 2")
  -- 25 code points with the one label: the word alone.
  local long = with_activity({ enabled = true, kind = "work", labels = { "Visual Studio Code" } })
  h.assert_equal(features.activity_summary(long, "ko"), "작업 중")
  local two = with_activity({ enabled = true, kind = "stream", labels = { "OBS", "Discord" } })
  h.assert_equal(features.activity_summary(two, "ko"), "방송 중 · OBS, Discord")
  local huge = with_activity({ enabled = true, kind = "media",
    labels = { string.rep("가", 40), "VLC" } })
  h.assert_equal(features.activity_summary(huge, "ko"), "감상 중")
  local fits = with_activity({ enabled = true, kind = "game",
    labels = { "Steam", "Battle.net", "Epic Games" } })
  h.assert_equal(features.activity_summary(fits, "en"), "Gaming · Steam +2")
end

function T.test_an_activity_push_repaints_at_once()
  -- activity.changed carries the whole status like every push (#110), so it
  -- goes through the same apply_status as a poll.
  local push = require "push"
  local status = with_activity({ enabled = true, kind = "game", labels = { "Steam" } })
  local _, events = push.apply(state.new(state.ON),
    { type = "activity.changed", status = status }, { lang = "ko" })
  h.assert_equal(h.event_value(events, caps.ACTIVITY, "activity"), "game")
  h.assert_equal(h.event_value(events, caps.ACTIVITY, "summary"), "게임 중 · Steam")
end

--------------------------------------------------------------------------------
-- #115: keep-awake
--------------------------------------------------------------------------------

function T.test_the_awake_switch_follows_the_status_on_its_component()
  local on = features.awake_events(status_v12({ awake = { on = true, until_ = "" } }))
  h.assert_equal(h.component_value(on, "awake", "switch", "switch"), "on")
  h.assert_nil(h.event_value(on, "switch", "switch"), "not the main switch")
  local off = features.awake_events(status_v12({ awake = { on = false, ["until"] = "" } }))
  h.assert_equal(h.component_value(off, "awake", "switch", "switch"), "off")
  h.assert_equal(h.component_value(features.awake_events({}), "awake", "switch", "switch"), "off",
    "an old service cannot keep the PC awake")
  h.assert_equal(h.component_value(state.initial_rows("ko"), "awake", "switch", "switch"), "off")
end

function T.test_awake_minutes_come_from_the_preference()
  h.assert_equal(features.awake_minutes({}), 60)
  h.assert_equal(features.awake_minutes({ awakeMinutes = 0 }), 0, "0 = until switched off")
  h.assert_equal(features.awake_minutes({ awakeMinutes = 90 }), 90)
  h.assert_equal(features.awake_minutes({ awakeMinutes = 5000 }), 1440)
  h.assert_equal(features.awake_minutes({ awakeMinutes = -3 }), 0)
end

local function switch_command(name, component)
  return { capability = "switch", command = name, component = component, args = {} }
end

function T.test_the_awake_switch_sends_awake_and_awakeoff()
  local device = device_with(status_v12({ awake = { on = false } }))
  device.preferences.awakeMinutes = 30
  local calls = with_service(nil, function()
    handlers_for("switch").on(driver, device, switch_command("on", "awake"))
    handlers_for("switch").off(driver, device, switch_command("off", "awake"))
  end)
  h.assert_deep_equal(calls.actions, {
    { command = "awake", value = 30 }, { command = "awakeoff" },
  })
  h.assert_equal(calls.polls, 2)
  h.assert_true(calls.poll_opts[1].force["awake/switch.switch"] == true,
    "the poll after it answers the awake row")
  -- The PC's power was not touched: no wake, no shutdown.
  h.assert_equal(poll.get_state(device).power_state, state.ON)
end

function T.test_the_default_period_is_sent_as_sixty_minutes()
  local device = device_with(status_v12({ awake = { on = false } }))
  local calls = with_service(nil, function()
    handlers_for("switch").on(driver, device, switch_command("on", "awake"))
  end)
  h.assert_deep_equal(calls.actions, { { command = "awake", value = 60 } })
end

function T.test_the_main_switch_off_is_still_the_power_command()
  -- The same capability handler serves both components.
  local device = device_with(status_v12())
  local sent = {}
  local original = client.command
  client.command = function(_, command) sent[#sent + 1] = command; return true, {}, nil end
  local ok, err = pcall(function()
    with_service(nil, function()
      handlers_for("switch").off(driver, device, switch_command("off", "main"))
      handlers_for("switch").off(driver, device, switch_command("off", nil))
    end)
  end)
  client.command = original
  if not ok then
    error(err, 0)
  end
  h.assert_deep_equal(sent, { "shutdown", "shutdown" })
end

function T.test_an_old_service_springs_the_awake_toggle_back()
  local device = device_with({ service_version = "v1.1.0" })
  local calls = with_service(nil, function()
    handlers_for("switch").on(driver, device, switch_command("on", "awake"))
  end)
  h.assert_equal(#calls.actions, 0)
  local emitted = h.emitted(device)
  h.assert_equal(h.last_value(emitted, "awake", "switch", "switch"), "off")
  h.assert_true(h.component_forced(emitted, "awake", "switch", "switch"))
  h.assert_equal(info_summary(device), "서비스 v1.2.0 필요")
end

function T.test_an_awake_push_moves_the_switch_at_once()
  local push = require "push"
  local status = status_v12({ awake = { on = true, ["until"] = "2026-09-30T12:00:00+09:00" } })
  local nxt, events = push.apply(state.new(state.ON), { type = "awake.changed", status = status }, {})
  h.assert_equal(h.component_value(events, "awake", "switch", "switch"), "on")
  h.assert_true(nxt.extras.awake_on)
  -- ... and the glue puts it on the awake component of the device.
  local device = h.fake_device({})
  poll.emit(device, events)
  h.assert_equal(h.component_value(h.emitted(device), "awake", "switch", "switch"), "on")
  h.assert_equal(h.event_value(h.emitted(device), "switch", "switch"), "on",
    "the main switch comes from the power state (on)")
end

--------------------------------------------------------------------------------
-- #116: battery
--------------------------------------------------------------------------------

local profiles = require "profiles"

local function laptop(percent, ac)
  return status_v12({ battery = { present = true, percent = percent, charging = false, ac = ac } })
end

local DESKTOP = status_v12({ battery = { present = false, percent = -1, charging = false, ac = false } })

function T.test_a_laptop_reports_its_battery_on_the_battery_component()
  local events = features.battery_events(laptop(81, false))
  h.assert_equal(h.component_value(events, "battery", "battery", "battery"), 81)
  h.assert_equal(h.component_value(events, "battery", "powerSource", "powerSource"), "battery")
  events = features.battery_events(laptop(100, true))
  h.assert_equal(h.component_value(events, "battery", "powerSource", "powerSource"), "mains")
end

function T.test_an_unknown_percent_is_left_out()
  local events = features.battery_events(laptop(-1, true))
  h.assert_nil(h.component_value(events, "battery", "battery", "battery"))
  h.assert_equal(h.component_value(events, "battery", "powerSource", "powerSource"), "mains")
end

function T.test_a_desktop_reports_nothing()
  h.assert_equal(#features.battery_events(DESKTOP), 0)
  h.assert_equal(#features.battery_events({ service_version = "v1.1.0" }), 0)
end

local function device_on_profile(name, id)
  local device = device_with(status_v12())
  device.id = id or ("battery-" .. name)
  device:set_field(profiles.FIELD, name)
  device.profile = { id = "abc", name = name, components = h.components_for(name) }
  return device
end

function T.test_two_statuses_with_a_battery_move_a_desktop_profile()
  profiles.reset()
  local device = device_on_profile("pc-tv.v2")
  h.assert_nil(profiles.apply_battery(device, true), "one status is not enough")
  h.assert_equal(profiles.apply_battery(device, true), "pc-tv-battery.v2", "the style is kept")
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-tv-battery.v2" } })
  h.assert_true(profiles.has_battery(device), "persisted for the next migration")
  -- Settled: more of the same asks for nothing.
  h.assert_nil(profiles.apply_battery(device, true))
  h.assert_equal(#device.metadata_updates, 1)
end

function T.test_and_two_without_one_move_it_back()
  profiles.reset()
  local device = device_on_profile("pc-battery.v2")
  h.assert_nil(profiles.apply_battery(device, false))
  h.assert_equal(profiles.apply_battery(device, false), "pc.v2")
  h.assert_false(profiles.has_battery(device))
end

function T.test_a_flapping_reading_moves_nothing()
  profiles.reset()
  local device = device_on_profile("pc.v2")
  for _, present in ipairs({ true, false, true, false, true }) do
    h.assert_nil(profiles.apply_battery(device, present))
  end
  h.assert_equal(#device.metadata_updates, 0)
end

function T.test_a_device_on_an_old_profile_is_left_to_ensure()
  profiles.reset()
  local device = device_on_profile("pc.v1")
  profiles.apply_battery(device, true)
  h.assert_nil(profiles.apply_battery(device, true))
  h.assert_equal(#device.metadata_updates, 0)
end

function T.test_a_refused_battery_switch_is_not_asked_again_this_run()
  profiles.reset()
  local device = device_on_profile("pc.v2")
  local tries = 0
  function device:try_update_metadata()
    tries = tries + 1
    error("no such profile", 0)
  end
  profiles.apply_battery(device, true)
  h.assert_nil(profiles.apply_battery(device, true))
  profiles.apply_battery(device, true)
  profiles.apply_battery(device, true)
  h.assert_equal(tries, 1)
end

function T.test_a_later_migration_lands_a_laptop_on_its_battery_profile()
  -- The persisted answer is what `ensure` reads (v2 -> v3 one day).
  profiles.reset()
  local device = device_on_profile("pc-hub.v1", "laptop-v1")
  device:set_field(profiles.BATTERY_FIELD, true)
  h.assert_equal(profiles.ensure(device), "pc-hub-battery.v2")
end

function T.test_a_poll_follows_the_battery_and_repaints_after()
  profiles.reset()
  local device = device_on_profile("pc.v2", "polled-laptop")
  local fake = { timers = {} }
  function fake:call_with_delay(delay, fn, name)
    self.timers[#self.timers + 1] = { delay = delay, fn = fn, name = name }
  end
  h.assert_nil(poll.follow_battery(fake, device, laptop(50, false)))
  h.assert_equal(poll.follow_battery(fake, device, laptop(49, false)), "pc-battery.v2")
  h.assert_equal(#fake.timers, 1)
  h.assert_equal(fake.timers[1].name, "battery-profile")
end

function T.test_a_battery_push_reaches_the_battery_component()
  local push = require "push"
  local _, events = push.apply(state.new(state.ON),
    { type = "battery.changed", status = laptop(15, false) }, {})
  h.assert_equal(h.component_value(events, "battery", "battery", "battery"), 15)
  local device = device_on_profile("pc-battery.v2", "pushed-laptop")
  poll.emit(device, events)
  h.assert_equal(h.component_value(h.emitted(device), "battery", "battery", "battery"), 15)
  -- The same events on a desktop's profile go nowhere.
  local desktop = device_on_profile("pc.v2", "pushed-desktop")
  poll.emit(desktop, events)
  h.assert_nil(h.component_value(h.emitted(desktop), "battery", "battery", "battery"))
end

--------------------------------------------------------------------------------
-- #108: PC notifications
--------------------------------------------------------------------------------

--- Run `fn(sent)` with `client.notify` recorded.
local function with_notify(opts, fn)
  opts = opts or {}
  local sent = {}
  local original_notify, original_once = client.notify, poll.once
  client.notify = function(_, text, speak)
    sent[#sent + 1] = { text = text, speak = speak }
    if opts.ok == false then
      return false, opts.body, opts.kind
    end
    return true, { ok = true }, nil
  end
  poll.once = function() return true end
  local ok, err = pcall(fn, sent)
  client.notify, poll.once = original_notify, original_once
  if not ok then
    error(err, 0)
  end
  return sent
end

function T.test_the_text_is_cleaned_and_cut_to_the_services_limit()
  h.assert_equal(features.notify_text("  세탁 끝\n\t남은 시간 0분  "), "세탁 끝 남은 시간 0분")
  h.assert_equal(features.notify_text("bell\7 here\127"), "bell here")
  h.assert_nil(features.notify_text("   \n  "))
  h.assert_nil(features.notify_text(nil))
  local long = features.notify_text(string.rep("가", 250))
  local count = select(2, long:gsub("[\1-\127\194-\244][\128-\191]*", ""))
  h.assert_equal(count, 200)
  h.assert_equal(long:sub(-3), "…")
end

function T.test_a_device_notification_is_a_toast_and_speak_reads_it_aloud()
  local device = device_with(status_v12())
  local sent = with_notify(nil, function()
    handlers_for("notification").deviceNotification(driver, device,
      { args = { notification = "현관문이 열렸습니다" } })
    handlers_for("speechSynthesis").speak(driver, device, { args = { phrase = "저녁 먹자" } })
  end)
  h.assert_deep_equal(sent, {
    { text = "현관문이 열렸습니다", speak = false },
    { text = "저녁 먹자", speak = true },
  })
end

function T.test_the_notify_body_has_no_title_and_speak_only_when_asked()
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  local deps, sent = recording_http('{"ok":true}')
  client.notify(device, "안녕", false, deps)
  client.notify(device, "안녕", true, deps)
  h.assert_equal(sent.urls[1], "http://192.168.1.20:5001/st/v1/notify")
  h.assert_deep_equal(sent.bodies[1], { text = "안녕" })
  h.assert_deep_equal(sent.bodies[2], { text = "안녕", speak = true })
end

function T.test_a_notification_is_gated_and_explained_on_the_message_row_only()
  local cases = {
    { status = { service_version = "v1.1.0" }, message = "서비스 v1.2.0 필요", sends = 0 },
    { status = status_v12({ features = { "audio" } }), message = "이 PC에서 지원 안 함", sends = 0 },
    { status = status_v12(), service = { ok = false, kind = "forbidden", body = { error = "notify_disabled" } },
      message = "PC 알림 꺼짐", sends = 1 },
    { status = status_v12(), service = { ok = false, kind = "conflict", body = { error = "no_user_session" } },
      message = "사용자 없음", sends = 1 },
    { status = status_v12(), service = { ok = false, kind = "ratelimited" }, message = "잠시 후 다시", sends = 1 },
  }
  for i, case in ipairs(cases) do
    local device = device_with(case.status)
    local sent = with_notify(case.service, function()
      handlers_for("notification").deviceNotification(driver, device, { args = { notification = "hi" } })
    end)
    h.assert_equal(#sent, case.sends, "case " .. i .. " sends")
    h.assert_equal(info_message(device), case.message, "case " .. i)
    h.assert_nil(info_summary(device), "case " .. i .. " touched the summary row")
  end
end

function T.test_an_empty_notification_is_not_sent()
  local device = device_with(status_v12())
  local sent = with_notify(nil, function()
    handlers_for("speechSynthesis").speak(driver, device, { args = { phrase = " \n " } })
  end)
  h.assert_equal(#sent, 0)
  h.assert_equal(info_message(device), "보낼 문구 없음")
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
