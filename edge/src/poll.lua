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
poll.WOL_READY_FIELD = "wol_ready"
poll.DEFAULT_INTERVAL = 30
-- First service release that speaks protocol 1 (§4).
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

--- `pcHealth.lastSeen`: the hub's local clock time of the last good poll.
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

--- Emit a list of `{ cap, attr, value }` records.
function poll.emit(device, events)
  local log = logger()
  for _, e in ipairs(events or {}) do
    local cap = capability_for(e.cap)
    local attr = cap and cap[e.attr]
    if attr then
      local ok, err = pcall(function() device:emit_event(attr(e.value)) end)
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

--- Emit only `pcHealth.message`.
function poll.emit_message(device, message)
  poll.emit(device, { { cap = caps.STATUS, attr = "message", value = message or "" } })
end

--- Emit `pcHealth.connection` + `pcHealth.message` + `pcHealth.summary` (#78).
--- The summary is the only status row the detail view still shows, so a failed
--- poll has to rewrite it as well; the power state comes from the device so the
--- line stays consistent with the tile.
function poll.emit_connection(device, connection, message)
  local s = poll.get_state(device)
  poll.emit(device, {
    { cap = caps.STATUS, attr = "connection", value = connection },
    { cap = caps.STATUS, attr = "message", value = message or "" },
    { cap = caps.STATUS, attr = "summary",
      value = state.status_summary((s or {}).power_state, connection, nil, poll.lang(device)) },
  })
end

--- err_kind (client.lua) -> `pcHealth.connection` enum value (§5.1), or nil
--- when the failure says nothing about the connection and the last state
--- should stand.
function poll.connection_for(kind)
  if kind == "unauthorized" or kind == "unreachable" or kind == "incompatible" then
    return kind
  end
  -- 403: the secret was accepted, the hub is not on the allow-list. The enum
  -- has no separate value for it (§4.1), so it shares `unauthorized` and the
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
    -- §8: nothing has changed about the PC, so do not repaint anything.
    return nil
  end
  return "ok"
end

--- Human-readable text for an err_kind (§6.1). `body` is the decoded response
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
    -- The service says which command or argument it refused (§4.3); quoting it
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
--- `opts.note` is a one-off confirmation to show in `pcHealth.message` when
--- nothing more important applies (§5.1, state.MESSAGE_ORDER); `opts.deps` is
--- the injected http/json/ltn12 the tests use instead of a socket.
function poll.once(driver, device, opts)
  opts = opts or {}
  local prefs = device.preferences or {}
  local lang = prefs.language
  local current = poll.get_state(device)

  if not client.device_base_url(device) then
    -- Freshly added device: nothing to poll until the user fills in the IP.
    poll.emit_connection(device, "unreachable", i18n.t(lang, "no_ip"))
    pcall(function() device:offline() end)
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
    -- §5.4: remember the WoL-capable adapter's MAC. A driver cannot write its
    -- own preferences, so this is kept as a field and used when `macAddress`
    -- is left empty.
    local mac = state.wol_mac(body)
    if mac then
      -- Persisted: the MAC must survive a hub or driver restart while the PC
      -- is off, or "wake" has nothing to send to.
      device:set_field(poll.MAC_FIELD, mac, { persist = true })
    end
    -- §6.3: remembered so `switch on` can say "WoL is off on the adapter"
    -- right away instead of at the next poll.
    device:set_field(poll.WOL_READY_FIELD, ((body or {}).wol or {}).ready == true)
    -- §13.1: the identity. A manually added device learns its machine_id here,
    -- so SSDP can later recognise it instead of creating a duplicate.
    poll.remember_identity(device, body)
    poll.emit(device, state.apply_status(nxt, body, {
      now = poll.now(),
      lang = lang,
      note = opts.note,
    }))
    pcall(function() device:online() end)
    -- §6.4: with the PC answering, ask it to push instead of waiting for the
    -- next poll. A failure here only means the driver keeps polling.
    pcall(function() require("push").ensure(driver, device, opts.deps) end)
    return true
  end

  local connection = poll.connection_for(kind)
  if not connection then
    -- §8: rate limited. The PC is fine, we simply asked too often (the poll
    -- right after a command can land inside the same second), so leave every
    -- attribute and the health status as they were.
    logger().warn(string.format("poll skipped: %s", i18n.t("en", kind)))
    return false, kind
  end

  local nxt = current
  if kind == "unreachable" then
    nxt = state.transition(current, "unreachable")
    pcall(function() device:offline() end)
    -- §13.2: the PC may just have moved to another address. One targeted SSDP
    -- search (rate limited to once per 5 minutes per device) before the next
    -- poll is cheaper than waiting for the user to notice.
    pcall(function() require("discovery").refresh(driver, device, opts.deps) end)
  end
  poll.set_state(device, nxt)
  poll.emit_power(device, nxt)
  poll.emit_connection(device, connection, poll.message_for(kind, body, lang))
  return false, kind
end

--- Store what a status body says about the PC's identity (§13.1). Returns true
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

--- Poll interval in seconds from the `pollInterval` preference (§5.4).
function poll.interval(prefs)
  local seconds = tonumber((prefs or {}).pollInterval)
  if seconds and seconds >= 5 then
    return math.floor(seconds)
  end
  return poll.DEFAULT_INTERVAL
end

--- §13.3: spread the devices' polls over the interval so N PCs are not all
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
