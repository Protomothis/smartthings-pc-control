-- Minimal cosock stub.
--
-- UDP sockets record what they were asked to send so wol tests can inspect the
-- datagrams. `asyncify` raises on purpose: HTTP in tests must go through an
-- injected `deps.http`, never through a real socket.

local cosock = { socket = {}, sent = {} }

function cosock.reset()
  cosock.sent = {}
end

function cosock.socket.udp()
  local sock = { options = {}, timeout = nil, sent = {}, closed = false }

  function sock:setoption(name, value)
    self.options[name] = value
    return 1
  end

  function sock:settimeout(seconds)
    self.timeout = seconds
    return 1
  end

  function sock:sendto(data, ip, port)
    local record = { data = data, ip = ip, port = port }
    self.sent[#self.sent + 1] = record
    cosock.sent[#cosock.sent + 1] = record
    return 1
  end

  function sock:close()
    self.closed = true
    return 1
  end

  return sock
end

function cosock.asyncify(name)
  error("cosock.asyncify(" .. tostring(name) .. ") is not available in tests; inject deps.http", 0)
end

function cosock.spawn(fn, name) -- luacheck: ignore name
  return fn()
end

return cosock
