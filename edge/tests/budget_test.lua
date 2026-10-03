-- The event budget (platform notes "이벤트 예산(rate limit)").
--
-- Measured on the hub 2026-10-01: the platform counts every `emit_event`
-- against a per-device budget, including the ones the hub then drops as
-- unchanged, and once it is spent it drops events after the hub's state cache
-- has taken them. So the driver deduplicates itself (`fields.ROWS_SENT`),
-- commands share their answer polls (`poll.answer`), and the rows a lost
-- event matters on are re-sent forced after a burst (`emit.resync`).
-- 2026-10-01 (the v5 -> v6 migration lost `pcApps.summary` for good): every
-- row is re-sent forced once per cycle (`poll.rotate_due`), and a repaint
-- goes out in batches (`emit.paint`).
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
local emit = require "device.emit"
local rows = require "device.rows"
local clock = require "device.clock"

local fields = require "device.fields"
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
    activity = { enabled = true, apps = { { slot = 1, id = "steam.exe", label = "Steam", running = true } },
      top = "steam.exe" },
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
  -- Every test's device has the same id: the battery votes one test left
  -- behind must not decide another's profile.
  require("profiles").reset()
  local device = h.fake_device({ ipAddress = "192.168.1.20", secret = "s", language = "ko" })
  device.device_network_id = "pc-control-budget"
  device.id = "budget-device"
  -- An installed device on the current generation of rows: no repaint of its
  -- own on the first poll, so the counts below are the polls' alone.
  device:set_field(fields.ROWS_PAINTED, poll.ROWS_VERSION)
  -- ...and with the command list resting where it was left (persisted), so
  -- `ensure_action` owes no repeat (#93 follow-up) that would show up below.
  device:set_field(fields.LAST_ACTION, state.ACTION_NONE)
  fields.set_state(device, state.new(state.ON))
  return device
end

--- Run `fn(pc)` against a fake PC whose status is `pc.status`. Actions change
--- it the way the real service would; the clock (`pc.now`, epoch seconds) and
--- the `lastSeen` time (`pc.seen`) only move when the test moves them.
local function with_pc(fn)
  local pc = { status = pc_status(), now = 1000000, seen = "14:05:12", actions = {}, polls = 0 }
  local original = {
    get_status = client.get_status, action = client.action,
    now = clock.now, wallclock = clock.wallclock,
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
  clock.now = function() return pc.seen end
  clock.wallclock = function() return pc.now end
  local ok, err = pcall(fn, pc)
  client.get_status, client.action = original.get_status, original.action
  clock.now, clock.wallclock = original.now, original.wallclock
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
    out[#out + 1] = emit.row_key(e)
  end
  table.sort(out)
  return out
end

local function count_forced(events, key)
  local n = 0
  for _, e in ipairs(events) do
    if emit.row_key(e) == key and (e.options or {}).state_change == true then
      n = n + 1
    end
  end
  return n
end

local MUTE = features.CAP_MUTE .. ".mute"
local VOLUME = features.CAP_VOLUME .. ".volume"
local LAST_SEEN = caps.STATUS .. ".lastSeen"
-- #123: the watch card's summary (was `pcApps.summary` on main).
local APPS_SUMMARY = features.WATCH_COMPONENT .. "/" .. caps.WATCH .. ".summary"

-- What a hub keeps over a driver restart (`set_field(…, { persist = true })`).
local PERSISTED = { fields.ROWS_PAINTED, fields.LAST_ACTION, fields.PLAN_COMMAND, fields.SERVICE_VERSION,
  fields.LAST_SEEN }

--------------------------------------------------------------------------------
-- a clock for whole minutes of driver life
--------------------------------------------------------------------------------

--- Stamp every timer `d` sets with when it is due on `pc`'s clock, so
--- `run_until` can run them in order.
local function clocked(d, pc)
  local delay, schedule = d.call_with_delay, d.call_on_schedule
  function d:call_with_delay(seconds, fn, name)
    local timer = delay(self, seconds, fn, name)
    timer.due = pc.now + seconds
    return timer
  end
  function d:call_on_schedule(interval, fn, name)
    local timer = schedule(self, interval, fn, name)
    timer.due = pc.now + interval
    return timer
  end
  return d
end

--- Run every timer due up to `horizon`, earliest first, moving `pc.now` (and
--- the `lastSeen` time with it, as on the hub). Schedules repeat.
local function run_until(d, pc, horizon)
  for _ = 1, 10000 do
    local due
    for _, timer in ipairs(d.timers) do
      if not timer.cancelled and timer.due and timer.due <= horizon
          and (not due or timer.due < due.due) then
        due = timer
      end
    end
    if not due then
      break
    end
    pc.now = math.max(pc.now, due.due)
    pc.seen = string.format("t+%d", math.floor(pc.now))
    if due.kind == "schedule" then
      due.due = due.due + due.interval
    else
      due.cancelled = true
    end
    due.fn()
  end
  pc.now = horizon
end

--- Record when (on `pc`'s clock) `device` emits: one `{ at, key, forced }` per
--- event from now on.
local function stamped(device, pc)
  local trace = {}
  local emit_event, emit_component_event = device.emit_event, device.emit_component_event
  local function note(event, component)
    local key = event.capability .. "." .. event.attribute
    if component and component.id ~= "main" then
      key = component.id .. "/" .. key
    end
    trace[#trace + 1] = { at = pc.now, key = key, forced = (event.options or {}).state_change == true }
  end
  function device:emit_event(event)
    note(event)
    return emit_event(self, event)
  end
  function device:emit_component_event(component, event)
    note(event, component)
    return emit_component_event(self, component, event)
  end
  return trace
end

--- The most events in any `seconds`-long window of a `stamped` trace.
local function busiest(trace, seconds)
  local most = 0
  for i = 1, #trace do
    local n = 0
    for j = i, #trace do
      if trace[j].at < trace[i].at + seconds then
        n = n + 1
      end
    end
    most = math.max(most, n)
  end
  return most
end

--------------------------------------------------------------------------------
-- dedupe
--------------------------------------------------------------------------------

function T.test_the_first_poll_of_a_run_sends_every_row_forced()
  -- On a hub with no record of an earlier run (a new device, a first install).
  -- After a restart the hub's state cache cuts this down, see #129 below.
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local first = h.emitted(device)
    h.assert_true(#first > 30, "the first poll paints every row, got " .. #first)
    for _, e in ipairs(first) do
      h.assert_true((e.options or {}).state_change == true,
        emit.row_key(e) .. ": the first emit of a run is forced (FIRST_FIELD)")
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
  h.assert_equal(emit.signature({ b = 1, a = { "x", "y" } }), emit.signature({ a = { "x", "y" }, b = 1 }))
  h.assert_true(emit.signature({ "x", "y" }) ~= emit.signature({ "y", "x" }), "order of a list counts")
  h.assert_equal(emit.signature(30), emit.signature(30.0))
  h.assert_true(emit.signature(1) ~= emit.signature("1"))
  h.assert_true(emit.signature(false) ~= emit.signature("false"))
  local device = h.fake_device()
  local row = function(value)
    return { { cap = features.CAP_TRACK_DATA, attr = "audioTrackData", value = value } }
  end
  emit.rows(device, row({ title = "A", artist = "B" }))
  emit.rows(device, row({ artist = "B", title = "A" }))
  h.assert_equal(#device.emitted, 1, "the same table, rebuilt, is not emitted twice")
  emit.rows(device, row({ title = "C", artist = "B" }))
  h.assert_equal(#device.emitted, 2)
end

function T.test_a_repaint_forgets_what_was_sent()
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    h.assert_equal(emit.sent_value(device, "switch.switch"), "on")
    -- A profile change: `repaint` runs (repaint_soon, ensure_rows).
    poll.repaint(device)
    h.assert_nil(emit.sent_value(device, "switch.switch"),
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
    h.assert_equal(count_forced(out, "switch.switch"), 1, "the switch is in the first batch, forced as the first of the generation")
    h.fire_all(d, "repaint-batch")
    -- The follow-up, 30 s later, re-sends it with the other rows that matter (resync).
    mark = #device.emitted
    h.assert_true(h.fire_last(d, "repaint-late-30"))
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
  for _, key in ipairs(emit.RESYNC_ROWS) do
    out[#out + 1] = key
  end
  table.sort(out)
  return out
end

--- The row keys this run has sent on `device` (the rotation's set).
local function sent_keys(device)
  local out = {}
  for key in pairs(device:get_field(fields.ROWS_SENT) or {}) do
    out[#out + 1] = key
  end
  table.sort(out)
  return out
end

function T.test_the_rotation_size_goes_round_in_ten_minutes()
  -- 600 s at the poll interval, rounded up, at most ROTATE_MAX.
  h.assert_equal(emit.rotate_size(42, 30), 3, "the default: 20 polls for 42 rows")
  h.assert_equal(emit.rotate_size(40, 30), 2)
  h.assert_equal(emit.rotate_size(42, 10), 1)
  h.assert_equal(emit.rotate_size(42, 60), 5)
  h.assert_equal(emit.rotate_size(42, 300), emit.ROTATE_MAX, "a 5 min interval takes longer than a cycle")
  h.assert_equal(emit.rotate_size(1, 30), 1)
  h.assert_equal(emit.rotate_size(0, 30), 0)
  h.assert_true(emit.ROTATE_MAX <= 5)
end

function T.test_the_rotation_re_sends_every_row_within_ten_minutes()
  -- The 2026-10-01 migration: a row lost after the hub's cache took it stays
  -- wrong for as long as nothing re-sends it. Now every row this run has
  -- sent goes out forced again within one cycle, a few per poll.
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local row_keys = sent_keys(device)
    local size = emit.rotate_size(#row_keys, poll.DEFAULT_INTERVAL)
    h.assert_true(size >= 1 and size <= 3, "rows per poll at 30 s: " .. size)
    local forced = {}
    local polls = emit.ROTATE_SECONDS // poll.DEFAULT_INTERVAL
    for i = 1, polls do
      pc.now = pc.now + poll.DEFAULT_INTERVAL
      pc.seen = "t" .. i
      local mark = #device.emitted
      poll.once(d, device)
      local out = since(device, mark)
      local extra = 0
      for _, e in ipairs(out) do
        local key = emit.row_key(e)
        if key ~= LAST_SEEN then
          extra = extra + 1
          h.assert_true((e.options or {}).state_change == true, key .. " is re-sent forced")
          forced[key] = (forced[key] or 0) + 1
        end
      end
      h.assert_equal(extra, size,
        "poll " .. i .. ": the time and " .. size .. " rows")
    end
    for _, key in ipairs(row_keys) do
      if key ~= LAST_SEEN then
        h.assert_true(forced[key] ~= nil, key .. " was not re-sent within " .. emit.ROTATE_SECONDS .. " s")
      end
    end
    -- And no row twice before every row has had its turn.
    local most = 0
    for _, n in pairs(forced) do
      most = math.max(most, n)
    end
    h.assert_true(most <= 2, "round robin, not the same rows again: " .. most)
  end)
end

function T.test_the_rotation_waits_a_poll_interval_between_steps()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    -- Polls closer together than the interval (answer polls, a refresh)
    -- add no rotation step.
    pc.now = pc.now + 23
    local mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), 0, "not due 23 s after")
    pc.now = pc.now + 1
    mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), emit.rotate_size(#sent_keys(device), 30),
      "due 24 s after (a fifth less than the interval, for timer jitter)")
    pc.now = pc.now + 10
    mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), 0)
  end)
end

function T.test_the_rotation_also_runs_while_the_pc_is_off()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local row_keys = sent_keys(device)
    client.get_status = function() return false, nil, "unreachable" end
    local forced, seen = {}, {}
    for _ = 1, emit.ROTATE_SECONDS // poll.DEFAULT_INTERVAL + 1 do
      pc.now = pc.now + poll.DEFAULT_INTERVAL
      local mark = #device.emitted
      poll.once(d, device)
      for _, e in ipairs(since(device, mark)) do
        seen[emit.row_key(e)] = true
        if (e.options or {}).state_change == true then
          forced[emit.row_key(e)] = e.value
        end
      end
    end
    -- With the values last sent: the volume the PC had before it went away.
    h.assert_equal(forced[VOLUME], 30)
    h.assert_equal(forced[caps.POWER_STATE .. ".powerState"], emit.sent_value(device, caps.POWER_STATE .. ".powerState"))
    -- Every row went out again: forced by the rotation, or as a change (the
    -- summary counts the minutes since the PC last answered).
    for _, key in ipairs(row_keys) do
      h.assert_true(seen[key] ~= nil, key .. " re-sent while the PC is off")
    end
  end)
end

function T.test_the_rotation_skips_a_row_this_poll_just_sent()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    -- Put the cursor right before lastSeen: the next step would pick it.
    local row_keys = sent_keys(device)
    for i, key in ipairs(row_keys) do
      if key == LAST_SEEN then
        device:set_field(fields.ROTATE_CURSOR, row_keys[i - 1])
      end
    end
    pc.now = pc.now + 30
    pc.seen = "14:05:42"
    local mark = #device.emitted
    poll.once(d, device)
    local out = since(device, mark)
    local times = 0
    for _, e in ipairs(out) do
      if emit.row_key(e) == LAST_SEEN then
        times = times + 1
        h.assert_false((e.options or {}).state_change == true, "the time went out as an ordinary change")
      end
    end
    h.assert_equal(times, 1, "the time once, not again as a rotation row")
    h.assert_equal(#out, 1 + emit.rotate_size(#row_keys, 30), "the step took the next rows instead")
  end)
end

function T.test_a_row_the_cloud_lost_heals_within_the_cycle_even_across_a_restart()
  -- The 2026-10-01 finding, end to end: the platform drops the summary
  -- after the hub's cache took it. Neither this run's dedupe nor a restart
  -- (which takes the hub's cache as sent) would ever send it again.
  with_pc(function(pc)
    local device = h.with_state_cache(new_device())
    local cloud = {}
    local emit_event, emit_component_event = device.emit_event, device.emit_component_event
    local dropped = false
    function device:emit_event(event)
      cloud[event.capability .. "." .. event.attribute] = event.value
      return emit_event(self, event)
    end
    function device:emit_component_event(component, event)
      local key = component.id .. "/" .. event.capability .. "." .. event.attribute
      if key == APPS_SUMMARY and not dropped then
        dropped = true -- the hub took it (the cache wrapper below), the cloud did not
        local by_cap = self.state_cache[component.id] or {}
        self.state_cache[component.id] = by_cap
        by_cap[event.capability] = by_cap[event.capability] or {}
        by_cap[event.capability][event.attribute] = { value = event.value }
        return
      end
      cloud[key] = event.value
      return emit_component_event(self, component, event)
    end
    local d = Driver("budget", {})
    poll.once(d, device)
    h.assert_true(dropped)
    h.assert_nil(cloud[APPS_SUMMARY])
    -- A refresh: the value is unchanged, nothing is sent.
    poll.once(d, device)
    h.assert_nil(cloud[APPS_SUMMARY])
    -- A restart: the hub's cache equals the value, so it is taken as sent.
    h.restart(device, PERSISTED)
    fields.set_state(device, state.new(state.ON))
    poll.once(d, device)
    h.assert_nil(cloud[APPS_SUMMARY], "the restart's first poll trusts the hub's cache")
    -- The rotation goes round within the cycle.
    local polls = 0
    while cloud[APPS_SUMMARY] == nil and polls < 100 do
      pc.now = pc.now + poll.DEFAULT_INTERVAL
      poll.once(d, device)
      polls = polls + 1
    end
    h.assert_true(cloud[APPS_SUMMARY] ~= nil, "the summary reached the cloud")
    h.assert_true(polls * poll.DEFAULT_INTERVAL <= emit.ROTATE_SECONDS,
      "within " .. emit.ROTATE_SECONDS .. " s: " .. polls * poll.DEFAULT_INTERVAL .. " s")
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
    -- A poll after that is an ordinary one: one rotation step, no more.
    pc.now = pc.now + 60
    mark = #device.emitted
    poll.once(d, device)
    h.assert_equal(#since(device, mark), emit.rotate_size(#sent_keys(device), poll.DEFAULT_INTERVAL))
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
-- #129 (W2): restarts, repaints and the power/schedule commands
--------------------------------------------------------------------------------

local function forced_all(events)
  for _, e in ipairs(events) do
    if (e.options or {}).state_change ~= true then
      return false, emit.row_key(e)
    end
  end
  return true
end

function T.test_a_restart_without_a_profile_change_sends_only_the_rows_that_matter()
  -- Before #129 the first poll of every run sent all ~40 rows forced, even
  -- though nothing about the profile had changed. The hub's persisted state
  -- cache says what the last run sent; only the rows whose loss the user
  -- notices go out again, forced. Measured here: 42 -> 6.
  with_pc(function(pc)
    local device = h.with_state_cache(new_device())
    local d = Driver("budget", {})
    poll.once(d, device)
    h.assert_true(#device.emitted > 30, "the earlier run painted every row")
    h.restart(device, PERSISTED)
    fields.set_state(device, state.new(state.ON))
    local mark = #device.emitted
    poll.once(d, device)
    local out = since(device, mark)
    h.assert_deep_equal(keys_of(out), resync_keys())
    h.assert_true(forced_all(out), "and forced: the cache may hold what the cloud lost")
    -- A seeded row that changes later is the run's first emit of it: forced.
    pc.status.presets = { { slot = 3, name = "새 프리셋" } }
    mark = #device.emitted
    poll.once(d, device)
    out = since(device, mark)
    h.assert_equal(h.event_value(out, caps.PRESET, "names"), "3 새 프리셋")
    h.assert_true(h.event_forced(out, caps.PRESET, "names"))
  end)
end

function T.test_after_a_profile_change_the_cache_is_no_evidence()
  with_pc(function()
    local device = h.with_state_cache(new_device())
    local d = Driver("budget", {})
    poll.once(d, device)
    h.restart(device, PERSISTED)
    fields.set_state(device, state.new(state.ON))
    -- A new generation of rows (`ensure_rows`) repaints, and the poll after
    -- it adds every row the repaint did not carry - all of it forced, in
    -- batches.
    device:set_field(fields.ROWS_PAINTED, "1")
    local mark = #device.emitted
    poll.once(d, device)
    h.fire_all(d, "repaint-batch")
    local out = since(device, mark)
    h.assert_true(#out > 30, "a new profile's record is empty: " .. #out)
    h.assert_true(forced_all(out))
    h.assert_false(emit.seeding(device))
  end)
end

function T.test_a_profile_change_forces_every_row_once()
  -- Before #129: `repaint` + a `force = true` poll at once (and `ensure_rows`'
  -- own repaint on top in `init`), then the same again at 15 s and 90 s -
  -- 83, 69 and 69 events on a migration. Now every row once - in batches of
  -- PAINT_BATCH, the rows the user sees first in the first one - then the
  -- rows that matter.
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    poll.repaint_soon(d, device)
    local first = since(device, mark)
    h.assert_equal(#first, emit.PAINT_BATCH, "the first batch")
    local in_first = {}
    for _, e in ipairs(first) do
      in_first[emit.row_key(e)] = true
    end
    for _, key in ipairs(emit.PAINT_FIRST) do
      h.assert_true(in_first[key], key .. " is in the first batch")
    end
    local batches = 1
    while true do
      local before = #device.emitted
      if not h.fire_last(d, "repaint-batch") then
        break
      end
      local batch = #device.emitted - before
      h.assert_true(batch >= 1 and batch <= emit.PAINT_BATCH, "batch " .. batches + 1 .. ": " .. batch)
      batches = batches + 1
    end
    h.assert_nil(emit.painting(device), "the queue is done")
    local out = since(device, mark)
    local seen = {}
    for _, e in ipairs(out) do
      local key = emit.row_key(e)
      h.assert_nil(seen[key], key .. " went out twice")
      seen[key] = true
    end
    h.assert_true(forced_all(out))
    h.assert_true(#out > 30 and #out < 50, "every row once: " .. #out)
    h.assert_equal(batches, math.ceil(#out / emit.PAINT_BATCH))
    for _, delay in ipairs(poll.LATE_REPAINT_SECONDS) do
      mark = #device.emitted
      h.assert_true(h.fire_last(d, "repaint-late-" .. delay))
      local late = since(device, mark)
      h.assert_deep_equal(keys_of(late), resync_keys(), delay .. " s: only the rows that matter")
      h.assert_true(forced_all(late))
    end
  end)
end

--- The Dev channel device on pc-monitor.v9 after the driver update (#123:
--- v10, the watch card on its new capability id), through the real `init` and then
--- `minutes` of driver life on the clock: the default 30 s polls, the
--- batches, the follow-ups.
--- `landing`: the profile change lands that many seconds in (`infoChanged`).
local function migrate(pc, minutes, landing)
  local device = new_device()
  device.profile = { id = "v9", name = "pc-monitor.v9", components = h.components_for("pc-monitor.v9") }
  device:set_field(fields.ROWS_PAINTED, "9")
  local trace = stamped(device, pc)
  local d = clocked(Driver("budget", {}), pc)
  local start = pc.now
  init_driver.lifecycle_handlers.init(d, device)
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-monitor.v10")
  if landing then
    run_until(d, pc, start + landing)
    init_driver.lifecycle_handlers.infoChanged(d, device, "infoChanged",
      { old_st_store = { profile = { id = "v9" } } })
  end
  run_until(d, pc, start + minutes * 60)
  return device, trace, start
end

--- When each row was first sent, forced, at or after `from`, relative to `start`.
local function painted_at(trace, start, from)
  local at = {}
  for _, entry in ipairs(trace) do
    if entry.forced and entry.at >= from and at[entry.key] == nil then
      at[entry.key] = entry.at - start
    end
  end
  return at
end

function T.test_a_migration_paints_every_row_within_seconds_under_the_guard()
  -- The 2026-10-01 migration (v5 -> v6) sent ~49 rows in the same second, and
  -- `pcApps.summary` never reached the cloud. Before #129 it was 83 at once,
  -- then 69 at 15 s and at 90 s. Measured here, before this change: 47 in the
  -- first second, 48 in the busiest 10 s. Now the same rows in batches: the
  -- rows the user sees first at once, every row within 25 s, and no 10 s
  -- with more than the guard's 20.
  with_pc(function(pc)
    local device, trace, start = migrate(pc, 2)
    local at = painted_at(trace, start, start)
    local painted, last = 0, 0
    for _, seconds in pairs(at) do
      painted = painted + 1
      last = math.max(last, seconds)
    end
    h.assert_true(painted > 30, "every row: " .. painted)
    for _, key in ipairs(sent_keys(device)) do
      h.assert_true(at[key] ~= nil, key .. " was painted, forced")
    end
    for _, key in ipairs(emit.PAINT_FIRST) do
      h.assert_equal(at[key], 0, key .. " goes in the first batch, at once")
    end
    h.assert_true(last <= 25, "the last row " .. last .. " s in (all at 0 s before)")
    local most = busiest(trace, emit.BUDGET_SECONDS)
    h.assert_true(most <= emit.BUDGET_EVENTS, "busiest 10 s: " .. most .. " (48 before)")
    -- The follow-up 30 s in: the rows that matter.
    local late = 0
    for _, entry in ipairs(trace) do
      if entry.at == start + 30 then
        late = late + 1
      end
    end
    h.assert_true(late <= #emit.RESYNC_ROWS + 1 + emit.ROTATE_MAX, "30 s: " .. late)
  end)
end

function T.test_a_migration_and_its_landing_stay_under_the_guard()
  -- The landing's `infoChanged` repaints again - the new profile is the one
  -- that has to hold every row - while the migration's batches are still
  -- going. Its rows join the queue at the same cadence instead of a second
  -- burst (before: 47 at once, then 42 more 10 s later).
  with_pc(function(pc)
    local landing = 3
    local device, trace, start = migrate(pc, 2, landing)
    local at = painted_at(trace, start, start + landing)
    for _, key in ipairs(sent_keys(device)) do
      h.assert_true(at[key] ~= nil, key .. " was painted again after the landing")
      h.assert_true(at[key] <= 30, key .. " " .. at[key] .. " s in")
    end
    local most = busiest(trace, emit.BUDGET_SECONDS)
    h.assert_true(most <= emit.BUDGET_EVENTS, "busiest 10 s: " .. most .. " (49 before)")
  end)
end

function T.test_a_repaint_during_a_paint_requeues_every_row_and_keeps_the_cadence()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    poll.repaint_soon(d, device)
    h.assert_true(emit.painting(device) ~= nil)
    pc.now = pc.now + 3
    local mark = #device.emitted
    poll.repaint_soon(d, device)
    h.assert_equal(#since(device, mark), 0, "no second first batch")
    local waiting = 0
    for _ in pairs(emit.painting(device).rows) do
      waiting = waiting + 1
    end
    h.assert_true(waiting > 30, "every row is waiting again: " .. waiting)
    -- One live batch timer, PAINT_SECONDS apart.
    local live = 0
    for _, t in ipairs(d.timers) do
      if t.name == "repaint-batch" and not t.cancelled then
        live = live + 1
        h.assert_equal(t.delay, emit.PAINT_SECONDS)
      end
    end
    h.assert_equal(live, 1)
    h.fire_all(d, "repaint-batch")
    local out = since(device, mark)
    h.assert_equal(#out, waiting, "each waiting row once")
    h.assert_true(forced_all(out))
    -- Only the latest repaint's follow-ups are left.
    live = 0
    for _, t in ipairs(d.timers) do
      if t.name == "repaint-late-30" and not t.cancelled then
        live = live + 1
      end
    end
    h.assert_equal(live, 1)
  end)
end

function T.test_a_command_during_a_paint_is_answered_at_once()
  -- The answer the app's spinner waits for does not wait for its batch.
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    poll.repaint_soon(d, device)
    h.assert_true(emit.painting(device).rows[caps.SCHEDULE .. ".status"] ~= nil, "still waiting")
    local mark = #device.emitted
    rows.answer_minutes_pick(device)
    emit.rows(device, { { cap = caps.SCHEDULE, attr = "status", value = "idle", force = true } })
    local out = since(device, mark)
    h.assert_equal(count_forced(out, caps.SCHEDULE .. ".minutesPick"), 1)
    h.assert_equal(count_forced(out, caps.SCHEDULE .. ".status"), 1)
    h.assert_nil(emit.painting(device).rows[caps.SCHEDULE .. ".status"], "and leaves the queue")
    -- An ordinary update of a waiting row only changes what its batch carries.
    mark = #device.emitted
    emit.rows(device, { { cap = caps.STATUS, attr = "message", value = "새 값" } })
    h.assert_equal(#since(device, mark), 0)
    h.fire_all(d, "repaint-batch")
    out = since(device, mark)
    h.assert_equal(h.event_value(out, caps.STATUS, "message"), "새 값")
    h.assert_true(h.event_forced(out, caps.STATUS, "message"))
    h.assert_equal(count_forced(out, caps.SCHEDULE .. ".status"), 0, "not a second time")
  end)
end

function T.test_a_paint_nobody_starts_starts_itself()
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    poll.repaint(device, d)
    h.assert_equal(#since(device, mark), 0, "queued")
    local timer
    for _, t in ipairs(d.timers) do
      if t.name == "repaint-batch" and not t.cancelled then
        timer = t
      end
    end
    h.assert_equal(timer.delay, emit.PAINT_START_SECONDS)
    h.fire_last(d, "repaint-batch")
    h.assert_equal(#since(device, mark), emit.PAINT_BATCH)
    -- Without a driver there are no timers: everything at once, as before.
    mark = #device.emitted
    poll.repaint(device)
    h.assert_true(#since(device, mark) > 10)
    h.assert_nil(emit.painting(device))
  end)
end

function T.test_a_preference_change_that_moves_no_profile_does_not_repaint()
  -- Before #129 every infoChanged was a whole forced repaint (69 events) -
  -- a new poll interval, a new secret. The record of the device before the
  -- change says whether its profile moved.
  with_pc(function()
    local device = new_device()
    device.profile = { id = "profile-1", name = "pc.v10", components = h.components_for("pc.v10") }
    local d = Driver("budget", {})
    poll.once(d, device)
    local mark = #device.emitted
    init_driver.lifecycle_handlers.infoChanged(d, device, "infoChanged",
      { old_st_store = { profile = { id = "profile-1" } } })
    h.assert_equal(#since(device, mark), 0)
    -- A profile that did move is repainted.
    init_driver.lifecycle_handlers.infoChanged(d, device, "infoChanged",
      { old_st_store = { profile = { id = "profile-0" } } })
    h.fire_all(d, "repaint-batch")
    h.assert_true(#since(device, mark) > 30)
  end)
end

function T.test_power_and_schedule_commands_share_the_answer_window()
  -- #129 (W2): `schedule`, `cancel` and `switch off` used to poll on their
  -- own; a routine that fires them together now gets one answer poll per
  -- window, and every row each of them is bound to is still forced.
  with_pc(function(pc)
    local original = { command = client.command, cancel = client.cancel }
    local sent = {}
    client.command = function(_, command, _mode, minutes)
      sent[#sent + 1] = command .. ":" .. tostring(minutes)
      return true, {}, nil
    end
    client.cancel = function()
      sent[#sent + 1] = "cancel"
      return true, { cancelled = true }, nil
    end
    local ok, err = pcall(function()
      local device = new_device()
      local d = Driver("budget", {})
      poll.once(d, device)
      local polls = pc.polls
      local mark = #device.emitted
      handlers_for(caps.SCHEDULE).schedule(d, device, { args = { minutes = "30", command = "restart" } })
      handlers_for(caps.SCHEDULE).cancel(d, device, { args = {} })
      handlers_for("switch").off(d, device, { command = "off", args = {} })
      h.assert_deep_equal(sent, { "restart:30", "cancel", "shutdown:0" })
      h.assert_equal(pc.polls, polls + 1, "only the first command polled at once")
      h.assert_true(h.fire_last(d, "answer-poll"))
      h.assert_equal(pc.polls, polls + 2, "the other two share one poll")
      local out = since(device, mark)
      h.assert_equal(count_forced(out, caps.SCHEDULE .. ".summary"), 2, "both polls answer the schedule rows")
      h.assert_equal(count_forced(out, "switch.switch"), 1, "the toggle is answered")
      h.assert_equal(h.last_value(out, nil, caps.STATUS, "message"), "예약을 취소했습니다",
        "the window's note is the last command's")
    end)
    client.command, client.cancel = original.command, original.cancel
    if not ok then
      error(err, 0)
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
    local records = {}
    for i = 1, emit.BUDGET_EVENTS + 5 do
      records[#records + 1] = { cap = caps.STATUS, attr = "message", value = "m" .. i }
    end
    emit.rows(device, records)
    h.assert_equal(#device.emitted, emit.BUDGET_EVENTS + 5, "the guard only logs")
    h.assert_equal(budget_warnings(), 1)
    emit.rows(device, { { cap = caps.STATUS, attr = "message", value = "again" } })
    h.assert_equal(budget_warnings(), 1, "once per window")
    pc.now = pc.now + emit.BUDGET_SECONDS
    emit.rows(device, { { cap = caps.STATUS, attr = "message", value = "later" } })
    h.assert_equal(budget_warnings(), 1, "a new window starts quiet")
  end)
end

--------------------------------------------------------------------------------
-- a PC that is off, or on with its app not answering
--------------------------------------------------------------------------------

local TRACK = features.CAP_TRACK_DATA .. ".audioTrackData"
local SESSION_SUMMARY = caps.SESSION .. ".summary"
local POWER = caps.POWER_STATE .. ".powerState"
local SWITCH = state.CAP_SWITCH .. ".switch"
-- The rows a routine reads or a list rests on: an offline PC never moves them
-- (switch and powerState aside, which say the power).
local ROUTINE_ROWS = {
  features.WATCH_COMPONENT .. "/" .. caps.WATCH .. ".slotOne",
  features.WATCH_COMPONENT .. "/" .. caps.WATCH .. ".slotTwo",
  features.WATCH_COMPONENT .. "/" .. caps.WATCH .. ".names",
  features.CAP_PLAYBACK .. ".playbackStatus",
  MUTE, VOLUME,
  features.AWAKE_COMPONENT .. "/" .. features.CAP_SWITCH .. ".switch",
  caps.SESSION .. ".locked", caps.SESSION .. ".user", caps.SESSION .. ".idleMinutes",
}

local function values_by_key(events)
  local out = {}
  for _, e in ipairs(events) do
    out[emit.row_key(e)] = e.value
  end
  return out
end

local function fail_with(kind)
  client.get_status = function() return false, nil, kind end
end

local function assert_routine_rows_untouched(events, context)
  local sent = values_by_key(events)
  for _, key in ipairs(ROUTINE_ROWS) do
    h.assert_nil(sent[key], context .. ": " .. key .. " is not the offline texts' row")
  end
end

function T.test_an_off_pc_says_so_on_the_display_rows_once_and_paints_them_back()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    h.assert_equal(emit.sent_value(device, APPS_SUMMARY), "Steam")
    local live_track = emit.sent_value(device, TRACK)
    h.assert_equal(live_track.title, "Blinding Lights")
    h.assert_equal(emit.sent_value(device, SESSION_SUMMARY), "사용 중 · kim")

    fail_with("unreachable")
    -- One poll without an answer is not a PC that is off: nothing changes yet.
    local mark = #device.emitted
    poll.once(d, device)
    local first = values_by_key(since(device, mark))
    h.assert_nil(first[POWER], "still on after one miss")
    h.assert_equal(first[caps.STATUS .. ".connection"], "unreachable")
    h.assert_equal(first[caps.STATUS .. ".message"], "PC에 연결할 수 없습니다")
    h.assert_nil(first[APPS_SUMMARY], "one miss leaves the watch card alone")
    h.assert_nil(first[TRACK])
    h.assert_nil(first[SESSION_SUMMARY])

    -- The second miss: off, and the rows that only describe a live PC say so.
    mark = #device.emitted
    poll.once(d, device)
    local off = since(device, mark)
    local sent = values_by_key(off)
    h.assert_equal(sent[POWER], state.OFF)
    h.assert_equal(sent[SWITCH], "off")
    h.assert_equal(sent[APPS_SUMMARY], "PC 꺼짐")
    h.assert_deep_equal(sent[TRACK], { title = "PC 꺼짐" })
    h.assert_equal(sent[SESSION_SUMMARY], "PC 꺼짐")
    h.assert_equal(sent[caps.STATUS .. ".message"], "PC가 꺼져 있거나 네트워크에 연결되지 않았습니다")
    assert_routine_rows_untouched(off, "off")

    -- Once: the polls after it send none of them again (event budget).
    for _ = 1, 3 do
      mark = #device.emitted
      poll.once(d, device)
      local again = values_by_key(since(device, mark))
      for _, key in ipairs({ APPS_SUMMARY, TRACK, SESSION_SUMMARY, POWER, SWITCH }) do
        h.assert_nil(again[key], key .. " is not re-sent while the PC stays off")
      end
    end

    -- The PC answers: the status paints every one of them back.
    client.get_status = function() return true, copy(pc.status), nil end
    mark = #device.emitted
    poll.once(d, device)
    local back = values_by_key(since(device, mark))
    h.assert_equal(back[POWER], state.ON)
    h.assert_equal(back[APPS_SUMMARY], "Steam")
    h.assert_deep_equal(back[TRACK], live_track)
    h.assert_equal(back[SESSION_SUMMARY], "사용 중 · kim")
  end)
end

function T.test_a_refused_poll_keeps_the_pc_on_and_says_the_app_is_down()
  with_pc(function(pc)
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)

    fail_with("app_down")
    local mark = #device.emitted
    poll.once(d, device)
    local down = since(device, mark)
    local sent = values_by_key(down)
    h.assert_nil(sent[POWER], "the PC answered the connection: it stays on")
    h.assert_nil(sent[SWITCH])
    h.assert_equal(sent[caps.STATUS .. ".connection"], "unreachable")
    h.assert_equal(sent[caps.STATUS .. ".summary"], "PC 앱 응답 없음")
    h.assert_equal(sent[caps.STATUS .. ".message"],
      "PC는 켜져 있지만 PC 앱이 응답하지 않습니다 · PC에서 앱을 다시 실행하세요")
    h.assert_equal(sent[APPS_SUMMARY], "PC 앱 응답 없음")
    h.assert_deep_equal(sent[TRACK], { title = "PC 앱 응답 없음" })
    h.assert_equal(sent[SESSION_SUMMARY], "PC 앱 응답 없음")
    assert_routine_rows_untouched(down, "app down")

    -- However long the app stays down, the PC is never counted off, and
    -- nothing is sent again.
    for _ = 1, 4 do
      mark = #device.emitted
      poll.once(d, device)
      h.assert_deep_equal(keys_of(since(device, mark)), {}, "an app that stays down costs nothing")
    end
    h.assert_equal(fields.state(device).power_state, state.ON)
    h.assert_equal(fields.state(device).unreachable_count, 0)

    -- The PC goes away for real: two misses, off, and the rows follow.
    fail_with("unreachable")
    poll.once(d, device)
    mark = #device.emitted
    poll.once(d, device)
    sent = values_by_key(since(device, mark))
    h.assert_equal(sent[POWER], state.OFF)
    h.assert_equal(sent[APPS_SUMMARY], "PC 꺼짐")

    -- It boots, and the app is not up yet: the PC is on again at once.
    fail_with("app_down")
    mark = #device.emitted
    poll.once(d, device)
    sent = values_by_key(since(device, mark))
    h.assert_equal(sent[POWER], state.ON)
    h.assert_equal(sent[SWITCH], "on")
    h.assert_equal(sent[APPS_SUMMARY], "PC 앱 응답 없음")

    client.get_status = function() return true, copy(pc.status), nil end
    mark = #device.emitted
    poll.once(d, device)
    sent = values_by_key(since(device, mark))
    h.assert_equal(sent[APPS_SUMMARY], "Steam")
    h.assert_equal(sent[caps.STATUS .. ".connection"], "ok")
  end)
end

function T.test_a_repaint_of_an_off_pc_keeps_the_off_texts()
  with_pc(function()
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    fail_with("unreachable")
    poll.once(d, device)
    poll.once(d, device)
    -- A profile change while the PC is off: every row once, from the last
    -- status - but not its "Steam".
    local mark = #device.emitted
    poll.repaint(device, nil)
    local sent = values_by_key(since(device, mark))
    h.assert_equal(sent[APPS_SUMMARY], "PC 꺼짐")
    h.assert_deep_equal(sent[TRACK], { title = "PC 꺼짐" })
    h.assert_equal(sent[SESSION_SUMMARY], "PC 꺼짐")
    h.assert_equal(sent[features.WATCH_COMPONENT .. "/" .. caps.WATCH .. ".slotOne"], "running",
      "the slot a routine reads keeps its value")
  end)
end

function T.test_the_track_row_is_left_alone_when_the_pc_never_painted_it()
  with_pc(function(pc)
    -- A service that sends no `media` block never painted the track row, and
    -- would not paint it back either.
    pc.status.media = nil
    local device = new_device()
    local d = Driver("budget", {})
    poll.once(d, device)
    h.assert_nil(emit.sent_value(device, TRACK))
    fail_with("unreachable")
    poll.once(d, device)
    poll.once(d, device)
    h.assert_nil(emit.sent_value(device, TRACK), "no track title appears for an off PC")
    h.assert_equal(emit.sent_value(device, APPS_SUMMARY), "PC 꺼짐")
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
