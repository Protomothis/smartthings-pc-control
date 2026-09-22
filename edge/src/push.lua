-- Push listener and subscription renewal (design doc §4.5, §6.4, §13.3).
--
-- One TCP server per driver, on an ephemeral port, accepting
-- `POST /pc/evt`. The service posts every device-state event to it with the
-- full §4.2 status attached, so an event needs no follow-up poll and no diff.
--
-- The split is the same as everywhere else in this driver: `parse_request`,
-- `event_for`, `apply` and `renew_delay` are pure and unit-tested, while the
-- socket and the device layer are reached through lazily required modules and
-- injectable `deps`, so loading this module in a test never opens anything.

local client = require "client"
local discovery = require "discovery"
local state = require "state"

local push = {}

push.PATH = "/pc/evt"
push.PROTOCOL = 1
-- §4.5: renew at 80% of the TTL, well before the service expires us.
push.RENEW_RATIO = 0.8
push.SUB_FIELD = "push_sub"
push.RENEW_TIMER_FIELD = "push_renew_timer"
-- A push body is a status document; 64 KiB is far more than one can be.
push.MAX_BODY = 64 * 1024
push.MAX_HEADERS = 64
push.BACKLOG = 8
-- Bounds one connection, not the accept loop.
push.READ_TIMEOUT = 5
-- Address used only to learn which local interface faces the LAN; nothing is
-- sent to it (a connected UDP socket assigns the source address at connect).
push.PROBE_ADDRESS = "8.8.8.8"
push.PROBE_PORT = 3200

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

--------------------------------------------------------------------------------
-- pure: HTTP parsing
--------------------------------------------------------------------------------

--- Header lines of a request head, lower-cased names -> values.
function push.parse_headers(head)
  local headers = {}
  if type(head) ~= "string" then
    return headers
  end
  local first = true
  for line in (head .. "\n"):gmatch("([^\n]*)\n") do
    line = line:gsub("\r$", "")
    if first then
      first = false
    elseif line ~= "" then
      local name, value = line:match("^([^:]+):%s*(.-)%s*$")
      if name then
        headers[name:lower()] = value
      end
    end
  end
  return headers
end

--- Parse a whole HTTP request: `method, path, body, headers`.
--
-- Returns `nil, nil, nil, err` when the text is not a complete request yet:
-- no blank line after the headers, or fewer body bytes than `Content-Length`
-- announced. A request without `Content-Length` (the chunk-less case: the
-- service closes the connection instead) yields whatever followed the blank
-- line, which is the whole body by then.
function push.parse_request(raw)
  if type(raw) ~= "string" or raw == "" then
    return nil, nil, nil, "empty request"
  end
  local head, body = raw:match("^(.-\r?\n)\r?\n(.*)$")
  if not head then
    return nil, nil, nil, "incomplete headers"
  end
  local request_line = head:match("^([^\r\n]*)")
  local method, path = request_line:match("^(%a[%a%-]*)%s+(%S+)%s+HTTP/%d%.%d%s*$")
  if not method then
    return nil, nil, nil, "bad request line"
  end

  local headers = push.parse_headers(head)
  local want = tonumber(headers["content-length"])
  if want then
    if want > push.MAX_BODY then
      return nil, nil, nil, "body too large"
    end
    if #body < want then
      return nil, nil, nil, "incomplete body"
    end
    body = body:sub(1, want)
  end

  -- The query string is never used, but a path that carries one must still
  -- route: "/pc/evt?x=1" is the same endpoint.
  path = path:gsub("%?.*$", "")
  return method:upper(), path, body, headers
end

--- A bare HTTP response. The hub answers `200` to anything it parsed (§6.4);
--- the body of the answer carries nothing.
function push.response(code, reason)
  code = tonumber(code) or 200
  return table.concat({
    string.format("HTTP/1.1 %d %s", code, reason or (code == 200 and "OK" or "Error")),
    "Content-Length: 0",
    "Connection: close",
    "", "",
  }, "\r\n")
end

--------------------------------------------------------------------------------
-- pure: event -> state
--------------------------------------------------------------------------------

-- §4.5 push `type` -> the §6.2 state machine event. Every other type (
-- `power.started`, `power.resumed`, `schedule.created`, `display.changed`,
-- `system.*`, `remote.*`, `session.*`) means the PC is alive and answering,
-- which is exactly `status_ok`.
push.EVENTS = {
  ["power.stopping"] = "stopping",
  ["schedule.cancelled"] = "schedule_cancelled",
}

--- The state machine event and reason for one push `type` / `data` pair.
function push.event_for(event_type, data)
  local event = push.EVENTS[event_type or ""]
  if event == "stopping" then
    return "stopping", (data or {}).reason or "unknown"
  end
  if event then
    return event, nil
  end
  return "status_ok", nil
end

--- Apply one push payload: `next_state, events`.
--
-- Pure, and deliberately the same second half as a poll: the `type` advances
-- the state machine, then the attached `status` goes through
-- `state.apply_status` exactly as `poll.once` would (§6.4).
function push.apply(device_state, payload, opts)
  payload = payload or {}
  local event, reason = push.event_for(payload.type, payload.data)
  local nxt = state.transition(device_state or state.new(), event, reason)

  local status = payload.status
  if type(status) ~= "table" then
    -- No status block: the event still moved the power state, nothing else.
    return nxt, nil, event
  end
  nxt.schedule_active = ((status.schedule or {}).active == true)
  return nxt, state.apply_status(nxt, status, opts), event
end

--------------------------------------------------------------------------------
-- pure: subscription bookkeeping
--------------------------------------------------------------------------------

--- §4.5: renew at 80% of the TTL.
function push.renew_delay(ttl)
  local seconds = tonumber(ttl) or client.DEFAULT_TTL
  return math.max(1, math.floor(seconds * push.RENEW_RATIO))
end

--- True when `sub` is a subscription that still covers `callback` at `now`.
function push.subscription_live(sub, callback, now)
  if type(sub) ~= "table" or type(sub.id) ~= "string" or sub.id == "" then
    return false
  end
  if callback and sub.callback ~= callback then
    -- The listener moved (driver restart, new port): the old subscription
    -- points at nothing.
    return false
  end
  local renew_at = tonumber(sub.renew_at)
  if renew_at and tonumber(now) and now >= renew_at then
    return false
  end
  return true
end

--- The subscription record stored in the device field.
function push.subscription(body, callback, ttl, now)
  return {
    id = (body or {}).id,
    expires_at = (body or {}).expires_at,
    callback = callback,
    renew_at = (tonumber(now) or 0) + push.renew_delay(ttl),
  }
end

--- `http://<hub ip>:<port>/pc/evt` for a listener.
function push.callback_url(listener)
  listener = listener or {}
  if type(listener.ip) ~= "string" or listener.ip == "" or not tonumber(listener.port) then
    return nil
  end
  return string.format("http://%s:%d%s", listener.ip, math.floor(listener.port), push.PATH)
end

--------------------------------------------------------------------------------
-- the listener
--------------------------------------------------------------------------------

-- One listener per driver. Weak keys so a driver that goes away takes its
-- entry with it; nothing else in the driver holds module-level state.
local listeners = setmetatable({}, { __mode = "k" })

--- The running listener for `driver`, or nil.
function push.listener(driver)
  if driver == nil then
    return nil
  end
  return listeners[driver]
end

--- The hub's LAN address, as the PC will see it.
--
-- §6.4 says `driver:get_ip()`; hub firmware that does not have it gets the
-- documented LAN-driver fallback: connect a UDP socket towards the PC (no
-- datagram is sent) and read the source address the kernel picked, which is
-- the interface facing that PC.
function push.hub_ip(driver, target, deps)
  deps = deps or {}
  if type(deps.hub_ip) == "string" then
    return deps.hub_ip
  end
  if driver then
    local ok, ip = pcall(function() return driver:get_ip() end)
    if ok and type(ip) == "string" and ip ~= "" then
      return ip
    end
  end
  local socket = deps.socket
  if not socket then
    local loaded, cosock = pcall(require, "cosock")
    if not loaded then
      return nil
    end
    socket = cosock.socket
  end
  local ok, ip = pcall(function()
    local udp = socket.udp()
    udp:setpeername(target or push.PROBE_ADDRESS, push.PROBE_PORT)
    local address = udp:getsockname()
    udp:close()
    return address
  end)
  if ok and type(ip) == "string" and ip ~= "" and ip ~= "0.0.0.0" then
    return ip
  end
  return nil
end

--- Open the listener for `driver` (idempotent).
-- `deps.socket` / `deps.spawn` / `deps.hub_ip` replace cosock in tests.
function push.start(driver, deps)
  deps = deps or {}
  local existing = push.listener(driver)
  if existing and not existing.closed then
    return existing
  end
  local log = logger()

  local cosock = deps.cosock
  if not cosock and not deps.socket then
    local loaded, mod = pcall(require, "cosock")
    if not loaded then
      log.warn("cosock is not available, push listener not started")
      return nil
    end
    cosock = mod
  end
  local socket = deps.socket or cosock.socket

  local ok, listener = pcall(function()
    local server = assert(socket.tcp())
    -- cosock only accepts timeout/keepalive/tcp-nodelay; reuseaddr is rejected
    -- by the hub ("unknown variant") and an ephemeral port needs none.
    assert(server:bind("0.0.0.0", 0))
    assert(server:listen(push.BACKLOG))
    local _, port = server:getsockname()
    return { sock = server, port = tonumber(port), ip = push.hub_ip(driver, nil, deps), closed = false }
  end)
  if not ok or not listener or not listener.port then
    log.warn("push listener could not be opened: " .. tostring(listener))
    return nil
  end
  if not listener.ip then
    log.warn("hub IP unknown, push listener is open but cannot be subscribed to")
  end
  listeners[driver] = listener

  local spawn = deps.spawn or (cosock and cosock.spawn)
  if spawn then
    spawn(function() push.serve(driver, listener, deps) end, "pc-push-listener")
  end
  log.info(string.format("push listener on %s:%d%s", tostring(listener.ip), listener.port, push.PATH))
  return listener
end

--- Close the listener (driver shutdown; nothing calls this on the hub today).
function push.shutdown(driver)
  local listener = push.listener(driver)
  if not listener then
    return false
  end
  listener.closed = true
  pcall(function() listener.sock:close() end)
  listeners[driver] = nil
  return true
end

--- Accept loop. Runs in its own cosock task.
function push.serve(driver, listener, deps)
  local log = logger()
  while not listener.closed do
    local sock, err = listener.sock:accept()
    if sock then
      local ok, handle_err = pcall(push.handle_connection, driver, sock, deps)
      if not ok then
        log.warn("push connection failed: " .. tostring(handle_err))
      end
      pcall(function() sock:close() end)
    elseif err and err ~= "timeout" then
      log.warn("push accept failed: " .. tostring(err))
      -- A broken listening socket would otherwise spin this loop forever.
      listener.closed = true
    end
  end
  pcall(function() listener.sock:close() end)
end

--- Read one request off `sock`: `method, path, body`.
function push.read_request(sock)
  pcall(function() sock:settimeout(push.READ_TIMEOUT) end)
  local lines = {}
  while true do
    local line, err = sock:receive()
    if not line then
      return nil, nil, nil, tostring(err)
    end
    line = line:gsub("\r$", "")
    if line == "" then
      break
    end
    lines[#lines + 1] = line
    if #lines > push.MAX_HEADERS then
      return nil, nil, nil, "too many headers"
    end
  end
  local head = table.concat(lines, "\r\n") .. "\r\n\r\n"

  local length = tonumber(push.parse_headers(head)["content-length"]) or 0
  if length > push.MAX_BODY then
    return nil, nil, nil, "body too large"
  end
  local body = ""
  if length > 0 then
    local received, err = sock:receive(length)
    if not received then
      return nil, nil, nil, tostring(err)
    end
    body = received
  end
  return push.parse_request(head .. body)
end

--- Serve one connection: parse, route, answer.
function push.handle_connection(driver, sock, deps)
  local log = logger()
  local method, path, body, err = push.read_request(sock)
  if not method then
    log.debug("push request ignored: " .. tostring(err))
    pcall(function() sock:send(push.response(400, "Bad Request")) end)
    return false
  end
  if method ~= "POST" or path ~= push.PATH then
    log.debug(string.format("push %s %s ignored", method, path))
    pcall(function() sock:send(push.response(404, "Not Found")) end)
    return false
  end
  -- §4.5: the service waits at most 2s (1.5s for power.stopping) and retries
  -- once, so the answer goes out before the payload is applied.
  pcall(function() sock:send(push.response(200)) end)
  push.deliver(driver, body, deps)
  return true
end

--- Decode one raw body and hand it to the matching device.
function push.deliver(driver, body, deps)
  deps = deps or {}
  local log = logger()
  local json = deps.json or require "st.json"
  local ok, payload = pcall(json.decode, body)
  if not ok or type(payload) ~= "table" then
    log.warn("push body is not JSON")
    return false, "bad json"
  end
  if payload.protocol ~= nil and payload.protocol ~= push.PROTOCOL then
    log.warn("push from an incompatible protocol: " .. tostring(payload.protocol))
    return false, "incompatible"
  end
  return push.route(driver, payload, deps)
end

--- Find the device for `payload.machine_id` (§13.3) and apply the event.
function push.route(driver, payload, deps)
  deps = deps or {}
  local log = logger()
  payload = payload or {}
  local machine_id = payload.machine_id
  if type(machine_id) ~= "string" or machine_id == "" then
    log.warn("push without a machine_id, ignored")
    return false, "no machine id"
  end

  local devices = deps.devices
  if not devices and driver then
    local ok, list = pcall(function() return driver:get_devices() end)
    devices = ok and list or nil
  end
  local device = discovery.find(devices, machine_id)
  if not device then
    -- §13.3: another PC on the LAN, or one this hub does not own.
    log.info("push for an unknown machine_id, ignored")
    return false, "unknown machine id"
  end
  return push.apply_to_device(driver, device, payload, deps)
end

--- Apply a payload to one device through the same glue a poll uses (§6.4).
function push.apply_to_device(driver, device, payload, deps)
  deps = deps or {}
  local poll = deps.poll or require "poll"
  local nxt, events, event = push.apply(poll.get_state(device), payload, {
    now = poll.now(),
    lang = poll.lang(device),
  })
  poll.set_state(device, nxt)

  if event == "status_ok" then
    -- A push proves the PC is up, so a pending wake timeout is done with
    -- (§6.3) and the message it would have written is not wanted.
    local ok, wol = pcall(require, "wol")
    if ok then
      pcall(function() wol.cancel_wake(driver, device) end)
    end
  end

  if events then
    poll.emit(device, events)
    pcall(function() device:online() end)
  else
    poll.emit_power(device, nxt)
  end

  -- §5.2: the display child follows `status.display`.
  local loaded, display = pcall(require, "display")
  if loaded then
    pcall(function() display.sync(driver, device, payload.status) end)
  end
  return true
end

--------------------------------------------------------------------------------
-- subscriptions (§4.5)
--------------------------------------------------------------------------------

--- Subscribe `device` to this hub's listener, or renew when due.
--
-- Called after every successful poll (§6.4). Returns `true` when a live
-- subscription exists afterwards; a failure is not an error — the driver keeps
-- polling and tries again on the next successful poll.
function push.ensure(driver, device, deps)
  deps = deps or {}
  local listener = push.listener(driver)
  if listener and not listener.ip then
    -- The probe at start time had no route to work with (no device had an
    -- address yet). This device has one, so aim at it: the source address the
    -- kernel picks for that PC is exactly the callback host the service will
    -- compare against (§4.5).
    listener.ip = push.hub_ip(driver, ((device or {}).preferences or {}).ipAddress, deps)
  end
  local callback = push.callback_url(listener)
  if not callback then
    return false, "no listener"
  end

  local now = (deps.now or os.time)()
  local existing = device:get_field(push.SUB_FIELD)
  if push.subscription_live(existing, callback, now) then
    return true, "live"
  end

  local log = logger()
  local ok, body, kind = client.subscribe(device, callback, client.DEFAULT_TTL, deps)
  if not ok then
    -- 401 (secret changed), 400 (callback refused) or unreachable: drop what
    -- we thought we had and rely on polling (§6.4).
    device:set_field(push.SUB_FIELD, nil)
    push.cancel_renew(driver, device)
    log.debug(string.format("subscribe failed (%s), polling only", tostring(kind)))
    return false, kind
  end

  local sub = push.subscription(body, callback, client.DEFAULT_TTL, now)
  device:set_field(push.SUB_FIELD, sub)
  log.info(string.format("subscribed %s to %s", tostring(sub.id), callback))
  push.schedule_renew(driver, device, client.DEFAULT_TTL, deps)
  return true, "subscribed"
end

--- Arm the renewal timer at 80% of the TTL (§4.5).
function push.schedule_renew(driver, device, ttl, deps)
  if not driver then
    return nil
  end
  push.cancel_renew(driver, device)
  local ok, timer = pcall(function()
    return driver:call_with_delay(push.renew_delay(ttl), function()
      device:set_field(push.RENEW_TIMER_FIELD, nil)
      push.ensure(driver, device, deps)
    end, "pc-push-renew")
  end)
  if ok then
    device:set_field(push.RENEW_TIMER_FIELD, timer)
    return timer
  end
  return nil
end

function push.cancel_renew(driver, device)
  local timer = device:get_field(push.RENEW_TIMER_FIELD)
  if timer then
    pcall(function() driver:cancel_timer(timer) end)
    device:set_field(push.RENEW_TIMER_FIELD, nil)
  end
end

--- Drop the subscription for a device that is going away (§4.5).
function push.stop(driver, device, deps)
  push.cancel_renew(driver, device)
  local sub = device:get_field(push.SUB_FIELD)
  device:set_field(push.SUB_FIELD, nil)
  if type(sub) ~= "table" or not sub.id then
    return false
  end
  -- Best effort: an unreachable PC expires the subscription by itself.
  local ok = pcall(function() client.unsubscribe(device, sub.id, deps) end)
  if ok then
    logger().info("unsubscribed " .. tostring(sub.id))
  end
  return true
end

return push
