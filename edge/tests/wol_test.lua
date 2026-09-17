local h = require "helpers"
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

function T.test_retry_schedule_matches_the_design()
  -- §6.3: immediately, at 2s and at 5s; give up after 90s.
  h.assert_deep_equal(wol.RETRY_DELAYS, { 0, 2, 5 })
  h.assert_equal(wol.WAKE_TIMEOUT, 90)
  h.assert_deep_equal(wol.PORTS, { 7, 9 })
end

return T
