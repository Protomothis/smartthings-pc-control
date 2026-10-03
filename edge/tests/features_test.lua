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
local fields = require "device.fields"
local emit = require "device.emit"
local rows = require "device.rows"
local clock = require "device.clock"
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
  fields.set_state(device, s)
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
  poll.once = function(_driver, polled, poll_opts)
    calls.polls = calls.polls + 1
    calls.poll_opts[#calls.poll_opts + 1] = poll_opts or {}
    if opts.on_poll then
      opts.on_poll(polled)
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

--- #108: what the message row (`pcToast.lastMessage`) shows last.
local function toast_value(device)
  return h.last_value(h.emitted(device), nil, caps.TOAST, "lastMessage")
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

  -- Service #104/#105: "audio"/"media" are listed only while media.enabled is
  -- on, so a v1.2.0 service without them has the setting off.
  local no_media = features.remember(state.new(), status_v12({ features = { "audio" } })).extras
  h.assert_equal(features.refusal(no_media, "pause"), "media_disabled")
  h.assert_nil(features.refusal(no_media, "mute"))
  local media_off = features.remember(state.new(), status_v12({ features = { "awake" } })).extras
  h.assert_equal(features.refusal(media_off, "volume"), "media_disabled")
  h.assert_equal(features.refusal(media_off, "preset"), "feature_missing")

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
  -- Service #104/#105: the PC answered, the action did not work.
  h.assert_equal(features.error_note("unreachable", { error = "unsupported", message = "no device" }),
    "feature_missing")
  h.assert_equal(features.error_note("unreachable", { error = "failed" }), "action_failed")
  h.assert_equal(features.error_note("unreachable", { error = "timeout" }), "action_failed")
end

function T.test_a_failed_action_on_the_pc_is_not_an_unreachable_pc()
  local device = device_with(status_v12())
  with_service({ ok = false, kind = "unreachable", body = { error = "failed", message = "COM" } }, function()
    handlers_for("audioVolume").volumeUp(driver, device, { args = {} })
  end)
  h.assert_equal(info_summary(device), "PC에서 실행 실패")
  h.assert_nil(h.event_value(h.emitted(device), caps.STATUS, "connection"))
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
  local answered = calls.poll_opts[1].force
  h.assert_true(answered["audioVolume.volume"] == true)
  h.assert_true(answered["audioMute.mute"] == true)
  -- ... which emit.force_rows then applies to exactly those records.
  local events = emit.force_rows(features.audio_events(status_v12()), answered)
  for _, e in ipairs(events) do
    h.assert_true(e.force == true, emit.row_key(e) .. " not forced")
  end
end

-- A media command that cannot go out, or that the service refused, leaves a
-- note on the info rows saying why (forced, so the same note twice is seen
-- twice). A refusal the service words itself is not an unreachable PC: the
-- connection row is left alone and no answer poll follows. A 403 without a
-- code is still the allow-list (§3.1).
function T.test_a_refused_media_command_says_why()
  local refused = function(kind, code)
    return { ok = false, kind = kind, body = { error = code } }
  end
  for _, c in ipairs({
    -- name, status the driver knows, language, the service's answer, handler, actions sent, summary
    { "a v1.1.0 service does not know volumeup", { service_version = "v1.1.0" }, "ko", nil,
      { "audioVolume", "volumeUp" }, 0, "PC 앱 v1.2.0 필요" },
    { "the same in English", { service_version = "v1.1.0" }, "en", nil,
      { "audioMute", "mute" }, 0, "Needs PC app v1.2.0" },
    { "a PC that never answered", nil, "ko", nil,
      { "mediaPlayback", "pause" }, 0, i18n.t("ko", "unreachable") },
    { "409 no_user_session", status_v12(), "ko", refused("conflict", "no_user_session"),
      { "mediaPlayback", "play" }, 1, "사용자 없음" },
    { "403 media_disabled", status_v12(), "ko", refused("forbidden", "media_disabled"),
      { "mediaTrackControl", "nextTrack" }, 1, "미디어 제어 꺼짐" },
  }) do
    local name, status, lang, reply, handler, actions, summary = table.unpack(c, 1, 7) -- explicit: rows have nil holes
    local device = device_with(status)
    device.preferences.language = lang
    local calls = with_service(reply, function()
      handlers_for(handler[1])[handler[2]](driver, device, { args = {} })
    end)
    h.assert_equal(#calls.actions, actions, name)
    h.assert_equal(info_summary(device), summary, name)
    h.assert_equal(info_message(device), summary, name)
    h.assert_true(h.event_forced(h.emitted(device), caps.STATUS, "summary"), name)
    if reply then
      h.assert_equal(calls.polls, 0, name .. ": no answer poll")
      h.assert_nil(h.event_value(h.emitted(device), caps.STATUS, "connection"), name .. ": not unreachable")
    end
  end

  local device = device_with(status_v12())
  with_service(refused("forbidden", "forbidden"), function()
    handlers_for("audioMute").mute(driver, device, { args = {} })
  end)
  h.assert_equal(h.last_value(h.emitted(device), nil, caps.STATUS, "connection"), "unauthorized")
  h.assert_equal(info_message(device), i18n.t("ko", "forbidden"))
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

function T.test_a_command_before_any_status_asks_first()
  -- The hub restarted and nothing has been read yet: poll once, then decide.
  local device = device_with(nil)
  local calls = with_service({ on_poll = function(d)
    local s = fields.state(d)
    features.remember(s, status_v12())
    fields.set_state(d, s)
  end }, function()
    handlers_for("audioMute").unmute(driver, device, { args = {} })
  end)
  h.assert_equal(calls.polls, 2, "one poll to learn the features, one after the command")
  h.assert_equal(#calls.actions, 1)
  h.assert_equal(calls.actions[1].command, "unmute")
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
  h.assert_equal(features.preset_names({ service_version = "v1.1.0" }, "ko"), "PC 앱 v1.2.0 필요")
end

function T.test_a_long_names_row_is_cut_on_a_character()
  -- C4: each name is cut to twelve characters and "…", so ten long names
  -- leave every slot number on the row, inside the row's cap.
  local list = {}
  for slot = 1, 10 do
    list[#list + 1] = { slot = slot, name = string.rep("가", 30) }
  end
  local text = features.preset_names(with_presets(list), "ko")
  local count = select(2, text:gsub("[\1-\127\194-\244][\128-\191]*", ""))
  h.assert_true(count <= features.NAMES_MAX_CHARS, "inside the cap: " .. count)
  local cut = string.rep("가", 12) .. "…"
  local parts = {}
  for slot = 1, 10 do
    parts[slot] = slot .. " " .. cut
  end
  h.assert_equal(text, table.concat(parts, " · "))
  h.assert_equal(features.truncate("짧다", 5), "짧다")
  h.assert_equal(features.truncate("abcdef", 4), "abc…")
end

function T.test_ten_presets_with_long_names_show_every_number()
  local list = {}
  for slot = 1, 10 do
    list[#list + 1] = { slot = slot, name = "Preset number " .. slot .. " with a very long name" }
  end
  local text = features.preset_names(with_presets(list), "en")
  for slot = 1, 10 do
    h.assert_contains(text, slot .. " Preset numbe…", "slot " .. slot)
  end
  h.assert_true(text:sub(-3) == "…", "the last one is cut, not the row")
  -- A name of exactly 13 characters stays whole; 12 + "…" is no shorter.
  h.assert_equal(features.preset_names(with_presets({ { slot = 1, name = "Thirteen char" } }), "en"),
    "1 Thirteen char")
  h.assert_equal(features.preset_names(with_presets({ { slot = 1, name = "Fourteen chars" } }), "en"),
    "1 Fourteen cha…")
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
  h.assert_equal(rows.shown_preset(device), "2")
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
  h.assert_equal(info_summary(old), "PC 앱 v1.2.0 필요")

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
  rows.emit_preset(device, "3", true, deps)
  device.emitted = {}
  -- The poll right after the command: still showing it.
  now = 1002
  h.assert_false(rows.ensure_preset(device, deps))
  h.assert_equal(#device.emitted, 0)
  -- The next scheduled poll: back to "none", forced, and once more after.
  now = 1030
  h.assert_true(rows.ensure_preset(device, deps))
  h.assert_equal(last_preset(device), "none")
  h.assert_true(h.event_forced(h.emitted(device), caps.PRESET, "lastPreset"))
  h.assert_true(rows.ensure_preset(device, deps), "the one repeat")
  h.assert_equal(#device.emitted, 2)
  h.assert_false(rows.ensure_preset(device, deps), "and then nothing")
  h.assert_equal(#device.emitted, 2)
end

--- The preset timers `run` started on the shared driver since `from`.
local function preset_timers(from)
  local out = {}
  for i = from + 1, #driver.timers do
    local t = driver.timers[i]
    if t.name == "preset-reset" or t.name == "preset-repeat" then
      out[#out + 1] = t
    end
  end
  return out
end

--- Run `fn` with `clock.wallclock` reading `time.now`.
local function with_clock(time, fn)
  local original = clock.wallclock
  clock.wallclock = function() return time.now end
  local ok, err = pcall(fn)
  clock.wallclock = original
  if not ok then
    error(err, 0)
  end
end

local function preset_events(device)
  local out = {}
  for _, e in ipairs(h.emitted(device)) do
    if e.cap == caps.PRESET and e.attr == "lastPreset" then
      out[#out + 1] = e
    end
  end
  return out
end

function T.test_a_timer_puts_the_preset_row_back_without_waiting_for_a_poll()
  local device = device_with(with_presets(PRESETS))
  local time = { now = 5000 }
  local from = #driver.timers
  with_clock(time, function()
    with_service(nil, function()
      handlers_for(caps.PRESET).run(driver, device, { args = { slot = "2" } })
    end)
    local timers = preset_timers(from)
    h.assert_equal(#timers, 1)
    h.assert_equal(timers[1].name, "preset-reset")
    h.assert_equal(timers[1].delay, rows.PRESET_HOLD_SECONDS)
    device.emitted = {}
    -- The timer fires at the hold. `os.time` counts whole seconds, so a timer
    -- that fires a moment early still ends the hold.
    time.now = 5000 + rows.PRESET_HOLD_SECONDS - 1
    h.assert_true(h.fire_last(driver, "preset-reset"))
    h.assert_equal(last_preset(device), "none")
    h.assert_true(h.event_forced(h.emitted(device), caps.PRESET, "lastPreset"))
    h.assert_equal(#device.emitted, 1)
    -- The owed repeat, on a short timer of its own.
    timers = preset_timers(from)
    h.assert_equal(timers[#timers].name, "preset-repeat")
    h.assert_equal(timers[#timers].delay, rows.PRESET_REPEAT_SECONDS)
    h.assert_true(h.fire_last(driver, "preset-repeat"))
    h.assert_equal(#device.emitted, 2)
    h.assert_true(h.event_forced(h.emitted(device), caps.PRESET, "lastPreset"))
    -- And nothing after it: no timer left, and the polls stay quiet.
    for _, t in ipairs(preset_timers(from)) do
      h.assert_true(t.cancelled, "no preset timer is still pending")
    end
    time.now = 5100
    h.assert_false(rows.ensure_preset(device))
    h.assert_equal(#device.emitted, 2)
  end)
  -- The same preset runs again as soon as the row rests on "none".
  local calls = with_service(nil, function()
    handlers_for(caps.PRESET).run(driver, device, { args = { slot = "2" } })
  end)
  h.assert_equal(#calls.actions, 1)
end

function T.test_a_second_run_within_the_hold_replaces_the_preset_timer()
  local device = device_with(with_presets(PRESETS))
  local time = { now = 6000 }
  local from = #driver.timers
  with_clock(time, function()
    with_service(nil, function()
      handlers_for(caps.PRESET).run(driver, device, { args = { slot = "1" } })
      time.now = 6003
      handlers_for(caps.PRESET).run(driver, device, { args = { slot = "2" } })
    end)
    local timers = preset_timers(from)
    h.assert_equal(#timers, 2)
    h.assert_true(timers[1].cancelled, "the first run's timer is cancelled")
    h.assert_false(timers[2].cancelled)
    -- Even if the platform ran the cancelled one anyway, it would do nothing.
    device.emitted = {}
    time.now = 6005
    timers[1].fn()
    h.assert_equal(#device.emitted, 0)
    h.assert_equal(rows.shown_preset(device), "2")
    -- The second run's timer ends the second run's hold.
    time.now = 6008
    h.assert_true(h.fire_last(driver, "preset-reset"))
    h.assert_true(h.fire_last(driver, "preset-repeat"))
    h.assert_false(h.fire_last(driver, "preset-repeat"))
    h.assert_equal(#preset_events(device), 2)
    h.assert_equal(last_preset(device), "none")
  end)
end

function T.test_a_poll_that_comes_first_leaves_the_timers_nothing_extra_to_send()
  local device = device_with(with_presets(PRESETS))
  local time = { now = 7000 }
  with_clock(time, function()
    with_service(nil, function()
      handlers_for(caps.PRESET).run(driver, device, { args = { slot = "5" } })
    end)
    device.emitted = {}
    time.now = 7000 + rows.PRESET_HOLD_SECONDS
    h.assert_true(rows.ensure_preset(device), "a poll at the hold resets the row")
    h.assert_true(h.fire_last(driver, "preset-reset"), "then the timer: the owed repeat")
    h.assert_false(h.fire_last(driver, "preset-repeat"), "nothing left to repeat")
    h.assert_false(rows.ensure_preset(device))
    h.assert_equal(#preset_events(device), 2)
  end)
end

function T.test_without_timers_the_polls_still_put_the_preset_row_back()
  local device = device_with(with_presets(PRESETS))
  local time = { now = 8000 }
  local timerless = {}
  with_clock(time, function()
    with_service(nil, function()
      handlers_for(caps.PRESET).run(timerless, device, { args = { slot = "1" } })
    end)
    h.assert_equal(rows.shown_preset(device), "1")
    h.assert_false(rows.hold_preset(nil, device))
    device.emitted = {}
    time.now = 8002
    h.assert_false(rows.ensure_preset(device), "still inside the hold")
    time.now = 8030
    h.assert_true(rows.ensure_preset(device))
    h.assert_true(rows.ensure_preset(device), "the one repeat")
    h.assert_false(rows.ensure_preset(device))
    h.assert_equal(#preset_events(device), 2)
    h.assert_equal(last_preset(device), "none")
  end)
end

--------------------------------------------------------------------------------
-- #123: the watch card (component `apps`, pcWatchList)
--------------------------------------------------------------------------------

local function with_apps(apps, opts)
  opts = opts or {}
  local status = status_v12({ activity = {
    enabled = opts.enabled ~= false, apps = apps, top = opts.top or "", scanned = opts.scanned,
  } })
  if opts.features then
    status.features = opts.features
  end
  return status
end

local STEAM = { slot = 1, id = "steam.exe", label = "Steam", running = true }
local CODE = { slot = 2, id = "code.exe", label = "VS Code", running = false }
local OBS = { slot = 3, id = "obs64.exe", label = "OBS", running = true }
local DISCORD = { slot = 5, id = "discord.exe", label = "Discord", running = true }

local WATCH = features.WATCH_COMPONENT

--- The card's rows of one record list: summary, names and the five slots.
local function card(events)
  local out = {
    summary = h.last_value(events, WATCH, caps.WATCH, "summary"),
    names = h.last_value(events, WATCH, caps.WATCH, "names"),
  }
  for slot = 1, features.WATCH_SLOTS do
    out[slot] = h.last_value(events, WATCH, caps.WATCH, features.slot_attr(slot))
  end
  return out
end

-- Each case: a status, and the card it paints (ko). `nil` slots: not sent at
-- all, so the hub keeps the last value.
local WATCH_CASES = {
  { "full, sorted by slot whatever the list order", with_apps({ OBS, STEAM, CODE }, { top = "steam.exe" }),
    { summary = "Steam 외 1", names = "1 Steam · 2 VS Code · 3 OBS",
      "running", "stopped", "running", "empty", "empty" } },
  { "nothing running", with_apps({ CODE }),
    { summary = "없음", names = "2 VS Code", "empty", "stopped", "empty", "empty", "empty" } },
  { "an empty list", with_apps({}),
    { summary = "없음", names = "없음", "empty", "empty", "empty", "empty", "empty" } },
  { "the opt-in off: the slots keep their values", with_apps({}, { enabled = false, features = { "audio" } }),
    { summary = "감지 꺼짐", names = "꺼짐" } },
  { "a service older than v1.2.0: the same", { service_version = "v1.1.0" },
    { summary = "PC 앱 v1.2.0 필요", names = "없음" } },
}

function T.test_the_watch_card_rows()
  for _, c in ipairs(WATCH_CASES) do
    local what, status, want = table.unpack(c, 1, 3)
    local events = features.watch_events(status, "ko")
    h.assert_deep_equal(card(events), want, what)
    for _, e in ipairs(events) do
      h.assert_equal(e.component, WATCH, what .. ": on the apps component")
      h.assert_true(e.value ~= "", what .. ": never an empty row")
    end
  end
  h.assert_equal(features.apps_summary(with_apps({ STEAM }), "en"), "Steam")
  h.assert_equal(features.apps_summary(with_apps({ CODE, STEAM, OBS, DISCORD }), "en"), "Steam +2")
  h.assert_equal(features.watch_names(with_apps({ STEAM, OBS }), "en"), "1 Steam · 3 OBS")
  h.assert_equal(features.watch_names(with_apps({}, { enabled = false, features = {} }), "en"), "Off")
  h.assert_equal(features.watch_names({ service_version = "v1.1.0" }, "en"), "None")
end

-- The summary is the first cell of the card's preview in the main view, a
-- third of the width in large type (platform notes "화면 배치"): the app
-- label alone, "외 N" / "+N" for the others, the label cut at 13 characters.
-- `{ label, others running, ko, en }`.
local SUMMARY_CASES = {
  { "Claude", 0, "Claude", "Claude" },
  { "Claude", 1, "Claude 외 1", "Claude +1" },
  { "Claude", 4, "Claude 외 4", "Claude +4" },
  { "열세글자짜리프로그램이름임", 0, "열세글자짜리프로그램이름임", "열세글자짜리프로그램이름임" },
  { "Microsoft Teams", 0, "Microsoft Te…", "Microsoft Te…" },
  { "Visual Studio Code", 2, "Visual Studi… 외 2", "Visual Studi… +2" },
}

function T.test_the_summary_is_one_short_cell()
  for _, c in ipairs(SUMMARY_CASES) do
    local label, others, ko, en = table.unpack(c, 1, 4)
    local list = { { slot = 1, id = "top.exe", label = label, running = true } }
    for n = 1, others do
      list[#list + 1] = { slot = n + 1, id = n .. ".exe", label = "Other " .. n, running = true }
    end
    local status = with_apps(list, { top = "top.exe" })
    h.assert_equal(features.apps_summary(status, "ko"), ko, label .. " ko")
    h.assert_equal(features.apps_summary(status, "en"), en, label .. " en")
  end
  -- The resting values, and the note an old service gets.
  h.assert_equal(features.apps_summary(with_apps({ CODE }), "ko"), "없음")
  h.assert_equal(features.apps_summary(with_apps({ CODE }), "en"), "None")
  local off = with_apps({}, { enabled = false, features = { "audio" } })
  -- C4: "감지 꺼짐", not the bare "꺼짐" a stopped slot reads.
  h.assert_equal(features.apps_summary(off, "ko"), "감지 꺼짐")
  h.assert_equal(features.apps_summary(off, "en"), "Detection off")
  h.assert_equal(features.apps_summary({ service_version = "v1.1.0" }, "ko"), "PC 앱 v1.2.0 필요")
  -- C4: the names row cuts each label the same way, so every slot number
  -- stays on the row.
  h.assert_equal(features.watch_names(with_apps({
    { slot = 1, id = "code.exe", label = "Visual Studio Code", running = true } }), "ko"), "1 Visual Studi…")
end

function T.test_the_summary_leads_with_the_lowest_running_slot()
  -- `top` that names a running entry is the service's own answer...
  h.assert_equal(features.apps_summary(with_apps({ STEAM, OBS }, { top = "obs64.exe" }), "ko"),
    "OBS 외 1")
  -- ...one that names nothing running (or nothing) gives way to the lowest slot.
  h.assert_equal(features.apps_summary(with_apps({ CODE, OBS, DISCORD }, { top = "code.exe" }), "ko"),
    "OBS 외 1")
  h.assert_equal(features.apps_summary(with_apps({ DISCORD, OBS }), "ko"), "OBS 외 1")
  -- The service lists "activity" only while the opt-in is on (#110); a block
  -- that claims to be on without the feature is not trusted either.
  local stray = with_apps({ STEAM }, { features = { "audio" } })
  h.assert_equal(features.apps_mode(stray), features.APPS_OFF)
  h.assert_deep_equal(features.apps_of(stray), {}, "and it lists nothing")
  -- The kind-based block of a #114 Dev build has no `apps`: off, not "none".
  local kind = status_v12({ activity = { enabled = true, kind = "game", labels = { "Steam" } } })
  h.assert_equal(features.apps_mode(kind), features.APPS_OFF)
  h.assert_equal(features.apps_mode({ service_version = "v1.1.0" }), features.APPS_OLD)
end

function T.test_the_watch_list_is_read_defensively()
  local list = {
    { slot = 4, id = "Steam.EXE", label = " Steam\n", running = true },
    { slot = 2, id = "steam.exe", label = "twice", running = false },
    { slot = 1, id = "", label = "no id", running = true },
    "not an entry",
    { slot = 2, id = "notepad.exe", running = "yes" },
    { slot = 2, id = "taken.exe", running = true },
    { slot = 6, id = "six.exe", running = true },
    { slot = 1.5, id = "half.exe", running = true },
    { slot = "3", id = "x.exe", label = string.rep("가", 40), running = false },
    -- No slot at all (a Dev service before contract v2): the lowest free one.
    { id = "late.exe", label = "Late", running = true },
    { id = "later.exe", label = "Later", running = true },
    { id = "none-left.exe", running = true },
  }
  local apps = features.apps_of(with_apps(list))
  h.assert_deep_equal(apps, {
    { slot = 1, id = "late.exe", label = "Late", running = true },
    { slot = 2, id = "notepad.exe", label = "notepad.exe", running = false },
    { slot = 3, id = "x.exe", label = string.rep("가", 29) .. "…", running = false },
    { slot = 4, id = "steam.exe", label = "Steam", running = true },
    { slot = 5, id = "later.exe", label = "Later", running = true },
  }, "by slot; a repeated id or slot, a bad slot, no id and a non-table are skipped; "
    .. "a missing label is the id; only true runs; 30 code points at most")
  h.assert_equal(#apps, features.WATCH_SLOTS, "never more than five")
end

function T.test_long_labels_still_fit_the_definition()
  local long = string.rep("가", 30)
  local list = {}
  for slot = 1, 5 do
    list[slot] = { slot = slot, id = slot .. ".exe", label = long, running = true }
  end
  local status = with_apps(list)
  local summary = features.apps_summary(status, "ko")
  h.assert_equal(summary, string.rep("가", 12) .. "… 외 4", "the label is cut, the count stays")
  h.assert_true(#(summary:gsub("[\128-\191]", "")) <= features.WATCH_SUMMARY_MAX_CHARS, summary)
  local names = features.watch_names(status, "ko")
  h.assert_true(#(names:gsub("[\128-\191]", "")) <= features.WATCH_NAMES_MAX_CHARS, names)
  -- C4: every slot number is on the row, each label cut to 12 + "…".
  local cut = string.rep("가", 12) .. "…"
  h.assert_equal(names, table.concat({ "1 " .. cut, "2 " .. cut, "3 " .. cut, "4 " .. cut, "5 " .. cut }, " · "))
end

function T.test_a_list_edit_holds_running_for_one_status()
  -- Right after a config change the service lists every entry as stopped
  -- until its next scan: a slot that shows "running" stays there for that
  -- one status; the next one, with the list unchanged, decides.
  local s = state.new(state.ON)
  features.remember(s, with_apps({ STEAM, OBS }))
  h.assert_deep_equal(s.extras.watch, { "running", "empty", "running", "empty", "empty" })
  local renamed = { slot = 3, id = "obs64.exe", label = "OBS Studio", running = false }
  local placeholder = { slot = 1, id = "steam.exe", label = "Steam", running = false }
  features.remember(s, with_apps({ placeholder, renamed, CODE }))
  h.assert_deep_equal(s.extras.watch, { "running", "stopped", "running", "empty", "empty" },
    "held; the new slot 2 is what the service says")
  local events = state.apply_status(s, with_apps({ placeholder, renamed, CODE }), { lang = "ko" })
  h.assert_equal(h.last_value(events, WATCH, caps.WATCH, "slotOne"), "running", "the rows follow the hold")
  h.assert_equal(h.last_value(events, WATCH, caps.WATCH, "names"), "1 Steam · 2 VS Code · 3 OBS Studio")
  features.remember(s, with_apps({ placeholder, renamed, CODE }))
  h.assert_deep_equal(s.extras.watch, { "stopped", "stopped", "stopped", "empty", "empty" }, "then it decides")
  -- Switching the feature back on is an edit too.
  features.remember(s, with_apps({ STEAM }))
  features.remember(s, with_apps({}, { enabled = false }))
  h.assert_deep_equal(s.extras.watch, { "running", "empty", "empty", "empty", "empty" }, "off keeps the values")
  features.remember(s, with_apps({ placeholder }))
  h.assert_equal(s.extras.watch[1], "running", "held after the opt-in came back")
end

-- C2: the service says when its first scan for a list is done. Before it
-- (`scanned: false`) no slot moves from running to stopped, however many
-- statuses come and whether or not the list changed; after it the values are
-- taken as they are, list change or not.
function T.test_scanned_false_never_moves_running_to_stopped()
  local s = state.new(state.ON)
  features.remember(s, with_apps({ STEAM, OBS }, { scanned = true }))
  h.assert_deep_equal(s.extras.watch, { "running", "empty", "running", "empty", "empty" })
  local idle_steam = { slot = 1, id = "steam.exe", label = "Steam", running = false }
  local idle_obs = { slot = 3, id = "obs64.exe", label = "OBS", running = false }
  for n = 1, 3 do
    features.remember(s, with_apps({ idle_steam, idle_obs, CODE }, { scanned = false }))
    h.assert_deep_equal(s.extras.watch, { "running", "stopped", "running", "empty", "empty" },
      "unscanned status " .. n .. ": held; the new slot 2 is what the service says")
  end
  -- Not scanned yet, and an app starts: stopped -> running is real news.
  features.remember(s, with_apps({ idle_steam, idle_obs, { slot = 2, id = "code.exe", label = "VS Code",
    running = true } }, { scanned = false }))
  h.assert_equal(s.extras.watch[2], "running", "an unscanned list may still move a slot to running")
  -- Scanned: taken as it is, even with a list that changed right now.
  features.remember(s, with_apps({ idle_steam, { slot = 3, id = "obs64.exe", label = "OBS Studio",
    running = false } }, { scanned = true }))
  h.assert_deep_equal(s.extras.watch, { "stopped", "empty", "stopped", "empty", "empty" },
    "scanned: no hold, not even for the list change")
end

function T.test_a_status_without_scanned_keeps_the_list_signature_hold()
  -- An older service: the hold lasts one status after a list change, and an
  -- unchanged list decides.
  local s = state.new(state.ON)
  features.remember(s, with_apps({ STEAM }))
  local idle_steam = { slot = 1, id = "steam.exe", label = "Steam", running = false }
  features.remember(s, with_apps({ idle_steam, CODE }))
  h.assert_equal(s.extras.watch[1], "running", "the list changed: held")
  features.remember(s, with_apps({ idle_steam, CODE }))
  h.assert_equal(s.extras.watch[1], "stopped", "unchanged: it decides")
  -- The same two statuses with `scanned: false` would still hold.
  local held = state.new(state.ON)
  features.remember(held, with_apps({ STEAM }, { scanned = true }))
  features.remember(held, with_apps({ idle_steam, CODE }, { scanned = false }))
  features.remember(held, with_apps({ idle_steam, CODE }, { scanned = false }))
  h.assert_equal(held.extras.watch[1], "running")
end

--- Polls with `client.get_status` answering `answers` in turn (`false` = the
--- PC does not answer).
local function polling(device, answers)
  local original = client.get_status
  local i = 0
  client.get_status = function()
    i = i + 1
    local body = answers[i]
    if body == false then
      return false, nil, "unreachable"
    end
    return true, body, nil
  end
  local ok, err = pcall(function()
    for _ = 1, #answers do
      poll.once(nil, device)
    end
  end)
  client.get_status = original
  if not ok then
    error(err, 0)
  end
end

function T.test_an_unreachable_pc_or_an_off_list_keeps_the_slots()
  -- Only an "on" list moves a slot: a PC that does not answer, an opt-in
  -- switched off and a service without the list leave every slot where it
  -- was (a made-up "stopped" would fire every "꺼지면" routine).
  local device = device_with(nil)
  device:set_field(fields.ROWS_PAINTED, poll.ROWS_VERSION)
  polling(device, { with_apps({ STEAM, OBS }, { top = "steam.exe" }) })
  local mark = #h.emitted(device)
  polling(device, { false, false, with_apps({}, { enabled = false }), { service_version = "v1.1.0", protocol = 1 } })
  local later = {}
  for i = mark + 1, #h.emitted(device) do
    later[#later + 1] = h.emitted(device)[i]
  end
  for slot = 1, features.WATCH_SLOTS do
    h.assert_nil(h.last_value(later, WATCH, caps.WATCH, features.slot_attr(slot)), features.slot_attr(slot) .. " is not touched")
  end
  h.assert_deep_equal({ h.last_value(later, WATCH, caps.WATCH, "summary"),
    h.last_value(later, WATCH, caps.WATCH, "names") }, { "PC 앱 v1.2.0 필요", "없음" })
  h.assert_equal(h.last_value(h.emitted(device), WATCH, caps.WATCH, "slotOne"), "running")
end

function T.test_an_activity_push_repaints_at_once()
  -- activity.changed carries the whole status like every push (#110), so it
  -- goes through the same apply_status as a poll.
  local push = require "push"
  local status = with_apps({ STEAM }, { top = "steam.exe" })
  local nxt, events = push.apply(state.new(state.ON),
    { type = "activity.changed", status = status }, { lang = "ko" })
  h.assert_deep_equal(card(events), { summary = "Steam", names = "1 Steam",
    "running", "empty", "empty", "empty", "empty" })
  h.assert_deep_equal(nxt.extras.apps, { STEAM })
  h.assert_equal(nxt.extras.apps_mode, features.APPS_ON)
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
  local calls = with_service(nil, function(recorded)
    handlers_for("switch").on(driver, device, switch_command("on", "awake"))
    handlers_for("switch").off(driver, device, switch_command("off", "awake"))
    -- The event budget: the second command landed inside the first one's
    -- answer window, so its poll comes when the window closes.
    h.assert_equal(recorded.polls, 1, "two commands at once share the window")
    h.fire_last(driver, "answer-poll")
  end)
  h.assert_deep_equal(calls.actions, {
    { command = "awake", value = 30 }, { command = "awakeoff" },
  })
  h.assert_equal(calls.polls, 2)
  h.assert_true(calls.poll_opts[1].force["awake/switch.switch"] == true,
    "the poll after it answers the awake row")
  h.assert_true(calls.poll_opts[2].force["awake/switch.switch"] == true,
    "and so does the one that closes the window")
  -- The PC's power was not touched: no wake, no shutdown.
  h.assert_equal(fields.state(device).power_state, state.ON)
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
  h.assert_equal(info_summary(device), "PC 앱 v1.2.0 필요")
end

function T.test_a_repaint_paints_the_new_rows_from_the_last_status()
  -- An icon switch or a preference change repaints every row forced. Painting
  -- the keep-awake switch with its never-polled "off" would fire every routine
  -- that watches it, so a repaint uses what the PC last said.
  local device = device_with(status_v12({ awake = { on = true },
    activity = { enabled = true, apps = { { slot = 2, id = "steam.exe", label = "Steam", running = true } },
      top = "steam.exe" } }))
  poll.repaint(device)
  local emitted = h.emitted(device)
  h.assert_equal(h.last_value(emitted, "awake", "switch", "switch"), "on")
  h.assert_true(h.component_forced(emitted, "awake", "switch", "switch"))
  -- #123: the watch card too, every row forced, the empty slots included.
  h.assert_deep_equal(card(emitted), { summary = "Steam", names = "2 Steam",
    "empty", "running", "empty", "empty", "empty" })
  h.assert_true(h.component_forced(emitted, WATCH, caps.WATCH, "slotTwo"))
  -- A device nothing has been read for gets the resting defaults.
  local fresh = device_with(nil)
  poll.repaint(fresh)
  h.assert_equal(h.last_value(h.emitted(fresh), "awake", "switch", "switch"), "off")
  h.assert_deep_equal(card(h.emitted(fresh)), { summary = "없음", names = "없음",
    "empty", "empty", "empty", "empty", "empty" })
end

function T.test_a_repaint_of_an_off_list_keeps_what_the_slots_showed()
  -- #123: an icon switch while the list is off (or before this run has read an
  -- "on" status) paints the slots with what they last had - this run's last
  -- send, else the hub's state cache - never a made-up value.
  local device = h.with_state_cache(device_with(with_apps({ STEAM, OBS })))
  poll.repaint(device)
  fields.set_state(device, (function()
    local s = state.new(state.ON)
    features.remember(s, with_apps({}, { enabled = false }))
    return s
  end)())
  local mark = #h.emitted(device)
  poll.repaint(device)
  local again = {}
  for i = mark + 1, #h.emitted(device) do
    again[#again + 1] = h.emitted(device)[i]
  end
  h.assert_deep_equal(card(again), { summary = "감지 꺼짐", names = "꺼짐",
    "running", "empty", "running", "empty", "empty" }, "from what was sent")
  -- A restart: nothing sent in this run, the hub's cache still has them.
  h.restart(device, {})
  fields.set_state(device, state.new(state.ON))
  mark = #h.emitted(device)
  poll.repaint(device)
  again = {}
  for i = mark + 1, #h.emitted(device) do
    again[#again + 1] = h.emitted(device)[i]
  end
  h.assert_equal(h.last_value(again, WATCH, caps.WATCH, "slotThree"), "running", "from the hub's state cache")
  h.assert_equal(h.last_value(again, WATCH, caps.WATCH, "slotTwo"), "empty")
end

function T.test_an_awake_push_moves_the_switch_at_once()
  local push = require "push"
  local status = status_v12({ awake = { on = true, ["until"] = "2026-09-30T12:00:00+09:00" } })
  local nxt, events = push.apply(state.new(state.ON), { type = "awake.changed", status = status }, {})
  h.assert_equal(h.component_value(events, "awake", "switch", "switch"), "on")
  h.assert_true(nxt.extras.awake_on)
  -- ... and the glue puts it on the awake component of the device.
  local device = h.fake_device({})
  emit.rows(device, events)
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
  -- An unknown percent is left out, the power source still reported.
  events = features.battery_events(laptop(-1, true))
  h.assert_nil(h.component_value(events, "battery", "battery", "battery"))
  h.assert_equal(h.component_value(events, "battery", "powerSource", "powerSource"), "mains")
  -- A desktop, or a service older than v1.2.0, reports nothing.
  h.assert_equal(#features.battery_events(DESKTOP), 0)
  h.assert_equal(#features.battery_events({ service_version = "v1.1.0" }), 0)
end

local function device_on_profile(name, id)
  local device = device_with(status_v12())
  device.id = id or ("battery-" .. name)
  device:set_field(fields.PROFILE_NAME, name)
  device.profile = { id = "abc", name = name, components = h.components_for(name) }
  return device
end

function T.test_two_statuses_with_a_battery_move_a_desktop_profile()
  profiles.reset()
  local device = device_on_profile("pc-tv.v10")
  h.assert_nil(profiles.apply_battery(device, true), "one status is not enough")
  h.assert_equal(profiles.apply_battery(device, true), "pc-tv-battery.v10", "the style is kept")
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-tv-battery.v10" } })
  h.assert_true(profiles.has_battery(device), "persisted for the next migration")
  -- Settled: more of the same asks for nothing.
  h.assert_nil(profiles.apply_battery(device, true))
  h.assert_equal(#device.metadata_updates, 1)
end

function T.test_and_two_without_one_move_it_back()
  profiles.reset()
  local device = device_on_profile("pc-battery.v10")
  h.assert_nil(profiles.apply_battery(device, false))
  h.assert_equal(profiles.apply_battery(device, false), "pc.v10")
  h.assert_false(profiles.has_battery(device))
end

function T.test_a_flapping_reading_moves_nothing()
  profiles.reset()
  local device = device_on_profile("pc.v10")
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
  local device = device_on_profile("pc.v10")
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
  -- The persisted answer is what `ensure` reads for a name without a battery
  -- half: a v1 name, or a plain v2/v3 name whose statuses said "battery" just
  -- before the update.
  profiles.reset()
  local device = device_on_profile("pc-hub.v1", "laptop-v1")
  device:set_field(fields.HAS_BATTERY, true)
  h.assert_equal(profiles.ensure(device), "pc-hub-battery.v10")
  local plain = device_on_profile("pc-tv.v2", "laptop-v2-plain")
  plain:set_field(fields.HAS_BATTERY, true)
  h.assert_equal(profiles.ensure(plain), "pc-tv-battery.v10")
  -- A v2 or v3 battery name keeps its half without the field.
  local named = device_on_profile("pc-tv-battery.v2", "laptop-v2")
  h.assert_equal(profiles.ensure(named), "pc-tv-battery.v10")
  local named_v3 = device_on_profile("pc-hub-battery.v3", "laptop-v3")
  h.assert_equal(profiles.ensure(named_v3), "pc-hub-battery.v10")
  local plain_v3 = device_on_profile("pc-remote.v3", "laptop-v3-plain")
  plain_v3:set_field(fields.HAS_BATTERY, true)
  h.assert_equal(profiles.ensure(plain_v3), "pc-remote-battery.v10")
  -- pcToast: and a v4 (pcNotify) name the same way.
  local named_v4 = device_on_profile("pc-plug-battery.v4", "laptop-v4")
  h.assert_equal(profiles.ensure(named_v4), "pc-plug-battery.v10")
  local plain_v4 = device_on_profile("pc-plug.v4", "laptop-v4-plain")
  plain_v4:set_field(fields.HAS_BATTERY, true)
  h.assert_equal(profiles.ensure(plain_v4), "pc-plug-battery.v10")
end

function T.test_a_poll_follows_the_battery_and_repaints_after()
  profiles.reset()
  local device = device_on_profile("pc.v10", "polled-laptop")
  local fake = { timers = {} }
  function fake:call_with_delay(delay, fn, name)
    self.timers[#self.timers + 1] = { delay = delay, fn = fn, name = name }
  end
  h.assert_nil(poll.follow_battery(fake, device, laptop(50, false)))
  h.assert_equal(poll.follow_battery(fake, device, laptop(49, false)), "pc-battery.v10")
  h.assert_equal(#fake.timers, 1)
  h.assert_equal(fake.timers[1].name, "battery-profile")
end

function T.test_a_battery_push_reaches_the_battery_component()
  local push = require "push"
  local _, events = push.apply(state.new(state.ON),
    { type = "battery.changed", status = laptop(15, false) }, {})
  h.assert_equal(h.component_value(events, "battery", "battery", "battery"), 15)
  local device = device_on_profile("pc-battery.v10", "pushed-laptop")
  emit.rows(device, events)
  h.assert_equal(h.component_value(h.emitted(device), "battery", "battery", "battery"), 15)
  -- The same events on a desktop's profile go nowhere.
  local desktop = device_on_profile("pc.v10", "pushed-desktop")
  emit.rows(desktop, events)
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
  client.notify = function(_, text, ...)
    sent[#sent + 1] = { text = text, extra = select("#", ...) }
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

function T.test_pc_toast_send_is_a_toast()
  -- The screen's one text field: `pcToast.send(text)`, text only.
  local device = device_with(status_v12())
  local sent = with_notify(nil, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "현관문이 열렸습니다" } })
  end)
  h.assert_deep_equal(sent, { { text = "현관문이 열렸습니다", extra = 0 } })
  h.assert_equal(info_message(device), "PC에 메시지를 보냈습니다")
  h.assert_nil(info_summary(device), "a sent message leaves the summary row alone")
  -- The row the text was typed into answers with the text, forced: the app's
  -- spinner waits for an event on `lastMessage` (2026-10-01: a row bound to
  -- nothing spun into "네트워크 오류").
  h.assert_equal(toast_value(device), "현관문이 열렸습니다")
  h.assert_true(h.event_forced(h.emitted(device), caps.TOAST, "lastMessage"))
  h.assert_equal(device:get_field(fields.LAST_TOAST), "현관문이 열렸습니다", "remembered")
end

function T.test_the_same_text_twice_is_answered_twice()
  -- Unchanged, so only `state_change` gets the second answer through.
  local device = device_with(status_v12())
  with_notify(nil, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "밥 먹자" } })
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "밥 먹자" } })
  end)
  local answers = 0
  for _, e in ipairs(h.emitted(device)) do
    if e.cap == caps.TOAST and e.attr == "lastMessage" then
      answers = answers + 1
      h.assert_equal(e.value, "밥 먹자")
      h.assert_true((e.options or {}).state_change == true, "every answer is forced")
    end
  end
  h.assert_equal(answers, 2)
end

function T.test_the_row_shows_the_cleaned_text_that_went_out()
  local device = device_with(status_v12())
  local sent = with_notify(nil, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "  세탁\n끝  " } })
  end)
  h.assert_equal(sent[1].text, "세탁 끝")
  h.assert_equal(toast_value(device), "세탁 끝")
end

function T.test_the_confirmation_follows_the_language()
  local device = device_with(status_v12())
  device.preferences.language = "en"
  with_notify(nil, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "hi" } })
    h.assert_equal(info_message(device), "Message sent to the PC")
  end)
end

function T.test_pc_toast_handles_exactly_its_one_command()
  local names = {}
  for name in pairs(handlers_for(caps.TOAST)) do
    names[#names + 1] = name
  end
  table.sort(names)
  h.assert_deep_equal(names, { "send" })
end

function T.test_no_read_aloud_or_standard_notification_handler_is_left()
  -- Read-aloud is gone, and with it pcMessage and the standard pair of the
  -- unpublished v2 profiles: nothing the driver registers can speak.
  local handlers = driver.capability_handlers or {}
  h.assert_nil(handlers["notification"], "the standard notification of pc.v2")
  h.assert_nil(handlers["speechSynthesis"], "the standard speechSynthesis of pc.v2")
  h.assert_nil(handlers["numbersystem53811.pcmessage"], "pcMessage of pc.v3")
  h.assert_nil(handlers["numbersystem53811.pcnotify"], "pcNotify of pc.v4, whose row had no attribute")
  for id, by_command in pairs(handlers) do
    h.assert_nil(by_command.speak, tostring(id) .. " still handles speak")
  end
end

function T.test_a_long_message_is_cut_to_the_services_limit_before_it_goes_out()
  -- The capability caps the text at 200 too, but the driver cuts after
  -- cleaning whatever arrives.
  local device = device_with(status_v12())
  local sent = with_notify(nil, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = string.rep("가", 250) } })
  end)
  h.assert_equal(#sent, 1)
  local count = select(2, sent[1].text:gsub("[\1-\127\194-\244][\128-\191]*", ""))
  h.assert_equal(count, 200)
  h.assert_equal(sent[1].text:sub(-3), "…")
end

function T.test_the_notify_body_is_the_text_alone()
  -- No `title` (the service's default "SmartThings" says where it came from)
  -- and never `speak`.
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  local deps, sent = recording_http('{"ok":true}')
  client.notify(device, "안녕", deps)
  h.assert_equal(sent.urls[1], "http://192.168.1.20:5001/st/v1/notify")
  h.assert_deep_equal(sent.bodies[1], { text = "안녕" })
end

function T.test_a_notification_is_gated_and_explained_on_the_message_row_only()
  local cases = {
    { status = { service_version = "v1.1.0" }, message = "PC 앱 v1.2.0 필요", sends = 0 },
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
      handlers_for(caps.TOAST).send(driver, device, { args = { text = "hi" } })
    end)
    h.assert_equal(#sent, case.sends, "case " .. i .. " sends")
    h.assert_equal(info_message(device), case.message, "case " .. i)
    h.assert_nil(info_summary(device), "case " .. i .. " touched the summary row")
    -- The row still answers - with what it showed, never with the text that
    -- did not go out - or it spins into "네트워크 오류".
    h.assert_equal(toast_value(device), "없음", "case " .. i .. " row value")
    h.assert_true(h.event_forced(h.emitted(device), caps.TOAST, "lastMessage"), "case " .. i .. " forced")
    h.assert_nil(device:get_field(fields.LAST_TOAST), "case " .. i .. " remembered a text that was not sent")
  end
end

function T.test_a_refused_message_keeps_the_last_one_on_the_row()
  local device = device_with(status_v12())
  device:set_field(fields.LAST_TOAST, "세탁 끝")
  with_notify({ ok = false, kind = "ratelimited" }, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "또 보냄" } })
  end)
  h.assert_equal(toast_value(device), "세탁 끝")
  h.assert_true(h.event_forced(h.emitted(device), caps.TOAST, "lastMessage"))
end

function T.test_an_unreachable_pc_still_answers_the_row()
  -- A network failure is `report_error`'s (connection, summary, message),
  -- and the row answers on top of it.
  local device = device_with(status_v12())
  device.preferences.language = "en"
  local sent = with_notify({ ok = false, kind = "unreachable" }, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = "hi" } })
  end)
  h.assert_equal(#sent, 1)
  h.assert_equal(toast_value(device), "None", "the rest value follows the language")
  h.assert_true(h.event_forced(h.emitted(device), caps.TOAST, "lastMessage"))
  h.assert_equal(h.last_value(h.emitted(device), nil, caps.STATUS, "connection"), "unreachable")
end

function T.test_an_empty_notification_is_not_sent()
  local device = device_with(status_v12())
  local sent = with_notify(nil, function()
    handlers_for(caps.TOAST).send(driver, device, { args = { text = " \n " } })
    handlers_for(caps.TOAST).send(driver, device, { args = {} })
  end)
  h.assert_equal(#sent, 0)
  h.assert_equal(info_message(device), "보낼 문구 없음")
  local answers = 0
  for _, e in ipairs(h.emitted(device)) do
    if e.cap == caps.TOAST and e.attr == "lastMessage" then
      answers = answers + 1
      h.assert_equal(e.value, "없음", "never the empty text")
      h.assert_true((e.options or {}).state_change == true)
    end
  end
  h.assert_equal(answers, 2, "each empty send is answered")
end

function T.test_the_message_row_is_never_empty()
  -- "" is null in the cloud and "-" on the phone (platform notes).
  for _, lang in ipairs({ "ko", "en" }) do
    local device = device_with(status_v12())
    device.preferences.language = lang
    h.assert_equal(rows.shown_toast(device), lang == "ko" and "없음" or "None")
    device:set_field(fields.LAST_TOAST, "")
    h.assert_true(rows.shown_toast(device) ~= "", lang)
    -- An empty text is never remembered either.
    rows.emit_toast(device, "")
    h.assert_true(toast_value(device) ~= "", lang)
    h.assert_true(toast_value(device) ~= nil, lang)
  end
end

function T.test_the_message_row_is_painted_once_per_run_and_by_a_repaint()
  -- `ensure_toast` runs with every poll and push unforced, and the first of
  -- them in a driver run is forced (`fields.ROWS_FORCED`) - which is what gives a
  -- device just moved onto pcToast its value. After that the unchanged value
  -- is not emitted at all (the event budget). A repaint forces it as well.
  local device = device_with(status_v12())
  device:set_field(fields.LAST_TOAST, "안녕")
  rows.ensure_toast(device)
  rows.ensure_toast(device)
  local forced = {}
  for _, e in ipairs(h.emitted(device)) do
    if e.cap == caps.TOAST and e.attr == "lastMessage" then
      h.assert_equal(e.value, "안녕")
      forced[#forced + 1] = (e.options or {}).state_change == true
    end
  end
  h.assert_deep_equal(forced, { true })
  poll.repaint(device)
  h.assert_true(h.event_forced(h.emitted(device), caps.TOAST, "lastMessage"), "a repaint forces it")
end

--------------------------------------------------------------------------------
-- #118: now playing and the playback status
--------------------------------------------------------------------------------

local function playing(media, list)
  local status = status_v12({ media = media })
  status.features = list or { "audio", "media", "nowplaying", "notify", "presets", "awake" }
  return status
end

function T.test_the_playback_status_follows_the_media_block()
  for given, expected in pairs({ playing = "playing", paused = "paused", stopped = "stopped", none = "stopped" }) do
    local events = features.apply_status(playing({ status = given }), { lang = "ko" })
    h.assert_equal(h.event_value(events, "mediaPlayback", "playbackStatus"), expected, given)
  end
  -- No block (before #117), or a status this driver does not know: not emitted.
  h.assert_nil(h.event_value(features.apply_status(status_v12(), {}), "mediaPlayback", "playbackStatus"))
  h.assert_nil(features.playback_status(playing({ status = "buffering-ish" })))
end

function T.test_the_track_data_needs_the_opt_in_and_a_title()
  local status = playing({ status = "playing", title = "Blinding Lights", artist = "The Weeknd",
    album = "", app = "Spotify" })
  h.assert_deep_equal(features.track_data(status, "ko"), { title = "Blinding Lights", artist = "The Weeknd" },
    "an empty album is left out, never sent as \"\"; the app is not a track field")
  -- Opt-in on, nothing playing.
  h.assert_deep_equal(features.track_data(playing({ status = "none" }), "ko"), { title = "재생 중인 미디어 없음" })
  -- Opt-in off: the service sends no title, and says nothing more.
  h.assert_deep_equal(features.track_data(playing({ status = "playing", title = "leaked?" },
    { "audio", "media" }), "en"), { title = "Now playing is off" })
  -- A service without the block: nothing at all.
  h.assert_nil(features.track_data(status_v12(), "ko"))
  h.assert_equal(#features.track_events(status_v12(), "ko"), 0)
end

function T.test_a_long_title_is_cut_and_control_characters_go()
  local data = features.track_data(playing({ status = "playing", title = "a\tb" .. string.rep("가", 200) }), "ko")
  h.assert_equal(data.title:sub(1, 3), "a b")
  h.assert_equal(data.title:sub(-3), "…")
end

function T.test_the_media_group_is_emitted_in_screen_order()
  local events = features.apply_status(playing({ status = "paused", title = "X" }), { lang = "ko" })
  local order = {}
  for _, e in ipairs(events) do
    if features.MEDIA_CAPS[e.cap] and order[#order] ~= e.cap then
      order[#order + 1] = e.cap
    end
  end
  h.assert_deep_equal(order, { "audioTrackData", "mediaPlayback", "mediaTrackControl", "audioVolume", "audioMute" })
end

function T.test_a_refused_media_command_answers_with_the_last_playback_status()
  local device = device_with(playing({ status = "playing" }, { "audio" }))
  with_service(nil, function()
    handlers_for("mediaPlayback").pause(driver, device, { args = {} })
  end)
  local emitted = h.emitted(device)
  h.assert_equal(h.last_value(emitted, nil, "mediaPlayback", "playbackStatus"), "playing")
  h.assert_true(h.event_forced(emitted, "mediaPlayback", "playbackStatus"))
  h.assert_equal(info_summary(device), "미디어 제어 꺼짐")
end

function T.test_a_media_push_repaints_the_group_at_once()
  local push = require "push"
  local _, events = push.apply(state.new(state.ON),
    { type = "media.changed", status = playing({ status = "playing", title = "Song" }) }, { lang = "ko" })
  h.assert_equal(h.event_value(events, "mediaPlayback", "playbackStatus"), "playing")
  h.assert_deep_equal(h.event_value(events, "audioTrackData", "audioTrackData"), { title = "Song" })
end

function T.test_the_media_group_goes_to_the_media_component_when_the_profile_has_one()
  -- #118: the alternative layout (`gen-profiles.js --media-component`).
  local events = features.apply_status(playing({ status = "playing", title = "Song" }), { lang = "ko" })
  local split = h.fake_device({})
  split.profile = { components = { main = { id = "main" }, media = { id = "media" }, awake = { id = "awake" } } }
  emit.rows(split, events)
  local emitted = h.emitted(split)
  h.assert_equal(h.component_value(emitted, "media", "mediaPlayback", "playbackStatus"), "playing")
  h.assert_equal(h.component_value(emitted, "media", "audioVolume", "volume"), 30)
  h.assert_nil(h.event_value(emitted, "mediaPlayback", "playbackStatus"), "not on main")
  -- The rest of the screen stays on main.
  h.assert_true(h.event_value(emitted, caps.PRESET, "names") ~= nil)
  -- And the default layout keeps all of it on main.
  local together = h.fake_device({})
  emit.rows(together, events)
  h.assert_equal(h.event_value(h.emitted(together), "mediaPlayback", "playbackStatus"), "playing")
end

--------------------------------------------------------------------------------
-- components (#107: the emit glue)
--------------------------------------------------------------------------------

function T.test_an_event_for_another_component_goes_there()
  local device = h.fake_device({})
  emit.rows(device, { { cap = "switch", attr = "switch", value = "on", component = "awake" } })
  local emitted = h.emitted(device)
  h.assert_equal(#emitted, 1)
  h.assert_equal(emitted[1].component, "awake")
  h.assert_nil(h.event_value(emitted, "switch", "switch"), "the main switch was not touched")
  h.assert_equal(h.component_value(emitted, "awake", "switch", "switch"), "on")
end

function T.test_an_event_for_a_component_the_profile_lacks_is_skipped()
  -- A desktop's profile has no battery component, and a v1 profile no awake one.
  local device = h.fake_device({})
  emit.rows(device, { { cap = "battery", attr = "battery", value = 80, component = "battery" } })
  h.assert_equal(#h.emitted(device), 0)
  device.profile = { components = { { id = "main" } } }
  emit.rows(device, { { cap = "switch", attr = "switch", value = "on", component = "awake" } })
  h.assert_equal(#h.emitted(device), 0)
end

function T.test_the_row_key_names_the_component()
  h.assert_equal(emit.row_key({ cap = "switch", attr = "switch" }), "switch.switch")
  h.assert_equal(emit.row_key({ cap = "switch", attr = "switch", component = "main" }), "switch.switch")
  h.assert_equal(emit.row_key({ cap = "switch", attr = "switch", component = "awake" }), "awake/switch.switch")
end

--------------------------------------------------------------------------------
-- which PC app the driver wants (edge-driver.md "버전 짝 맞춤")
--------------------------------------------------------------------------------

function T.test_service_versions_compare_on_major_minor_patch()
  h.assert_deep_equal(features.parse_version("v1.2.0"), { 1, 2, 0 })
  h.assert_deep_equal(features.parse_version("1.2.0"), { 1, 2, 0 })
  -- A prerelease is its release: our rc builds do not ask for themselves.
  h.assert_deep_equal(features.parse_version("v1.2.0-rc14"), { 1, 2, 0 })
  h.assert_deep_equal(features.parse_version("V1.10.3+build.7"), { 1, 10, 3 })
  h.assert_deep_equal(features.parse_version("1.2"), { 1, 2, 0 })
  for _, unknown in ipairs({ "dev", "", "v", "release-1", 12, {} }) do
    h.assert_nil(features.parse_version(unknown), tostring(unknown))
  end
  h.assert_nil(features.parse_version(nil))

  for _, c in ipairs({
    { "v1.1.2", "1.2.0", true },
    { "v1.1.9-rc1", "1.2.0", true },
    { "v1.2.0", "1.2.0", false },
    { "v1.2.0-rc14", "1.2.0", false },
    { "1.2.1", "1.2.0", false },
    { "v1.10.0", "1.9.0", false }, -- numbers, not strings
    { "v0.9.9", "1.0.0", true },
    { "v2.0.0", "1.99.99", false },
  }) do
    h.assert_equal(features.version_below(c[1], c[2]), c[3], c[1] .. " < " .. c[2])
  end
  h.assert_nil(features.version_below("dev", "1.2.0"))
  h.assert_nil(features.version_below(nil, "1.2.0"))
end

function T.test_needs_app_update_flags_only_a_known_older_pc_app()
  local with = { "awake" }
  for _, c in ipairs({
    { "an old service", { service_version = "v1.1.2" }, true },
    { "an old service that somehow lists features", { service_version = "v1.1.2", features = with }, true },
    { "the recommended one", { service_version = "v1.2.0", features = with }, false },
    { "an rc of it", { service_version = "v1.2.0-rc14", features = with }, false },
    { "without the v", { service_version = "1.2.0", features = with }, false },
    { "a newer one", { service_version = "v1.3.0", features = with }, false },
    { "a dev build with features", { service_version = "dev", features = with }, false },
    { "no version, with features", { features = with }, false },
    -- No `features` at all is a service older than v1.2.0, whatever it says.
    { "no version, no features", {}, true },
    { "a dev build without features", { service_version = "dev" }, true },
  }) do
    h.assert_equal(features.needs_app_update(c[2]), c[3], c[1])
  end
  h.assert_false(features.needs_app_update(nil), "no body says nothing")
end

-- The refusals and the watch card ask for RECOMMENDED_SERVICE_VERSION, by
-- name; no string has a version of its own.
function T.test_the_notes_name_the_recommended_version()
  h.assert_equal(features.note_text("ko", "needs_service"), "PC 앱 v1.2.0 필요")
  h.assert_equal(features.note_text("en", "needs_service"), "Needs PC app v1.2.0")
  -- Any other key is formatted with its own arguments.
  h.assert_equal(features.note_text("ko", "preset_empty", 3), i18n.t("ko", "preset_empty", 3))
  h.assert_equal(features.preset_names({ service_version = "v1.1.2" }, "en"), "Needs PC app v1.2.0")
  h.assert_equal(features.apps_summary({ service_version = "v1.1.2" }, "ko"), "PC 앱 v1.2.0 필요")
end

-- Bumping the constant is the whole job (edge-driver.md "버전 짝 맞춤"): every
-- notice follows it, and a PC app that was new enough becomes "old".
function T.test_changing_the_recommended_version_changes_every_text()
  local saved = features.RECOMMENDED_SERVICE_VERSION
  local ok, err = pcall(function()
    features.RECOMMENDED_SERVICE_VERSION = "9.9.9"
    local current = status_v12({ uptime_seconds = 300, wol = { ready = true } })

    h.assert_true(features.needs_app_update(current))
    -- The rows of a status body.
    local events = state.apply_status(state.new(state.ON), current, { lang = "ko", now = "21:00" })
    h.assert_equal(h.event_value(events, caps.STATUS, "summary"), "연결됨 · 5분 · 앱 업데이트 필요")
    h.assert_equal(h.event_value(events, caps.STATUS, "message"), "PC 앱을 v9.9.9 이상으로 업데이트하세요")
    events = state.apply_status(state.new(state.ON), current, { lang = "en", now = "21:00" })
    h.assert_equal(h.event_value(events, caps.STATUS, "message"), "Update the PC app to v9.9.9 or newer")

    -- A feature the v1.2.0 PC does not list is a newer release's, not one
    -- this PC lacks.
    local extras = features.remember(state.new(), status_v12({ features = { "awake" } })).extras
    h.assert_equal(features.refusal(extras, "preset"), "needs_service")
    h.assert_equal(features.refusal(extras, "volume"), "needs_service")
    h.assert_equal(features.note_text("ko", "needs_service"), "PC 앱 v9.9.9 필요")
    h.assert_equal(features.note_text("en", "needs_service"), "Needs PC app v9.9.9")
    h.assert_equal(features.apps_summary({ service_version = "v1.1.2" }, "ko"), "PC 앱 v9.9.9 필요")
    h.assert_equal(features.preset_names({ service_version = "v1.1.2" }, "en"), "Needs PC app v9.9.9")

    -- And through a handler, as the hub calls it.
    local device = device_with({ service_version = "v1.1.2" })
    local calls = with_service(nil, function()
      handlers_for("audioVolume").volumeUp(driver, device, { args = {} })
    end)
    h.assert_equal(#calls.actions, 0)
    h.assert_equal(info_summary(device), "PC 앱 v9.9.9 필요")
  end)
  features.RECOMMENDED_SERVICE_VERSION = saved
  if not ok then
    error(err, 0)
  end
  -- Back at 1.2.0, the same PC is new enough again.
  h.assert_false(features.needs_app_update(status_v12()))
  local extras = features.remember(state.new(), status_v12({ features = { "awake" } })).extras
  h.assert_equal(features.refusal(extras, "preset"), "feature_missing")
end

-- C3: a shutdown or restart stops the apps; the helper push.lua uses.
function T.test_stop_watch_moves_only_running_slots()
  local input = { "running", "stopped", "empty", "running", nil }
  local out, records = features.stop_watch(input)
  h.assert_deep_equal(out, { "stopped", "stopped", "empty", "stopped" })
  h.assert_deep_equal(input, { "running", "stopped", "empty", "running" }, "the input is not modified")
  h.assert_equal(#records, 2)
  for i, attr in ipairs({ "slotOne", "slotFour" }) do
    h.assert_equal(records[i].attr, attr)
    h.assert_equal(records[i].value, "stopped")
    h.assert_equal(records[i].component, WATCH)
    h.assert_true(records[i].force, attr .. " forced")
  end
  local none, empty = features.stop_watch(nil)
  h.assert_nil(none)
  h.assert_deep_equal(empty, {})
end

return T
