-- Polling and health (design doc §6.1): the poll cycle and its timer, the
-- answer to a command, the repaint after a profile change, and the error
-- mapping for a failed request.
--
-- Sending is device/emit.lua's, the rows the driver writes itself are
-- device/rows.lua's, the stored values device/fields.lua's. This module is the
-- top of that layer: it is the one that calls into push, wol and discovery,
-- and it hands the two of them that need a poll back (`once`,
-- `follow_battery`) through their `use`, so none of them requires this one.

local caps = require "caps"
local client = require "client"
local clock = require "device.clock"
local discovery = require "discovery"
local emit = require "device.emit"
local features = require "features"
local fields = require "device.fields"
local i18n = require "i18n"
local profiles = require "profiles"
local push = require "push"
local rows = require "device.rows"
local state = require "state"
local wol = require "wol"

local poll = {}

-- The row generation (`fields.ROWS_PAINTED`) is the profile generation: a new
-- capability id always comes with one, and a device on a new profile starts
-- with an empty cloud record, so every row is painted once per generation
-- (`ensure_rows`; platform notes "허브의 정의 캐시").
poll.ROWS_VERSION = tostring(profiles.VERSION)
poll.DEFAULT_INTERVAL = 30
-- First service release that speaks protocol 1 (§3).
poll.MIN_SERVICE_VERSION = "1.1.0"

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

--------------------------------------------------------------------------------
-- repainting after a profile change
--------------------------------------------------------------------------------

--- Paint every row once per row generation (`ROWS_VERSION`): on `added`, and
--- on `init` for a device migrated onto new capability ids, whose attributes
--- all start unset. `driver` (optional) spreads the repaint over batches.
function poll.ensure_rows(device, driver)
  if fields.get(device, fields.ROWS_PAINTED) == poll.ROWS_VERSION then
    return false
  end
  fields.set(device, fields.ROWS_PAINTED, poll.ROWS_VERSION)
  poll.repaint(device, driver)
  return true
end

--- #123: the watch card's slot values a repaint keeps - what the last status
--- settled on, else what this run sent or the hub's state cache holds. A slot
--- with none of those is painted `empty` (features.watch_events).
function poll.kept_watch(device)
  local watch = (fields.extras(device) or {}).watch
  if type(watch) == "table" then
    return watch
  end
  local out = {}
  for slot = 1, features.WATCH_SLOTS do
    out[slot] = features.watch_value(emit.last_value(device, {
      cap = caps.WATCH, attr = features.slot_attr(slot), component = features.WATCH_COMPONENT,
    }))
  end
  return out
end

--- Repaint every row, forced: the cloud starts a new profile with empty
--- states. Rows only a later poll carries are forced by that poll (`forget`
--- starts a new "first emit is forced" generation). `driver` (optional): in
--- batches (`emit.paint`).
function poll.repaint(device, driver)
  -- Read before `forget` drops what this run has sent.
  local watch = poll.kept_watch(device)
  emit.forget(device)
  -- The resting value, not the remembered one: mid-transition the row has to
  -- keep saying "in progress". A repeat `ensure_action` owed is this event.
  rows.owe_action_repeat(device, nil)
  local records = emit.gather(device, function()
    rows.emit_action(device, rows.resting_action(device), true)
    rows.emit_plan_command(device, rows.plan_command(device), true)
    rows.answer_preset(device)
    rows.answer_toast(device)
    -- The v1.2.0 rows from the last status this run read, when there was one.
    emit.rows(device, state.initial_rows(fields.lang(device), fields.service_version(device),
      (fields.extras(device) or {}).last_status, watch))
  end)
  return emit.paint(driver, device, records)
end

--- Repaint now and look again a little later (a migration, an icon or a
--- battery switch). Every row goes once, in batches, then an ordinary poll
--- adds what the status says. The follow-ups at `LATE_REPAINT_SECONDS` (a
--- poll plus `resync`) cover a first batch that raced the cloud applying the
--- new profile. A second repaint_soon replaces the first one's follow-ups, and
--- one during a running paint rejoins its queue. `opts.painted`: the caller
--- has just repainted (`ensure_rows` in `init`).
poll.LATE_REPAINT_SECONDS = { 30, 90 }
function poll.repaint_soon(driver, device, opts)
  opts = opts or {}
  if not opts.painted then
    poll.repaint(device, driver)
  end
  pcall(poll.once, driver, device)
  -- (`once` starts the paint itself; this is for a poll that raised.)
  emit.paint_start(driver, device)
  local previous = fields.get(device, fields.REPAINT_LATE_TIMERS)
  for _, timer in ipairs(type(previous) == "table" and previous or {}) do
    pcall(function() driver:cancel_timer(timer) end)
  end
  local timers = {}
  for _, delay in ipairs(poll.LATE_REPAINT_SECONDS) do
    local ok, timer = pcall(function()
      return driver:call_with_delay(delay, function()
        pcall(poll.once, driver, device)
        emit.resync(device)
      end, "repaint-late-" .. delay)
    end)
    if ok and timer then
      timers[#timers + 1] = timer
    end
  end
  fields.set(device, fields.REPAINT_LATE_TIMERS, timers)
end

--- Follow `status.battery.present` onto the plain or the `-battery` profile
--- (`profiles.apply_battery`). A switch is repainted one second later, so the
--- poll or push that got here finishes first. Returns the new profile name.
function poll.follow_battery(driver, device, status)
  local moved = profiles.apply_battery(device, features.battery_present(status))
  if moved and driver then
    pcall(function()
      driver:call_with_delay(1, function()
        poll.repaint_soon(driver, device)
      end, "battery-profile")
    end)
  end
  return moved
end

--------------------------------------------------------------------------------
-- answering commands (platform notes "이벤트 예산")
--------------------------------------------------------------------------------

-- Commands close together share their answer polls. The first polls at once
-- and opens a window of `ANSWER_WINDOW_SECONDS`; the commands inside it add
-- their rows and are answered by one poll when it closes, which opens the
-- next window. Every command's rows still get their forced emit within one
-- window (the spinner rule).
poll.ANSWER_WINDOW_SECONDS = 1.5

-- After a burst (`BURST_COMMANDS` inside `BURST_SECONDS`) the budget has most
-- likely run out, so `BURST_RESYNC_SECONDS` later the rows the user notices go
-- out once more (`emit.resync`), ahead of their turn in the rotation.
poll.BURST_COMMANDS = 3
poll.BURST_SECONDS = 10
poll.BURST_RESYNC_SECONDS = 60

--- Count a command towards the burst that brings the resync forward.
function poll.note_command(driver, device)
  local now = clock.epoch()
  local times = fields.get(device, fields.RECENT_COMMANDS)
  local kept = {}
  for _, t in ipairs(type(times) == "table" and times or {}) do
    if now >= t and now - t < poll.BURST_SECONDS then
      kept[#kept + 1] = t
    end
  end
  kept[#kept + 1] = now
  fields.set(device, fields.RECENT_COMMANDS, kept)
  if #kept < poll.BURST_COMMANDS or not driver then
    return false
  end
  if fields.get(device, fields.BURST_TIMER) then
    return false
  end
  local ok, timer = pcall(function()
    return driver:call_with_delay(poll.BURST_RESYNC_SECONDS, function()
      fields.set(device, fields.BURST_TIMER, nil)
      emit.resync(device)
    end, "resync-burst")
  end)
  if ok then
    fields.set(device, fields.BURST_TIMER, timer or true)
  end
  return ok
end

local function merge_rows(into, keys)
  if type(keys) == "table" then
    for key, wanted in pairs(keys) do
      if wanted then
        into[key] = true
      end
    end
  end
  return into
end

-- Open an answer window; when it closes, the commands that arrived inside it
-- are answered by one poll, which opens the next window. False when no timer
-- could be set (every command polls then).
local function open_window(driver, device)
  if not driver then
    return false
  end
  local window = { rows = {}, owed = false }
  local ok = pcall(function()
    driver:call_with_delay(poll.ANSWER_WINDOW_SECONDS, function()
      if fields.get(device, fields.ANSWER_WINDOW) ~= window then
        return
      end
      fields.set(device, fields.ANSWER_WINDOW, nil)
      if window.owed then
        open_window(driver, device)
        pcall(poll.once, driver, device, { force = window.rows, note = window.note })
      end
    end, "answer-poll")
  end)
  if ok then
    fields.set(device, fields.ANSWER_WINDOW, window)
  end
  return ok
end

--- The poll that answers a command that went out. `keys`: the row keys the
--- app is watching, sent forced. `note`: the one-off confirmation for
--- `pcInfo.message` ("예약을 취소했습니다"); in a shared window the last one
--- wins. Returns what the poll returned, or true when the command joined a
--- window whose poll is still to come.
function poll.answer(driver, device, keys, note)
  poll.note_command(driver, device)
  local window = fields.get(device, fields.ANSWER_WINDOW)
  if type(window) == "table" then
    merge_rows(window.rows, keys)
    if note ~= nil then
      window.note = note
    end
    window.owed = true
    return true
  end
  open_window(driver, device)
  return poll.once(driver, device, { force = keys, note = note })
end

--------------------------------------------------------------------------------
-- the rotation (platform notes "이벤트 예산", 2026-10-01 migration)
--------------------------------------------------------------------------------

--- A rotation step when one is due (`emit.rotate`): at most once per poll
--- interval (less a fifth, for timer jitter), so a command burst's answer
--- polls add none. The first step of a run only starts the clock, and there
--- is none while a paint is sending every row anyway. Called at the end of
--- every poll, successful or not. Returns how many rows went out.
function poll.rotate_due(_driver, device, deps)
  if emit.painting(device) then
    return 0
  end
  local now = clock.epoch(deps)
  local at = tonumber(fields.get(device, fields.ROTATE_AT))
  if not at or now < at then
    fields.set(device, fields.ROTATE_AT, now)
    return 0
  end
  local interval = poll.interval((device or {}).preferences)
  if now - at < interval * 4 / 5 then
    return 0
  end
  fields.set(device, fields.ROTATE_AT, now)
  return emit.rotate(device, interval)
end

--------------------------------------------------------------------------------
-- a failed request
--------------------------------------------------------------------------------

--- err_kind (client.lua) -> `pcInfo.connection` value (§4), or nil when the
--- failure says nothing about the connection and the last state stands.
function poll.connection_for(kind)
  if kind == "unauthorized" or kind == "unreachable" or kind == "incompatible" then
    return kind
  end
  -- 403: the secret was accepted, the hub is not on the allow-list. The enum
  -- has no value of its own for it (§3.1); the message tells the two apart.
  if kind == "forbidden" then
    return "unauthorized"
  end
  -- The service answered but refused: a protocol problem, not a connection one.
  if kind == "badrequest" then
    return "incompatible"
  end
  if kind == "ratelimited" then
    return nil
  end
  return "ok"
end

--- The sentence for an err_kind (§3.1). `body` is the decoded response when
--- there was one: a higher `protocol` means the driver is the old side.
function poll.message_for(kind, body, lang)
  body = body or {}
  if kind == "incompatible" then
    local protocol = tonumber(body.protocol)
    if protocol and protocol > client.PROTOCOL then
      return i18n.t(lang, "incompatible_driver")
    end
    return i18n.t(lang, "incompatible_service", poll.MIN_SERVICE_VERSION)
  end
  if kind == "badrequest" then
    -- The service says what it refused (§3.3).
    local text = i18n.t(lang, "badrequest")
    if type(body.error) == "string" and body.error ~= "" then
      return text .. " · " .. body.error
    end
    return text
  end
  if kind == "unauthorized" or kind == "forbidden" or kind == "unreachable"
      or kind == "ratelimited" then
    return i18n.t(lang, kind)
  end
  return ""
end

--------------------------------------------------------------------------------
-- the poll
--------------------------------------------------------------------------------

--- Store what a status body says about the PC's identity (§6.5), and put the
--- short id in the device's `model` once. True when something changed.
function poll.remember_identity(device, body)
  body = body or {}
  local changed = false
  if type(body.machine_id) == "string" and body.machine_id ~= ""
      and fields.get(device, fields.MACHINE_ID) ~= body.machine_id then
    fields.set(device, fields.MACHINE_ID, body.machine_id)
    changed = true
  end
  if type(body.hostname) == "string" and body.hostname ~= ""
      and fields.get(device, fields.HOSTNAME) ~= body.hostname then
    fields.set(device, fields.HOSTNAME, body.hostname)
    changed = true
  end
  discovery.ensure_model(device)
  return changed
end

-- One poll cycle: GET /st/v1/status, advance the state machine, emit, health.
-- `opts.force`: the row keys this poll answers a command on (sent forced), or
-- true for every row. `opts.note`: a one-off confirmation for `pcInfo.message`
-- (state.MESSAGE_ORDER). `opts.deps`: the injected http/json/clock of tests.
local function once(driver, device, opts)
  opts = opts or {}
  local prefs = device.preferences or {}
  local lang = prefs.language
  local current = fields.state(device)

  if not client.device_base_url(device) then
    -- Nothing to poll until there is an address.
    rows.emit_connection(device, "unreachable", i18n.t(lang, "no_ip"), opts.deps)
    -- Never offline: the app greys out an offline device, which would take
    -- Wake-on-LAN away (platform notes "상태 캐시와 infoChanged").
    pcall(function() device:online() end)
    return false, "no ip"
  end

  local ok, body, kind = client.get_status(device, opts.deps)

  if ok then
    local nxt = state.transition(current, "status_ok")
    -- The pending schedule is how the PC's own grace period shows up
    -- (state.grace_limit), and which v1.2.0 features it offers.
    state.remember_schedule(nxt, body)
    features.remember(nxt, body)
    fields.set_state(device, nxt)
    wol.cancel_wake(driver, device)
    -- §6.4: the MAC to wake this PC on, its adapter's name and whether WoL is
    -- on there - all read while the PC is off, when no status is available.
    -- A driver cannot write its own preferences, so these are fields.
    local mac = state.wol_mac(body)
    if mac then
      fields.set(device, fields.WOL_MAC, mac)
    end
    fields.set(device, fields.WOL_READY, not state.wol_off(body))
    local adapter = state.wol_adapter(body)
    if adapter then
      fields.set(device, fields.WOL_ADAPTER, adapter)
    end
    poll.remember_identity(device, body)
    -- Before `ensure_rows`, which repaints the version row.
    fields.remember_service_version(device, body)
    fields.remember_last_seen(device, clock.epoch(opts.deps))
    -- No status body carries `lastAction` or `planCommand`, and a migrated
    -- device has emitted nothing under the new ids: the resting values first,
    -- then the body overwrites the rows it knows.
    poll.ensure_rows(device, driver)
    local records = state.apply_status(nxt, body, {
      now = clock.now(),
      lang = lang,
      note = opts.note,
    })
    if opts.force == true then
      emit.rows(device, records, { reason = "paint" })
    else
      emit.rows(device, records, { answer = opts.force })
    end
    rows.ensure_action(device)
    rows.ensure_plan_command(device)
    rows.ensure_preset(device, opts.deps)
    rows.ensure_toast(device)
    poll.rotate_due(driver, device, opts.deps)
    poll.follow_battery(driver, device, body)
    pcall(function() device:online() end)
    -- §6.3: with the PC answering, ask it to push. A failure only means the
    -- driver keeps polling.
    pcall(function() push.ensure(driver, device, opts.deps) end)
    return true
  end

  local connection = poll.connection_for(kind)
  if not connection then
    -- §3.1: rate limited. Nothing about the PC changed; leave every row.
    logger().warn(string.format("poll skipped: %s", i18n.t("en", kind)))
    return false, kind
  end

  local nxt = current
  if kind == "unreachable" then
    nxt = state.transition(current, "unreachable")
    pcall(function() device:online() end)
    -- §6.5: the PC may have moved; one targeted SSDP search (rate limited).
    pcall(function() discovery.refresh(driver, device, opts.deps) end)
  end
  fields.set_state(device, nxt)
  rows.emit_power(device, nxt)
  -- A PC that is waking or gone answers nothing: this is the path that keeps
  -- the command list's resting value during a transition, and hands it back
  -- to `none` when `waking` gives up.
  rows.ensure_action(device)
  rows.ensure_preset(device, opts.deps)
  rows.ensure_toast(device)
  rows.emit_connection(device, connection, poll.message_for(kind, body, lang), opts.deps)
  poll.rotate_due(driver, device, opts.deps)
  return false, kind
end

--- One poll cycle (see `once` above). Also `refresh`, and a command's answer.
function poll.once(driver, device, opts)
  local results = table.pack(once(driver, device, opts))
  -- A repaint this poll queued, or the one it followed: the queue now holds
  -- the status's rows too, so its first batch goes.
  emit.paint_start(driver, device)
  return table.unpack(results, 1, results.n)
end

--------------------------------------------------------------------------------
-- the timer
--------------------------------------------------------------------------------

--- Poll interval in seconds from the `pollInterval` preference (§7).
function poll.interval(prefs)
  local seconds = tonumber((prefs or {}).pollInterval)
  if seconds and seconds >= 5 then
    return math.floor(seconds)
  end
  return poll.DEFAULT_INTERVAL
end

--- §6.7: spread the devices' polls over the interval so N PCs are not all
--- asked in the same second. A hash of the DNI keeps a device's slot across
--- restarts without coordination. FNV-1a, 32 bit.
function poll.offset(dni, interval)
  interval = math.floor(tonumber(interval) or poll.DEFAULT_INTERVAL)
  if interval <= 1 or type(dni) ~= "string" or dni == "" then
    return 0
  end
  -- Hex literals so the constants are integers on every Lua 5.3 build
  -- (offset basis 2166136261, prime 16777619).
  local hash = 0x811C9DC5
  for i = 1, #dni do
    hash = (hash ~ dni:byte(i)) & 0xFFFFFFFF
    hash = (hash * 0x01000193) & 0xFFFFFFFF
  end
  -- 64-bit integers on the hub, 32-bit under fengari: without bit 31 both
  -- read the same value.
  return (hash & 0x7FFFFFFF) % interval
end

function poll.stop(driver, device)
  for _, field in ipairs({ fields.POLL_TIMER, fields.POLL_START_TIMER }) do
    local timer = fields.get(device, field)
    if timer then
      pcall(function() driver:cancel_timer(timer) end)
      fields.set(device, field, nil)
    end
  end
end

--- (Re)start the poll timer. Safe to call on init and on every infoChanged.
function poll.start(driver, device)
  poll.stop(driver, device)
  local interval = poll.interval(device.preferences)
  local offset = poll.offset(device.device_network_id, interval)

  local function begin()
    fields.set(device, fields.POLL_START_TIMER, nil)
    local timer = driver:call_on_schedule(interval, function()
      poll.once(driver, device)
    end, "pc-poll")
    fields.set(device, fields.POLL_TIMER, timer)
    return timer
  end

  if offset > 0 then
    -- The schedule starts late; the first poll below still happens now.
    local starter = driver:call_with_delay(offset, begin, "pc-poll-start")
    fields.set(device, fields.POLL_START_TIMER, starter)
  else
    begin()
  end

  driver:call_with_delay(1, function()
    poll.once(driver, device)
  end, "pc-poll-initial")
  return fields.get(device, fields.POLL_TIMER)
end

--------------------------------------------------------------------------------
-- wiring: the modules below this one that need a poll get it handed in
--------------------------------------------------------------------------------

discovery.use({
  once = function(...) return poll.once(...) end,
})
push.use({
  follow_battery = function(...) return poll.follow_battery(...) end,
})

return poll
