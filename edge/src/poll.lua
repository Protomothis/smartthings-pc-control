-- Polling / health (design doc §6.1) and the device-layer glue that turns the
-- pure event records from state.lua into `device:emit_event` calls.
--
-- This module owns the per-device runtime state (a plain table in a device
-- field) and the poll timer. wol.lua reuses the glue functions so the wake
-- timeout can update the device without knowing about st.capabilities.

local caps = require "caps"
local client = require "client"
local i18n = require "i18n"
local state = require "state"

local poll = {}

poll.STATE_FIELD = "pc_state"
poll.TIMER_FIELD = "poll_timer"
poll.START_TIMER_FIELD = "poll_start_timer"
poll.MAC_FIELD = "wol_mac"
-- #82: the last `pcExec.lastAction` value emitted for this device.
poll.ACTION_FIELD = "last_action"
-- #84: the `pcPlanner.planCommand` the user picked for the schedule row.
poll.PLAN_FIELD = "plan_command"
-- #85: which generation of capability ids this device's rows were painted for.
-- A renamed capability (pcRun -> pcExec, pcPlan -> pcPlanner) starts with
-- every attribute unset on the hub, so the persisted "already painted" fields
-- would otherwise skip a device that has been migrated (platform notes "허브의 정의 캐시").
poll.ROWS_FIELD = "rows_painted"
-- #86 bumps it again: `pcVersion` is a new capability, so its row starts unset
-- on every existing device and has to be painted once.
-- #88 bumps it once more: `pcCountdown` became `pcPlanner` and gained
-- `minutesPick`, so every schedule row of a migrated device starts out unset.
poll.ROWS_VERSION = "88c"
poll.WOL_READY_FIELD = "wol_ready"
poll.DEFAULT_INTERVAL = 30
-- First service release that speaks protocol 1 (§3).
poll.MIN_SERVICE_VERSION = "1.1.0"

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  -- Loading poll.lua outside the hub (tests, syntax check) must not fail.
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

-- Resolve a capability object from an id produced by state.lua. Custom
-- capabilities do not exist until the account owner creates them (#72), so a
-- miss is logged and skipped rather than raised.
local function capability_for(id)
  local ok, capabilities = pcall(require, "st.capabilities")
  if not ok then
    return nil
  end
  if id == state.CAP_SWITCH then
    return capabilities.switch
  end
  local found, cap = pcall(function() return capabilities[id] end)
  if found then
    return cap
  end
  return nil
end

--- `pcInfo.lastSeen`: the hub's local clock time of the last good poll.
--- A tile the user glances at wants "14:05:12", not an ISO timestamp, and the
--- date is never interesting for a value that is at most a few minutes old.
function poll.now()
  return os.date("%H:%M:%S")
end

function poll.lang(device)
  return ((device or {}).preferences or {}).language
end

--- Runtime state for `device`, created on first use.
function poll.get_state(device)
  return device:get_field(poll.STATE_FIELD) or state.new()
end

function poll.set_state(device, s)
  device:set_field(poll.STATE_FIELD, s)
end

--- Re-exported so wol.lua does not have to require state.lua as well.
function poll.transition(s, event, arg)
  return state.transition(s, event, arg)
end

-- #86: what a record's `force = true` becomes on the hub. An event whose value
-- equals the current one is dropped by the platform, and the app - which is
-- waiting for exactly that attribute to change - keeps its spinner up until it
-- gives up with an error. Cancelling with nothing scheduled and re-picking the
-- value a list already shows are both that case (measured on the phone
-- 2026-09-22, platform notes "상세 화면(detailView) 위젯"), so every emit that answers an app command is forced.
poll.FORCE = { state_change = true }

-- #86: the rows the schedule list is bound to. `schedule` and `cancel` are
-- answered on them, and cancelling with nothing scheduled leaves every one of
-- them exactly as it was, which is the case that used to spin and then fail.
poll.SCHEDULE_ROWS = {
  [caps.SCHEDULE .. ".status"] = true,
  [caps.SCHEDULE .. ".active"] = true,
  [caps.SCHEDULE .. ".summary"] = true,
}

--- Mark every event as forced (a repaint after a profile change: the hub
--- de-duplicates unchanged values, but the cloud record of the new profile is
--- empty until it receives them).
function poll.force_all(events)
  for _, e in ipairs(events or {}) do
    e.force = true
  end
  return events
end

--- Mark the events whose `<cap>.<attr>` is in `keys` as forced (#86).
-- Returns the same list, so it can wrap a call.
function poll.force_rows(events, keys)
  if type(keys) ~= "table" then
    return events
  end
  for _, e in ipairs(events or {}) do
    if keys[tostring(e.cap) .. "." .. tostring(e.attr)] then
      e.force = true
    end
  end
  return events
end

--- Emit a list of `{ cap, attr, value, force? }` records.
-- `force` marks an emit that answers a command from the app: it goes out with
-- `{ state_change = true }` so the platform delivers it even when the value did
-- not change. Ordinary poll updates stay unforced.
function poll.emit(device, events)
  local log = logger()
  for _, e in ipairs(events or {}) do
    local cap = capability_for(e.cap)
    local attr = cap and cap[e.attr]
    if attr then
      local ok, err = pcall(function()
        if e.force then
          device:emit_event(attr(e.value, poll.FORCE))
        else
          device:emit_event(attr(e.value))
        end
      end)
      if not ok then
        log.warn(string.format("emit %s.%s failed: %s", tostring(e.cap), tostring(e.attr), tostring(err)))
      end
    else
      log.debug(string.format("capability %s.%s not available, skipped", tostring(e.cap), tostring(e.attr)))
    end
  end
end

--- Emit `switch` + `powerState` for a runtime state (§6.2).
function poll.emit_power(device, s)
  local power = (s or {}).power_state or state.UNKNOWN
  poll.emit(device, {
    { cap = state.CAP_SWITCH, attr = "switch", value = state.switch_for(power) },
    { cap = caps.POWER_STATE, attr = "powerState", value = power },
  })
end

--- Emit only `pcInfo.message`.
function poll.emit_message(device, message)
  poll.emit(device, { { cap = caps.STATUS, attr = "message", value = message or "" } })
end

--- Emit `pcInfo.connection` + `pcInfo.message` + `pcInfo.summary` (#78).
--- The summary is the only status row the detail view still shows, so a failed
--- poll has to rewrite it as well. #82: the power word is not in it any more -
--- the `pcPower` row right above says that.
-- #85: the `versions` row goes out here as well. A PC we cannot reach has no
-- service version to report, but the driver half is still the answer to "did
-- my update land?", and a row that was never emitted reads as
-- "-" (platform notes "상세 화면(detailView) 위젯"). `state.versions` writes "v?" for the service half.
function poll.emit_connection(device, connection, message)
  local lang = poll.lang(device)
  poll.emit(device, {
    { cap = caps.STATUS, attr = "connection", value = connection },
    { cap = caps.STATUS, attr = "message", value = message or "" },
    { cap = caps.STATUS, attr = "summary",
      value = state.status_summary(connection, lang) },
    -- #86: the row lives on its own capability now; pcInfo keeps the attribute
    -- (and the emit) because its definition cannot change without a rename.
    { cap = caps.VERSION, attr = "versions", value = state.versions(nil, lang) },
    { cap = caps.STATUS, attr = "versions", value = state.versions(nil, lang) },
  })
end

--- Emit `pcExec.lastAction` and remember it (#82, #84).
--
-- #84: the only value this is ever called with is `none`. Closing the detail
-- view's command list without picking anything sends the row's CURRENT value
-- as the `execute` argument (measured on the phone 2026-09-22, platform notes "상세 화면(detailView) 위젯"), so the
-- row has to rest on a value that is both a valid argument and a no-op -
-- otherwise the cloud rejects the command with "network or server error"
-- before it ever reaches the hub. What ran is told by `lastCommand` instead.
-- Anything that is not a `lastAction` enum value is coerced to `none` rather
-- than emitted, because the hub rejects a value outside the enum.
-- @param action a `lastAction` enum value
-- @param force #86: true when this emit answers an `execute` from the app. The
--   row rests on `none` and therefore never changes value, so without
--   `state_change` the platform drops the event and the app spins until it
--   errors out (platform notes "상세 화면(detailView) 위젯").
function poll.emit_action(device, action, force)
  local value = state.is_action(action) and action or state.ACTION_NONE
  pcall(function() device:set_field(poll.ACTION_FIELD, value, { persist = true }) end)
  poll.emit(device, {
    { cap = caps.COMMAND, attr = "lastAction", value = value, force = force == true },
  })
  return value
end

--- #86: answer an `execute` on the row the app is watching.
--
-- The command list rests on `none` whatever is picked (#84), so the attribute
-- it is bound to never changes and the app's spinner would run out into an
-- error. A forced re-emit of the resting value ends it.
function poll.answer_action(device)
  local seen
  pcall(function() seen = device:get_field(poll.ACTION_FIELD) end)
  return poll.emit_action(device, seen, true)
end

--- Paint `lastAction` as `none` on a device that has never been told one, so
--- the detail-view list reads "-" nowhere (#82). Does nothing afterwards.
function poll.ensure_action(device)
  local seen
  pcall(function() seen = device:get_field(poll.ACTION_FIELD) end)
  if type(seen) == "string" and seen ~= "" then
    return false
  end
  poll.emit_action(device, state.ACTION_NONE)
  return true
end

--- Emit `pcPlanner.planCommand` and remember it (#84, moved in #85).
--
-- The command a `pcPlanner.schedule` without an explicit command runs. It is
-- the user's own choice, made on the detail view, so it is persisted rather
-- than derived from a status body. An unschedulable value is coerced (§3.3).
--
-- #85: emitted under the schedule capability, because the app puts a detail row
-- in the card of the capability that owns it and this row belongs next to the
-- schedule it configures.
-- #86: `force` for the same reason as `emit_action` - picking the value the row
-- already shows (restart -> restart) changes nothing, and the app waits for a
-- change it will never see.
function poll.emit_plan_command(device, command, force)
  local value = state.plan_command_for(command)
  pcall(function() device:set_field(poll.PLAN_FIELD, value, { persist = true }) end)
  poll.emit(device, {
    { cap = caps.SCHEDULE, attr = "planCommand", value = value, force = force == true },
  })
  return value
end

--- #88: answer a `schedule` on the row the 예약 시간 list is bound to.
--
-- The list rests on `minutesPick`, whose only value is "-1" (the no-op
-- `minutes`), so the attribute never changes and the platform would drop the
-- event - leaving the app spinning until it fails, exactly as the command row
-- did before #86. Forced, the re-emit ends the spinner, and it goes out for
-- every `schedule` the app sends: a picked delay, the Cancel entry and the
-- dismissed picker alike.
function poll.answer_minutes_pick(device)
  poll.emit(device, {
    { cap = caps.SCHEDULE, attr = "minutesPick", value = state.MINUTES_PICK, force = true },
  })
  return state.MINUTES_PICK
end

--- The command this device schedules when none is given: the picked one, else
--- the `offAction` preference when that is schedulable, else `shutdown`.
function poll.plan_command(device)
  local picked
  pcall(function() picked = device:get_field(poll.PLAN_FIELD) end)
  return state.plan_command_for(picked, ((device or {}).preferences or {}).offAction)
end

--- Paint `planCommand` on a device that has never picked one (#84): an
--- attribute that was never emitted reads as "-" on the phone, and the row is
--- a list, which does not open at all without a value (platform notes "상세 화면(detailView) 위젯").
function poll.ensure_plan_command(device)
  local seen
  pcall(function() seen = device:get_field(poll.PLAN_FIELD) end)
  if state.is_plan_command(seen) then
    return false
  end
  poll.emit_plan_command(device, poll.plan_command(device))
  return true
end

--- #85: paint every pcExec and pcPlanner attribute once, so no row of either
--- card reads "-" and the app stops saying the device has not reported all of
--- its state.
--
-- Called from `added` and from `init`: a device that was migrated onto the new
-- capability ids (platform notes "허브의 정의 캐시") has never emitted any of them, even though the
-- `last_action` / `plan_command` fields from the old ones survived, so the
-- version stamp forces one repaint per generation instead of trusting them.
function poll.ensure_rows(device)
  local painted
  pcall(function() painted = device:get_field(poll.ROWS_FIELD) end)
  if painted == poll.ROWS_VERSION then
    return false
  end
  pcall(function() device:set_field(poll.ROWS_FIELD, poll.ROWS_VERSION, { persist = true }) end)

  poll.repaint(device)
  return true
end

--- Repaint every row with forced events: after a profile change (`infoChanged`)
--- the cloud starts the new profile with empty states, and the hub would
--- otherwise drop the re-emit of values it considers unchanged.
function poll.repaint(device)
  local seen
  pcall(function() seen = device:get_field(poll.ACTION_FIELD) end)
  poll.emit_action(device, seen, true)
  poll.emit_plan_command(device, poll.plan_command(device), true)
  poll.emit(device, poll.force_all(state.initial_rows(poll.lang(device))))
end

--- err_kind (client.lua) -> `pcInfo.connection` enum value (§4), or nil
--- when the failure says nothing about the connection and the last state
--- should stand.
function poll.connection_for(kind)
  if kind == "unauthorized" or kind == "unreachable" or kind == "incompatible" then
    return kind
  end
  -- 403: the secret was accepted, the hub is not on the allow-list. The enum
  -- has no separate value for it (§3.1), so it shares `unauthorized` and the
  -- message tells the two apart.
  if kind == "forbidden" then
    return "unauthorized"
  end
  -- "badrequest" means the service answered but refused; from the app's point
  -- of view that is a protocol problem, not a connection problem.
  if kind == "badrequest" then
    return "incompatible"
  end
  if kind == "ratelimited" then
    -- §3.1: nothing has changed about the PC, so do not repaint anything.
    return nil
  end
  return "ok"
end

--- Human-readable text for an err_kind (§3.1). `body` is the decoded response
--- when there was one: a higher `protocol` means our driver is the old side,
--- and a missing or lower one means the service is.
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
    -- The service says which command or argument it refused (§3.3); quoting it
    -- is more use than "the service rejected the command" on its own.
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

--- One poll cycle: GET /st/v1/status, advance the state machine, emit, and set
--- health online/offline. Also used by `refresh` and after a command.
--- `opts.note` is a one-off confirmation to show in `pcInfo.message` when
--- nothing more important applies (§4, state.MESSAGE_ORDER); `opts.deps` is
--- the injected http/json/ltn12 the tests use instead of a socket.
function poll.once(driver, device, opts)
  opts = opts or {}
  local prefs = device.preferences or {}
  local lang = prefs.language
  local current = poll.get_state(device)

  if not client.device_base_url(device) then
    -- Freshly added device: nothing to poll until the user fills in the IP.
    poll.emit_connection(device, "unreachable", i18n.t(lang, "no_ip"))
    -- Never offline: the driver on the hub is what acts, and SmartThings
    -- greys out an offline device, which would take Wake-on-LAN away exactly
    -- when it is needed. "PC off" is powerState/switch, not health.
    pcall(function() device:online() end)
    return false, "no ip"
  end

  local ok, body, kind = client.get_status(device, opts.deps)

  if ok then
    local nxt = state.transition(current, "status_ok")
    nxt.schedule_active = ((body or {}).schedule or {}).active == true
    poll.set_state(device, nxt)
    -- A successful status while waking means the PC is up: drop the 90s timeout.
    local wol = require "wol"
    wol.cancel_wake(driver, device)
    -- §6.4: remember the WoL-capable adapter's MAC. A driver cannot write its
    -- own preferences, so this is kept as a field and used when `macAddress`
    -- is left empty.
    local mac = state.wol_mac(body)
    if mac then
      -- Persisted: the MAC must survive a hub or driver restart while the PC
      -- is off, or "wake" has nothing to send to.
      device:set_field(poll.MAC_FIELD, mac, { persist = true })
    end
    -- §6.4: remembered so `switch on` can say "WoL is off on the adapter"
    -- right away instead of at the next poll.
    device:set_field(poll.WOL_READY_FIELD, ((body or {}).wol or {}).ready == true)
    -- §6.5: the identity. A manually added device learns its machine_id here,
    -- so SSDP can later recognise it instead of creating a duplicate.
    poll.remember_identity(device, body)
    -- #82/#84/#85: no status body carries `lastAction` or `planCommand`, and a
    -- migrated device has emitted nothing at all under the new capability ids,
    -- so the resting values go out first and the status body overwrites the
    -- rows it does know about.
    poll.ensure_rows(device)
    -- #86: `opts.force` names the rows this poll is answering a command on
    -- (the schedule rows after `schedule` / `cancel`). They go out with
    -- `state_change = true` so the app's spinner ends even when the value is
    -- the one it already had; everything else stays an ordinary update.
    local events = state.apply_status(nxt, body, {
      now = poll.now(),
      lang = lang,
      note = opts.note,
    })
    -- opts.force == true: a repaint after a profile change. The hub drops
    -- re-emits of values it considers unchanged, so every row goes out with
    -- state_change = true or the cloud record of the new profile stays empty.
    if opts.force == true then
      poll.force_all(events)
    else
      poll.force_rows(events, opts.force)
    end
    poll.emit(device, events)
    poll.ensure_action(device)
    poll.ensure_plan_command(device)
    pcall(function() device:online() end)
    -- §6.3: with the PC answering, ask it to push instead of waiting for the
    -- next poll. A failure here only means the driver keeps polling.
    pcall(function() require("push").ensure(driver, device, opts.deps) end)
    return true
  end

  local connection = poll.connection_for(kind)
  if not connection then
    -- §3.1: rate limited. The PC is fine, we simply asked too often (the poll
    -- right after a command can land inside the same second), so leave every
    -- attribute and the health status as they were.
    logger().warn(string.format("poll skipped: %s", i18n.t("en", kind)))
    return false, kind
  end

  local nxt = current
  if kind == "unreachable" then
    nxt = state.transition(current, "unreachable")
    pcall(function() device:online() end) -- see above: health stays online so the switch can wake the PC
    -- §6.5: the PC may just have moved to another address. One targeted SSDP
    -- search (rate limited to once per 5 minutes per device) before the next
    -- poll is cheaper than waiting for the user to notice.
    pcall(function() require("discovery").refresh(driver, device, opts.deps) end)
  end
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  poll.emit_connection(device, connection, poll.message_for(kind, body, lang))
  return false, kind
end

--- Store what a status body says about the PC's identity (§6.5). Returns true
--- when something changed.
function poll.remember_identity(device, body)
  body = body or {}
  local discovery = require "discovery"
  local changed = false
  if type(body.machine_id) == "string" and body.machine_id ~= ""
      and device:get_field(discovery.MACHINE_FIELD) ~= body.machine_id then
    device:set_field(discovery.MACHINE_FIELD, body.machine_id, { persist = true })
    changed = true
  end
  if type(body.hostname) == "string" and body.hostname ~= ""
      and device:get_field(discovery.HOSTNAME_FIELD) ~= body.hostname then
    device:set_field(discovery.HOSTNAME_FIELD, body.hostname, { persist = true })
    changed = true
  end
  return changed
end

--- Poll interval in seconds from the `pollInterval` preference (§7).
function poll.interval(prefs)
  local seconds = tonumber((prefs or {}).pollInterval)
  if seconds and seconds >= 5 then
    return math.floor(seconds)
  end
  return poll.DEFAULT_INTERVAL
end

--- §6.7: spread the devices' polls over the interval so N PCs are not all
--- asked in the same second. A hash of the DNI is deterministic (the same
--- device keeps its slot across restarts) and needs no coordination between
--- devices, unlike an index that would have to be recomputed on every add and
--- remove. FNV-1a, 32 bit.
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
  return hash % interval
end

function poll.stop(driver, device)
  for _, field in ipairs({ poll.TIMER_FIELD, poll.START_TIMER_FIELD }) do
    local timer = device:get_field(field)
    if timer then
      pcall(function() driver:cancel_timer(timer) end)
      device:set_field(field, nil)
    end
  end
end

--- (Re)start the poll timer. Safe to call on init and on every infoChanged.
function poll.start(driver, device)
  poll.stop(driver, device)
  local interval = poll.interval(device.preferences)
  local offset = poll.offset(device.device_network_id, interval)

  local function begin()
    device:set_field(poll.START_TIMER_FIELD, nil)
    local timer = driver:call_on_schedule(interval, function()
      poll.once(driver, device)
    end, "pc-poll")
    device:set_field(poll.TIMER_FIELD, timer)
    return timer
  end

  if offset > 0 then
    -- The schedule itself starts late; the first poll below still happens now,
    -- so the tiles do not wait for the offset.
    local starter = driver:call_with_delay(offset, begin, "pc-poll-start")
    device:set_field(poll.START_TIMER_FIELD, starter)
  else
    begin()
  end

  -- Prime the tiles instead of waiting a whole interval for the first tick.
  driver:call_with_delay(1, function()
    poll.once(driver, device)
  end, "pc-poll-initial")
  return device:get_field(poll.TIMER_FIELD)
end

return poll
