-- The event budget (platform notes "이벤트 예산(rate limit)").
--
-- Measured on the hub 2026-10-01: the platform counts every `emit_event`
-- against a per-device budget, including the ones the hub then drops as
-- unchanged, and once it is spent it drops events after the hub's state cache
-- has taken them. So the driver deduplicates itself (`poll.SENT_FIELD`),
-- commands share their answer polls (`poll.answer`), and the rows a lost
-- event matters on are re-sent forced every so often (`poll.resync`).
--
-- These tests drive whole polls and whole command handlers, with the HTTP
-- layer swapped for a fake PC, and count what the device was told.

local h = require "helpers"
local Driver = require "st.driver"
local caps = require "caps"
local client = require "client"
local features = require "features"
local log = require "log"
local poll = require "poll"
local state = require "state"

local init_driver = require "init"

local T = {}

--------------------------------------------------------------------------------
-- fixtures
--------------------------------------------------------------------------------

local function copy(value)
  if type(value) ~= "table" then
    return value
  end
  local out = {}
  for k, v in pairs(value) do
    out[k] = copy(v)
  end
  return out
end

--- A v1.2.0 PC with everything on: audio, media with now playing, presets,
--- activity, keep-awake, and a logged-in session.
local function pc_status()
  return {
    protocol = 1, service_version = "v1.2.0", power = "on", secret_set = true,
    uptime_seconds = 3600,
    features = { "audio", "media", "nowplaying", "notify", "presets", "activity", "awake" },
    audio = { available = true, volume = 30, muted = false, device = "스피커" },
    media = { status = "playing", title = "Blinding Lights", artist = "The Weeknd", app = "Spotify" },
    presets = { { slot = 1, name = "게임 모드" }, { slot = 2, name = "방송 시작" } },
    activity = { enabled = true, kind = "game", labels = { "Steam" } },
    awake = { on = false },
    session = { exposed = true, locked = false, idle_seconds = 0, user = "kim" },
    wol = {
      ready = true,
      selected = { name = "Ethernet", mac = "AA:BB:CC:DD:EE:FF", ip = "192.168.1.20",
        wol_enabled = true, wol_capable = true, source = "auto" },
    },
  }
end

local function new_device()
  local device = h.fake_device({ ipAddress = "192.168.1.20", secret = "s", language = "ko" })
  device.device_network_id = "pc-control-budget"
  device.id = "budget-device"
  -- An installed device on the current generation of rows: no repaint of its
  -- own on the first poll, so the counts below are the polls' alone.
  device:set_field(poll.ROWS_FIELD, poll.ROWS_VERSION)
  -- ...and with the command list resting where it was left (persisted), so
  -- `ensure_action` owes no repeat (#93 follow-up) that would show up below.
  device:set_field(poll.ACTION_FIELD, state.ACTION_NONE)
  poll.set_state(device, state.new(state.ON))
  return device
end

--- Run `fn(pc)` against a fake PC whose status is `pc.status`. Actions change
--- it the way the real service would; the clock (`pc.now`, epoch seconds) and
--- the `lastSeen` time (`pc.seen`) only move when the test moves them.
local function with_pc(fn)
  local pc = { status = pc_status(), now = 1000000, seen = "14:05:12", actions = {}, polls = 0 }
  local original = {
    get_status = client.get_status, action = client.action,
    now = poll.now, wallclock = poll.wallclock,
  }
  client.get_status = function()
    pc.polls = pc.polls + 1
    return true, copy(pc.status), nil
  end
  client.action = function(_, command, value)
    pc.actions[#pc.actions + 1] = command
    local audio = pc.status.audio
    if command == "mute" then
      audio.muted = true
    elseif command == "unmute" then
      audio.muted = false
    elseif command == "volume" then
      audio.volume = value
    end
    return true, {}, nil
  end
  poll.now = function() return pc.seen end
  poll.wallclock = function() return pc.now end
  local ok, err = pcall(fn, pc)
  client.get_status, client.action = original.get_status, original.action
  poll.now, poll.wallclock = original.now, original.wallclock
  if not ok then
    error(err, 0)
  end
end

local function handlers_for(id)
  return init_driver.capability_handlers[id]
end

--- What was emitted since `mark` (an index into `device.emitted`).
local function since(device, mark)
  local all = h.emitted(device)
  local out = {}
  for i = mark + 1, #all do
    out[#out + 1] = all[i]
  end
  return out
end

local function keys_of(events)
  local out = {}
  for _, e in ipairs(events) do
    out[#out + 1] = poll.row_key(e)
  end
  table.sort(out)
  return out
end

local function count_forced(events, key)
  local n = 0
  for _, e in ipairs(events) do
    if poll.row_key(e) == key and (e.options or {}).state_change == true then
      n = n + 1
    end
  end
  return n
end

local MUTE = features.CAP_MUTE .. ".mute"
local VOLUME = features.CAP_VOLUME .. ".volume"

--------------------------------------------------------------------------------
-- dedupe
--------------------------------------------------------------------------------

function T.test_the_first_poll_of_a_run_sends_every_row_forced()
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local first = h.emitted(device)
    h.assert_true(#first > 30, "the first poll paints every row, got " .. #first)
    for _, e in ipairs(first) do
      h.assert_true((e.options or {}).state_change == true,
        poll.row_key(e) .. ": the first emit of a run is forced (FIRST_FIELD)")
    end
  end)
end

function T.test_a_steady_poll_sends_nothing_or_only_the_time()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    poll.once(d, device)
    h.assert_deep_equal(keys_of(since(device, mark)), {},
      "an identical status at the same clock time emits nothing")
    pc.seen = "14:05:42"
    mark = #device.emitted
    poll.once(d, device)
    h.assert_deep_equal(keys_of(since(device, mark)), { caps.STATUS .. ".lastSeen" },
      "30 s later only the time moved")
  end)
end

function T.test_a_changed_value_emits_exactly_that_row()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    pc.status.audio.volume = 55
    local mark = #device.emitted
    poll.once(d, device)
    local out = since(device, mark)
    h.assert_deep_equal(keys_of(out), { VOLUME })
    h.assert_equal(out[1].value, 55)
    h.assert_false((out[1].options or {}).state_change == true, "an ordinary update stays unforced")
  end)
end

function T.test_forced_rows_always_go_out()
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    poll.once(d, device, { force = { [MUTE] = true, [VOLUME] = true } })
    local out = since(device, mark)
    h.assert_deep_equal(keys_of(out), { MUTE, VOLUME })
    h.assert_equal(count_forced(out, MUTE), 1)
    h.assert_equal(count_forced(out, VOLUME), 1)
    -- And `force = true` (a repaint) sends everything again.
    mark = #device.emitted
    poll.once(d, device, { force = true })
    h.assert_true(#since(device, mark) > 30, "a forced poll is a whole repaint")
  end)
end

function T.test_tables_compare_by_content()
  h.assert_equal(poll.signature({ b = 1, a = { "x", "y" } }), poll.signature({ a = { "x", "y" }, b = 1 }))
  h.assert_true(poll.signature({ "x", "y" }) ~= poll.signature({ "y", "x" }), "order of a list counts")
  h.assert_equal(poll.signature(30), poll.signature(30.0))
  h.assert_true(poll.signature(1) ~= poll.signature("1"))
  h.assert_true(poll.signature(false) ~= poll.signature("false"))
  local device = h.fake_device()
  local row = function(value)
    return { { cap = features.CAP_TRACK_DATA, attr = "audioTrackData", value = value } }
  end
  poll.emit(device, row({ title = "A", artist = "B" }))
  poll.emit(device, row({ artist = "B", title = "A" }))
  h.assert_equal(#device.emitted, 1, "the same table, rebuilt, is not emitted twice")
  poll.emit(device, row({ title = "C", artist = "B" }))
  h.assert_equal(#device.emitted, 2)
end

function T.test_a_repaint_forgets_what_was_sent()
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    h.assert_equal(poll.sent_value(device, "switch.switch"), "on")
    -- A profile change: `repaint` runs (repaint_soon, ensure_rows).
    poll.repaint(device)
    h.assert_nil(poll.sent_value(device, "switch.switch"),
      "the new profile's record is empty, so nothing counts as sent")
    local mark = #device.emitted
    poll.once(d, device)
    local out = since(device, mark)
    h.assert_equal(h.event_value(out, "switch", "switch"), "on",
      "the unchanged switch goes out once more for the new profile")
    h.assert_equal(h.event_value(out, caps.POWER_STATE, "powerState"), state.ON)
    -- ...and once only.
    mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), 0)
  end)
end

function T.test_repaint_soon_clears_the_cache_as_well()
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    poll.repaint_soon(d, device)
    local out = since(device, mark)
    h.assert_equal(count_forced(out, "switch.switch"), 1, "the forced poll repaints the switch")
    -- The late repaint, 15 s later, goes through it all again.
    mark = #device.emitted
    h.fire_last(d, "repaint-late-15")
    h.assert_equal(count_forced(since(device, mark), "switch.switch"), 1)
  end)
end

--------------------------------------------------------------------------------
-- commands
--------------------------------------------------------------------------------

--- Four mute/unmute commands as they were measured on 2026-10-01: 1.5-2 s
--- apart, so each answer window has closed before the next one lands. Before
--- the event budget every one of them re-sent every row; now each sends what
--- changed plus the rows it answers.
function T.test_four_spaced_commands_send_a_bounded_number_of_events()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    local sequence = { "mute", "unmute", "mute", "unmute" }
    for i, command in ipairs(sequence) do
      pc.now = pc.now + 2
      pc.seen = string.format("14:05:%02d", 14 + 2 * i)
      handlers_for("audioMute")[command](d, device, { command = command, args = {} })
      h.fire_last(d, "answer-poll")
    end
    local out = since(device, mark)
    h.assert_deep_equal(pc.actions, sequence)
    h.assert_equal(pc.polls, 1 + 4, "one answer poll per command, none stacked")
    h.assert_equal(count_forced(out, MUTE), 4, "every command's row is answered forced")
    h.assert_equal(h.last_value(out, nil, features.CAP_MUTE, "mute"), "unmuted")
    -- mute + volume (the rows the command answers) + lastSeen, per command.
    h.assert_equal(#out, 12, "4 commands -> " .. #out .. " events")
  end)
end

--- Four commands inside one window (a routine, or fast taps): two polls.
function T.test_four_rapid_commands_share_their_answer_polls()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    local sequence = { "mute", "unmute", "mute", "unmute" }
    for _, command in ipairs(sequence) do
      handlers_for("audioMute")[command](d, device, { command = command, args = {} })
    end
    h.assert_equal(pc.polls, 1 + 1, "only the first command polled at once")
    h.assert_true(h.fire_last(d, "answer-poll"), "the window closes")
    h.assert_equal(pc.polls, 1 + 2, "and the other three are answered by one poll")
    -- That poll opened another window, which closes with nothing owed.
    h.assert_true(h.fire_last(d, "answer-poll"))
    h.assert_equal(pc.polls, 1 + 2)
    h.assert_false(h.fire_last(d, "answer-poll"), "nothing left pending")
    local out = since(device, mark)
    h.assert_equal(count_forced(out, MUTE), 2, "both answer polls force the mute row")
    h.assert_equal(h.last_value(out, nil, features.CAP_MUTE, "mute"), "unmuted")
    h.assert_true(#out <= 6, "4 rapid commands -> " .. #out .. " events")
    -- The next command after the windows closed is answered at once again.
    handlers_for("audioMute").mute(d, device, { command = "mute", args = {} })
    h.assert_equal(pc.polls, 1 + 3)
  end)
end

function T.test_a_single_command_is_still_answered_at_once()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    handlers_for("audioVolume").setVolume(d, device, { command = "setVolume", args = { volume = 30 } })
    local out = since(device, mark)
    h.assert_equal(pc.polls, 2)
    h.assert_equal(count_forced(out, VOLUME), 1, "the unchanged volume is answered forced")
  end)
end

--------------------------------------------------------------------------------
-- resync
--------------------------------------------------------------------------------

local function resync_keys()
  local out = {}
  for _, key in ipairs(poll.RESYNC_ROWS) do
    out[#out + 1] = key
  end
  table.sort(out)
  return out
end

function T.test_every_ten_minutes_the_user_facing_rows_go_out_forced()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    pc.now = pc.now + poll.RESYNC_SECONDS - 1
    local mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), 0, "not due yet")
    pc.now = pc.now + 1
    mark = #device.emitted
    poll.once(d, device)
    local out = since(device, mark)
    h.assert_deep_equal(keys_of(out), resync_keys())
    h.assert_true(#out <= 8)
    for _, e in ipairs(out) do
      h.assert_true((e.options or {}).state_change == true, poll.row_key(e) .. " forced")
    end
    h.assert_equal(h.component_value(out, features.AWAKE_COMPONENT, "switch", "switch"), "off")
    h.assert_equal(h.event_value(out, features.CAP_PLAYBACK, "playbackStatus"), "playing")
    -- At most once per window.
    mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), 0)
  end)
end

function T.test_the_resync_also_runs_while_the_pc_is_off()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    client.get_status = function() return false, nil, "unreachable" end
    pc.now = pc.now + poll.RESYNC_SECONDS
    local mark = #device.emitted
    poll.once(d, device)
    local out = since(device, mark)
    h.assert_equal(count_forced(out, "switch.switch"), 1)
    h.assert_equal(count_forced(out, caps.POWER_STATE .. ".powerState"), 1)
  end)
end

function T.test_a_burst_of_commands_brings_the_resync_forward()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    for i = 1, 3 do
      pc.now = pc.now + 2
      handlers_for("audioMute")[i % 2 == 1 and "mute" or "unmute"](d, device, { args = {} })
      h.fire_last(d, "answer-poll")
    end
    local timer
    for _, t in ipairs(d.timers) do
      if t.name == "resync-burst" and not t.cancelled then
        timer = timer and error("two burst timers") or t
      end
    end
    h.assert_true(timer ~= nil, "three commands in 10 s schedule a resync")
    h.assert_equal(timer.delay, poll.BURST_RESYNC_SECONDS)
    local mark = #device.emitted
    h.fire_last(d, "resync-burst")
    local out = since(device, mark)
    h.assert_deep_equal(keys_of(out), resync_keys())
    h.assert_equal(h.event_value(out, features.CAP_MUTE, "mute"), "muted")
    -- The regular one waits a whole window from there.
    pc.now = pc.now + 60
    mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), 0)
  end)
end

function T.test_two_spaced_commands_are_no_burst()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    for _ = 1, 3 do
      pc.now = pc.now + 6
      handlers_for("audioMute").mute(d, device, { args = {} })
      h.fire_last(d, "answer-poll")
    end
    for _, t in ipairs(d.timers) do
      h.assert_true(t.name ~= "resync-burst", "three commands over 18 s are not a burst")
    end
  end)
end

--------------------------------------------------------------------------------
-- the budget guard
--------------------------------------------------------------------------------

local function budget_warnings()
  local n = 0
  for _, entry in ipairs(log.entries) do
    if entry.level == "warn" and tostring(entry[1]):find("event budget", 1, true) then
      n = n + 1
    end
  end
  return n
end

function T.test_the_guard_warns_once_per_window_and_drops_nothing()
  with_pc(function(pc)
    log.reset()
    local device = h.fake_device()
    local rows = {}
    for i = 1, poll.BUDGET_EVENTS + 5 do
      rows[#rows + 1] = { cap = caps.STATUS, attr = "message", value = "m" .. i }
    end
    poll.emit(device, rows)
    h.assert_equal(#device.emitted, poll.BUDGET_EVENTS + 5, "the guard only logs")
    h.assert_equal(budget_warnings(), 1)
    poll.emit(device, { { cap = caps.STATUS, attr = "message", value = "again" } })
    h.assert_equal(budget_warnings(), 1, "once per window")
    pc.now = pc.now + poll.BUDGET_SECONDS
    poll.emit(device, { { cap = caps.STATUS, attr = "message", value = "later" } })
    h.assert_equal(budget_warnings(), 1, "a new window starts quiet")
  end)
end

function T.test_a_steady_poll_stays_under_the_guard()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    pc.now = pc.now + 30
    log.reset()
    for i = 1, 4 do
      pc.seen = string.format("14:06:%02d", i)
      handlers_for("audioMute")[i % 2 == 1 and "mute" or "unmute"](d, device, { args = {} })
      h.fire_last(d, "answer-poll")
    end
    h.assert_equal(budget_warnings(), 0, "four commands no longer trip it")
  end)
end

return T
