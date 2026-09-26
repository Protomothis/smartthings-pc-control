-- Wake-on-LAN: magic packet building and the wake sequence of design doc §6.4
-- (send immediately, at 2s and at 5s, to ports 7 and 9; give up after 90s).
--
-- `magic_packet` and `parse_mac` are pure and unit-tested. `send`/`wake` take
-- their socket and device-layer dependencies lazily so loading this module in a
-- test never pulls in cosock or st.*.

local i18n = require "i18n"

local wol = {}

wol.PORTS = { 7, 9 }
-- §6.4: three attempts. A PC that missed the first packet because the switch
-- was still learning the port usually catches the second or third.
wol.RETRY_DELAYS = { 0, 2, 5 }
wol.WAKE_TIMEOUT = 90
wol.DEFAULT_BROADCAST = "255.255.255.255"

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

--- Cancel a pending wake timeout (called when a poll succeeds while waking).
function wol.cancel_wake(driver, device)
  local timer = device:get_field("wake_timer")
  if timer then
    pcall(function() driver:cancel_timer(timer) end)
    device:set_field("wake_timer", nil)
  end
end

--- §6.2/§6.4: run the wake sequence for `device` and arm the 90s timeout.
--
-- The caller has already moved the device state to `waking`; on timeout we apply
-- `wake_timeout` (back to the previous state) and put "wake failed" into
-- `pcInfo.message`. `deps` exists for tests: `deps.devices` replaces the
-- device-layer glue and `deps.socket` replaces cosock.
function wol.wake(driver, device, deps)
  deps = deps or {}
  -- Lazy so that requiring wol in a test does not drag in the driver layer.
  local devices = deps.devices or require "poll"
  local prefs = device.preferences or {}
  local lang = prefs.language

  -- §6.4: the preference wins; otherwise fall back to the WoL-capable adapter
  -- MAC that the last successful poll learned from `status.wol.adapters`.
  local mac = prefs.macAddress
  if mac == nil or mac == "" then
    mac = device:get_field("wol_mac")
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
  if device:get_field("wol_ready") == false then
    devices.emit_message(device, i18n.t(lang, "wol_not_ready"))
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
  local timer = driver:call_with_delay(wol.WAKE_TIMEOUT, function()
    device:set_field("wake_timer", nil)
    local st = devices.get_state(device)
    local nxt = devices.transition(st, "wake_timeout")
    devices.set_state(device, nxt)
    devices.emit_power(device, nxt)
    -- #93: the wake is over, however it ended, so the command list stops saying
    -- "켜는 중…" and opens again.
    devices.ensure_action(device)
    devices.emit_message(device, i18n.t(lang, "wake_failed"))
  end, "wol-timeout")
  device:set_field("wake_timer", timer)

  return true, nil
end

return wol
