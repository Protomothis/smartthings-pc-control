-- Wake-on-LAN: magic packet building and the wake sequence of design doc §6.4
-- (send immediately, at 2s and at 5s, to ports 7 and 9; look every 5s while
-- waking; give up after 180s and one last look).
--
-- `magic_packet` and `parse_mac` are pure and unit-tested. `send` takes its
-- socket lazily so loading this module in a test never pulls in cosock, and
-- `wake` its device-layer glue from `deps.devices` when a test gives one. The
-- poll it needs while waking is poll.lua's, handed in through `wol.use` (or
-- `deps.once`), so this module does not require it.

local fields = require "device.fields"
local i18n = require "i18n"
local rows = require "device.rows"
local state = require "state"

local wol = {}

-- The device-layer glue `wake` uses.
local DEVICES = {
  emit_message = rows.emit_message,
  emit_power = rows.emit_power,
  ensure_action = rows.ensure_action,
  get_state = fields.state,
  set_state = fields.set_state,
  transition = state.transition,
}

wol.PORTS = { 7, 9 }
-- §6.4: three attempts. A PC that missed the first packet because the switch
-- was still learning the port usually catches the second or third.
wol.RETRY_DELAYS = { 0, 2, 5 }
-- #149: how long `waking` lasts before "깨우기 실패". A cold boot with a slow
-- POST or a pending Windows update easily takes over a minute and a half; the
-- last look at the end (`wake`) keeps a PC that just came up from being
-- called a failure.
wol.WAKE_TIMEOUT = 180
-- #149: while `waking`, a poll this long after the previous one ended,
-- whatever the `pollInterval` preference says - at 5 minutes the PC would
-- otherwise show as on up to 5 minutes late. Back to the plain interval as
-- soon as the PC answers (any poll or push ends the wake). An unreachable
-- poll emits nothing new (the event budget's dedupe), so the faster cadence
-- costs no events.
wol.WAKE_POLL_SECONDS = 5
wol.DEFAULT_BROADCAST = "255.255.255.255"

-- What this module needs from the poll layer above it (`once`), handed in by
-- poll.lua.
local wired = {}

--- `hooks.once(driver, device)`: poll.once.
function wol.use(hooks)
  wired = hooks or {}
end

--- Parse `AA:BB:CC:DD:EE:FF`, `AA-BB-CC-DD-EE-FF` or `aabbccddeeff` into the six
--- raw address bytes. Returns nil plus a message when the MAC is not valid.
function wol.parse_mac(mac)
  if type(mac) ~= "string" then
    return nil, "mac address is not a string"
  end
  local cleaned = mac:gsub("[%s:%-%.]", "")
  if not cleaned:match("^%x%x%x%x%x%x%x%x%x%x%x%x$") then
    return nil, "mac address is not valid: " .. mac
  end
  local bytes = {}
  for i = 1, 12, 2 do
    bytes[#bytes + 1] = string.char(tonumber(cleaned:sub(i, i + 1), 16))
  end
  return table.concat(bytes)
end

--- Build the 102-byte magic packet: six 0xFF bytes then the MAC 16 times.
function wol.magic_packet(mac)
  local bytes, err = wol.parse_mac(mac)
  if not bytes then
    return nil, err
  end
  return string.rep("\255", 6) .. string.rep(bytes, 16)
end

--- Send one magic packet to both WoL ports.
-- `deps.socket` may be injected in tests; by default this is cosock's socket so
-- the send does not block the driver thread.
-- Returns `true` when at least one datagram went out, else `false, err`.
function wol.send(mac, broadcast, deps)
  deps = deps or {}
  local packet, err = wol.magic_packet(mac)
  if not packet then
    return false, err
  end

  local socket = deps.socket
  if not socket then
    socket = require("cosock").socket
  end

  local sock, serr = socket.udp()
  if not sock then
    return false, serr or "could not open udp socket"
  end
  -- Broadcast has to be enabled explicitly; a directed subnet broadcast works
  -- too and is what `wolBroadcast` is for.
  pcall(function() sock:setoption("broadcast", true) end)
  pcall(function() sock:settimeout(1) end)

  local target = broadcast
  if target == nil or target == "" then
    target = wol.DEFAULT_BROADCAST
  end

  local sent, last_err = 0, nil
  for _, port in ipairs(wol.PORTS) do
    local ok, send_err = sock:sendto(packet, target, port)
    if ok then
      sent = sent + 1
    else
      last_err = send_err
    end
  end
  pcall(function() sock:close() end)

  if sent == 0 then
    return false, last_err or "no packet sent"
  end
  return true, nil
end

local function cancel_field(driver, device, name)
  local timer = fields.get(device, name)
  if timer then
    pcall(function() driver:cancel_timer(timer) end)
    fields.set(device, name, nil)
  end
end

--- End a wake under way: its timeout and its fast polls (called when a poll
--- or a push shows the PC up).
function wol.cancel_wake(driver, device)
  cancel_field(driver, device, fields.WAKE_TIMER)
  cancel_field(driver, device, fields.WAKE_POLL_TIMER)
end

local function waking(devices, device)
  local st = devices.get_state(device)
  return type(st) == "table" and st.power_state == state.WAKING
end

-- #149: the next fast poll of a wake, `WAKE_POLL_SECONDS` after the last one
-- ended (a delay, not a schedule: a poll that times out never overlaps the
-- next). It stops when the PC is no longer `waking` or the wake has ended.
local function poll_soon(driver, device, devices, once)
  local ok, timer = pcall(function()
    return driver:call_with_delay(wol.WAKE_POLL_SECONDS, function()
      fields.set(device, fields.WAKE_POLL_TIMER, nil)
      if not waking(devices, device) or not fields.get(device, fields.WAKE_TIMER) then
        return
      end
      pcall(once, driver, device)
      -- A poll that found the PC up has ended the wake (`cancel_wake`).
      if waking(devices, device) and fields.get(device, fields.WAKE_TIMER) then
        poll_soon(driver, device, devices, once)
      end
    end, "wol-poll")
  end)
  if ok and timer then
    fields.set(device, fields.WAKE_POLL_TIMER, timer)
  end
end

--- §6.2/§6.4: run the wake sequence for `device`, poll every
--- `WAKE_POLL_SECONDS` while it is waking and arm the `WAKE_TIMEOUT`.
--
-- The caller has already moved the device state to `waking`. At the timeout
-- the PC gets one last poll (#149: one that came up a moment ago is not a
-- failure); if it is still `waking` after that we apply `wake_timeout` (back
-- to the previous state) and put "wake failed" into `pcInfo.message`. `deps`
-- exists for tests: `deps.devices` replaces the device-layer glue,
-- `deps.socket` replaces cosock and `deps.once` the poll.
function wol.wake(driver, device, deps)
  deps = deps or {}
  local devices = deps.devices or DEVICES
  local prefs = device.preferences or {}
  local lang = prefs.language

  -- §6.4: the preference wins - it is the user's own value, typed into the
  -- driver, and nothing the PC reports may override it. Otherwise fall back to
  -- the MAC the last successful poll remembered: the adapter the service chose
  -- (#97, `status.wol.selected`), or the one the driver guessed from
  -- `status.wol.adapters` when the service is older than that.
  local mac = prefs.macAddress
  if mac == nil or mac == "" then
    mac = fields.get(device, fields.WOL_MAC)
  end
  if mac == nil or mac == "" then
    devices.emit_message(device, i18n.t(lang, "wol_no_mac"))
    return false, "no mac"
  end
  if not wol.parse_mac(mac) then
    devices.emit_message(device, i18n.t(lang, "wol_bad_mac", mac))
    return false, "bad mac"
  end

  -- §6.4: a PC whose adapter has WoL turned off is still sent the packet, but
  -- the last poll already knew it would probably not work, so say so now
  -- rather than at the next poll.
  if fields.get(device, fields.WOL_READY) == false then
    -- #97: naming the chosen adapter, when the last poll learned one.
    local adapter = fields.get(device, fields.WOL_ADAPTER)
    if type(adapter) == "string" and adapter ~= "" then
      devices.emit_message(device, i18n.t(lang, "wol_not_ready_on", adapter))
    else
      devices.emit_message(device, i18n.t(lang, "wol_not_ready"))
    end
  end

  local broadcast = prefs.wolBroadcast
  local function fire()
    local ok, err = wol.send(mac, broadcast, deps)
    if not ok then
      devices.emit_message(device, tostring(err))
    end
    return ok
  end

  for _, delay in ipairs(wol.RETRY_DELAYS) do
    if delay == 0 then
      fire()
    else
      driver:call_with_delay(delay, fire, "wol-send-" .. delay)
    end
  end

  wol.cancel_wake(driver, device)
  local once = deps.once or wired.once
  local timer = driver:call_with_delay(wol.WAKE_TIMEOUT, function()
    fields.set(device, fields.WAKE_TIMER, nil)
    cancel_field(driver, device, fields.WAKE_POLL_TIMER)
    if once then
      pcall(once, driver, device)
    end
    if not waking(devices, device) then
      -- The last look found the PC up (or something else ended the wake).
      return
    end
    local st = devices.get_state(device)
    local nxt = devices.transition(st, "wake_timeout")
    devices.set_state(device, nxt)
    devices.emit_power(device, nxt)
    -- #93: the wake is over, however it ended, so the command list stops saying
    -- "켜는 중…" and opens again.
    devices.ensure_action(device)
    devices.emit_message(device, i18n.t(lang, "wake_failed"))
  end, "wol-timeout")
  fields.set(device, fields.WAKE_TIMER, timer)
  if once then
    poll_soon(driver, device, devices, once)
  end

  return true, nil
end

return wol
