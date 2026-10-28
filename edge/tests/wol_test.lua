local h = require "helpers"
local fields = require "device.fields"
local state = require "state"
local wol = require "wol"

local T = {}

local MAC_BYTES = "\170\187\204\221\238\255" -- AA BB CC DD EE FF

function T.test_parse_mac_accepts_colon_dash_and_bare()
  h.assert_equal(wol.parse_mac("AA:BB:CC:DD:EE:FF"), MAC_BYTES)
  h.assert_equal(wol.parse_mac("AA-BB-CC-DD-EE-FF"), MAC_BYTES)
  h.assert_equal(wol.parse_mac("aabbccddeeff"), MAC_BYTES)
  h.assert_equal(wol.parse_mac("aa:bb:cc:dd:ee:ff"), MAC_BYTES)
  -- Cisco-style dotted notation and stray spaces are tolerated too.
  h.assert_equal(wol.parse_mac("aabb.ccdd.eeff"), MAC_BYTES)
  h.assert_equal(wol.parse_mac(" AA:BB:CC:DD:EE:FF "), MAC_BYTES)
end

function T.test_parse_mac_rejects_invalid_input()
  local cases = {
    "AA:BB:CC:DD:EE",          -- too short
    "AA:BB:CC:DD:EE:FF:00",    -- too long
    "GG:BB:CC:DD:EE:FF",       -- not hex
    "",
    "not a mac",
  }
  for _, bad in ipairs(cases) do
    local bytes, err = wol.parse_mac(bad)
    h.assert_nil(bytes, "should reject " .. bad)
    h.assert_true(type(err) == "string", "should explain why " .. bad .. " is invalid")
  end
  local bytes = wol.parse_mac(nil)
  h.assert_nil(bytes, "nil mac")
end

function T.test_magic_packet_bytes()
  local packet = wol.magic_packet("AA:BB:CC:DD:EE:FF")
  -- 6 sync bytes + 16 copies of the 6-byte address
  h.assert_equal(#packet, 102, "packet length")
  h.assert_equal(packet:sub(1, 6), string.rep("\255", 6), "sync stream")
  h.assert_equal(packet:sub(7, 12), MAC_BYTES, "first address copy")
  h.assert_equal(packet:sub(97, 102), MAC_BYTES, "last address copy")
  for i = 0, 15 do
    h.assert_equal(packet:sub(7 + i * 6, 12 + i * 6), MAC_BYTES, "copy " .. i)
  end
end

function T.test_magic_packet_is_format_independent()
  h.assert_equal(wol.magic_packet("aabbccddeeff"), wol.magic_packet("AA:BB:CC:DD:EE:FF"))
end

function T.test_magic_packet_rejects_invalid_mac()
  local packet, err = wol.magic_packet("nope")
  h.assert_nil(packet)
  h.assert_contains(err, "not valid")
end

-- A socket factory that records datagrams instead of sending them.
local function recording_socket()
  local sockets = {}
  local factory = {
    udp = function()
      local sock = { options = {}, sent = {}, closed = false }
      function sock:setoption(name, value) self.options[name] = value return 1 end
      function sock:settimeout(seconds) self.timeout = seconds return 1 end
      function sock:sendto(data, ip, port)
        self.sent[#self.sent + 1] = { data = data, ip = ip, port = port }
        return 1
      end
      function sock:close() self.closed = true return 1 end
      sockets[#sockets + 1] = sock
      return sock
    end,
  }
  return factory, sockets
end

function T.test_send_hits_both_wol_ports()
  local factory, sockets = recording_socket()
  local ok, err = wol.send("AA:BB:CC:DD:EE:FF", "192.168.1.255", { socket = factory })
  h.assert_true(ok, "send should succeed")
  h.assert_nil(err)
  h.assert_equal(#sockets, 1, "one socket")
  local sock = sockets[1]
  h.assert_equal(#sock.sent, 2, "one datagram per port")
  h.assert_equal(sock.sent[1].port, 7)
  h.assert_equal(sock.sent[2].port, 9)
  h.assert_equal(sock.sent[1].ip, "192.168.1.255")
  h.assert_equal(sock.sent[1].data, wol.magic_packet("AA:BB:CC:DD:EE:FF"))
  h.assert_true(sock.options.broadcast, "broadcast must be enabled")
  h.assert_true(sock.closed, "socket must be closed")
end

function T.test_send_defaults_to_global_broadcast()
  local factory, sockets = recording_socket()
  wol.send("AA:BB:CC:DD:EE:FF", nil, { socket = factory })
  h.assert_equal(sockets[1].sent[1].ip, "255.255.255.255")
  wol.send("AA:BB:CC:DD:EE:FF", "", { socket = factory })
  h.assert_equal(sockets[2].sent[1].ip, "255.255.255.255")
end

function T.test_send_refuses_an_invalid_mac()
  local factory, sockets = recording_socket()
  local ok, err = wol.send("zz", "255.255.255.255", { socket = factory })
  h.assert_false(ok)
  h.assert_contains(err, "not valid")
  h.assert_equal(#sockets, 0, "no socket should be opened")
end

--------------------------------------------------------------------------------
-- wake: which MAC, and what it says while sending (§6.4)
--------------------------------------------------------------------------------

-- A driver stand-in. `call_with_delay` records instead of running, so the two
-- retries and the 90s timeout do not need a clock.
local function fake_driver()
  local driver = { delays = {} }
  function driver:call_with_delay(seconds, fn, name)
    self.delays[#self.delays + 1] = { seconds = seconds, fn = fn, name = name }
    return { name = name }
  end
  function driver:cancel_timer(timer)
    self.cancelled = timer
  end
  return driver
end

-- The device-layer glue `wol.wake` reaches for (poll.lua in the driver).
local function fake_devices()
  local devices = { messages = {} }
  function devices.emit_message(_, message)
    devices.messages[#devices.messages + 1] = message
  end
  function devices.get_state() return "off" end
  function devices.transition() return "off" end
  function devices.set_state() end
  function devices.emit_power() end
  function devices.ensure_action() end
  return devices
end

local function wake(device)
  local factory, sockets = recording_socket()
  local devices = fake_devices()
  local ok, err = wol.wake(fake_driver(), device, { devices = devices, socket = factory })
  return ok, err, sockets, devices.messages
end

function T.test_wake_sends_to_the_mac_address_preference_above_all()
  -- §6.4/#97: the preference is the user's own value, typed into the driver.
  -- A MAC the service chose and the last poll remembered does not displace it.
  local device = h.fake_device({ macAddress = "AA:BB:CC:DD:EE:FF" })
  device:set_field("wol_mac", "11:22:33:44:55:66")
  local ok, _, sockets = wake(device)
  h.assert_true(ok)
  h.assert_equal(sockets[1].sent[1].data, wol.magic_packet("AA:BB:CC:DD:EE:FF"))
end

function T.test_wake_falls_back_to_the_remembered_mac()
  -- #97: what the last poll learned - `wol.selected.mac` from a service that
  -- chooses, the driver's own guess from one that does not.
  local device = h.fake_device({ macAddress = "" })
  device:set_field("wol_mac", "11:22:33:44:55:66")
  local ok, _, sockets = wake(device)
  h.assert_true(ok)
  h.assert_equal(sockets[1].sent[1].data, wol.magic_packet("11:22:33:44:55:66"))

  -- Nothing to send to: say so rather than sending into the void.
  local blank = h.fake_device({})
  local sent, err, no_sockets, messages = wake(blank)
  h.assert_false(sent)
  h.assert_equal(err, "no mac")
  h.assert_equal(#no_sockets, 0)
  h.assert_contains(messages[1], "MAC")
end

function T.test_wake_warns_about_the_adapter_it_knows_has_wol_off()
  -- §6.4: the packet still goes out, but the last poll already knew it would
  -- probably not land. #97: on the adapter the service chose, by name.
  local device = h.fake_device({ language = "ko" })
  device:set_field("wol_mac", "AA:BB:CC:DD:EE:FF")
  device:set_field("wol_ready", false)
  device:set_field("wol_adapter", "이더넷")
  local ok, _, sockets, messages = wake(device)
  h.assert_true(ok, "the packet goes out anyway")
  h.assert_equal(#sockets, 1)
  h.assert_equal(messages[1], "이더넷 어댑터에 WoL이 꺼져 있습니다 · SmartThings 탭 확인")

  -- A service too old to name one still gets the general sentence.
  device:set_field("wol_adapter", nil)
  local _, _, _, plain = wake(device)
  h.assert_equal(plain[1], "PC의 어댑터에 WoL이 꺼져 있습니다 · SmartThings 탭 확인")

  -- And a healthy adapter says nothing at all.
  device:set_field("wol_ready", true)
  local _, _, _, quiet = wake(device)
  h.assert_equal(#quiet, 0)
end

function T.test_retry_schedule_matches_the_design()
  -- §6.4: immediately, at 2s and at 5s.
  h.assert_deep_equal(wol.RETRY_DELAYS, { 0, 2, 5 })
  h.assert_deep_equal(wol.PORTS, { 7, 9 })
end

--------------------------------------------------------------------------------
-- #149: fast polls while waking, and one last look before "깨우기 실패"
--------------------------------------------------------------------------------

-- A waking PC with the real device glue, a driver whose timers a test fires
-- by name (newest first), and a poll stand-in: `pc.up` decides whether it
-- finds the PC.
local function waking_pc()
  local factory = recording_socket()
  local device = h.fake_device({ macAddress = "AA:BB:CC:DD:EE:FF", language = "ko" })
  fields.set_state(device, state.transition(state.new(state.OFF), "switch_on"))
  local driver = { delays = {} }
  function driver:call_with_delay(seconds, fn, name)
    local timer = { seconds = seconds, fn = fn, name = name }
    self.delays[#self.delays + 1] = timer
    return timer
  end
  function driver:cancel_timer(timer)
    timer.done = true
  end
  function driver:fire(name)
    for i = #self.delays, 1, -1 do
      local timer = self.delays[i]
      if timer.name == name and not timer.done then
        timer.done = true
        timer.fn()
        return true
      end
    end
    return false
  end
  local pc = { up = false, polls = 0, driver = driver, device = device }
  pc.deps = {
    socket = factory,
    -- What poll.once does with the answer that matters here.
    once = function(d, dev)
      pc.polls = pc.polls + 1
      if pc.up then
        fields.set_state(dev, state.transition(fields.state(dev), "status_ok"))
        wol.cancel_wake(d, dev)
      else
        fields.set_state(dev, state.transition(fields.state(dev), "unreachable"))
      end
    end,
  }
  return pc
end

local function messages(device)
  local out = {}
  for _, e in ipairs(h.emitted(device)) do
    if e.attr == "message" then
      out[#out + 1] = e.value
    end
  end
  return table.concat(out, " | ")
end

function T.test_waking_polls_every_few_seconds_until_the_pc_answers()
  local pc = waking_pc()
  h.assert_true(wol.wake(pc.driver, pc.device, pc.deps))
  for _ = 1, 3 do
    h.assert_true(pc.driver:fire("wol-poll"), "a fast poll is due")
  end
  h.assert_equal(pc.polls, 3)
  h.assert_equal(fields.state(pc.device).power_state, state.WAKING)
  pc.up = true
  h.assert_true(pc.driver:fire("wol-poll"))
  h.assert_equal(fields.state(pc.device).power_state, state.ON)
  h.assert_false(pc.driver:fire("wol-poll"), "no fast poll once the PC answered")
  h.assert_false(pc.driver:fire("wol-timeout"), "the timeout went with the wake")
  for _, timer in ipairs(pc.driver.delays) do
    if timer.name == "wol-poll" then
      h.assert_equal(timer.seconds, wol.WAKE_POLL_SECONDS)
    end
  end
end

function T.test_the_timeout_takes_one_last_look()
  -- The PC came up between the last fast poll and the timeout.
  local pc = waking_pc()
  wol.wake(pc.driver, pc.device, pc.deps)
  pc.up = true
  h.assert_true(pc.driver:fire("wol-timeout"))
  h.assert_equal(pc.polls, 1, "one last poll")
  h.assert_equal(fields.state(pc.device).power_state, state.ON)
  h.assert_nil(messages(pc.device):find("깨우기 실패", 1, true), "not a failure")
  h.assert_false(pc.driver:fire("wol-poll"), "no fast poll after the timeout")

  -- Still nothing at the end: back to off, and it says so.
  local silent = waking_pc()
  wol.wake(silent.driver, silent.device, silent.deps)
  h.assert_true(silent.driver:fire("wol-timeout"))
  h.assert_equal(silent.polls, 1)
  h.assert_equal(fields.state(silent.device).power_state, state.OFF)
  h.assert_contains(messages(silent.device), "깨우기 실패")
  h.assert_false(silent.driver:fire("wol-poll"), "the timeout ended the fast polls")
end

return T
