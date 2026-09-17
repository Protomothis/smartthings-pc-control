-- HTTP client for the service's `/st/v1` protocol (design doc §4).
--
-- Every call returns `(ok, body, err_kind)`:
--   ok = true   -> body is the decoded JSON table, err_kind is nil
--   ok = false  -> err_kind is one of "unauthorized", "forbidden",
--                  "unreachable", "incompatible", "badrequest",
--                  "ratelimited"; body may still carry the decoded response
--                  (the service answers errors with `{"error": "..."}`, and a
--                  protocol mismatch needs `body.protocol` to tell "service
--                  too old" from "driver too old").
--
-- No module-level mutable state: everything comes from `device.preferences`, and
-- `deps` lets tests inject a fake http/json/ltn12 instead of cosock.

local VERSION = require "version"

local client = {}

client.PROTOCOL = 1
client.TIMEOUT = 5
client.DEFAULT_PORT = 5001
client.BASE_PATH = "/st/v1"
-- The service records this as `hubLastSeen` and shows it in the GUI (§4.2).
client.USER_AGENT = "smartthings-pc-control-edge/" .. VERSION

--- `http://<ip>:<port>/st/v1`, or nil when no IP is configured yet.
function client.base_url(prefs)
  prefs = prefs or {}
  local ip = prefs.ipAddress
  if type(ip) ~= "string" or ip == "" then
    return nil
  end
  local port = math.floor(tonumber(prefs.port) or client.DEFAULT_PORT)
  return string.format("http://%s:%d%s", ip, port, client.BASE_PATH)
end

--- Request headers. §4.1/§8: the secret travels in `X-PC-Secret`, never in the
--- URL, and is omitted entirely when the service has no secret set.
function client.headers(prefs, body_length)
  prefs = prefs or {}
  local headers = {
    ["User-Agent"] = client.USER_AGENT,
    ["Accept"] = "application/json",
  }
  if type(prefs.secret) == "string" and prefs.secret ~= "" then
    headers["X-PC-Secret"] = prefs.secret
  end
  if body_length then
    headers["Content-Type"] = "application/json"
    headers["Content-Length"] = tostring(body_length)
  end
  return headers
end

-- Resolve the request function. cosock's asyncify keeps the blocking
-- socket.http from stalling the driver thread.
local function request_fn(deps)
  local http = deps.http
  if type(http) == "function" then
    return http
  end
  if type(http) == "table" and type(http.request) == "function" then
    return http.request
  end
  local cosock = require "cosock"
  local mod = cosock.asyncify "socket.http"
  mod.TIMEOUT = client.TIMEOUT
  return mod.request
end

--- Map an HTTP status code to an err_kind, or nil when the code is a success.
function client.classify(code)
  if type(code) ~= "number" then
    -- socket.http returns a string here (connection refused, timeout, ...).
    return "unreachable"
  end
  if code >= 200 and code < 300 then
    return nil
  end
  if code == 401 then
    return "unauthorized"
  end
  if code == 403 then
    -- The hub is not in `smartthings.allowed_hubs` (§4.1). It shows up as the
    -- same `connection = unauthorized` as a wrong secret, but the fix is a
    -- different one, so the kind stays separate for the message.
    return "forbidden"
  end
  if code == 400 then
    return "badrequest"
  end
  if code == 404 then
    -- A service older than v1.1.0 has no /st/v1 routes at all.
    return "incompatible"
  end
  if code == 429 then
    -- Rate limited (§8): the service is up, authenticated and healthy, it just
    -- refused this one request. Nothing about the PC changed, so the caller
    -- keeps the last state instead of painting an error.
    return "ratelimited"
  end
  return "unreachable"
end

--- Perform one `/st/v1` request.
-- @param opts table: `method`, `path`, `body` (table, encoded as JSON),
--   `expect_protocol` (true for endpoints that carry `"protocol": 1`)
function client.request(device, opts, deps)
  deps = deps or {}
  opts = opts or {}
  local prefs = (device and device.preferences) or {}

  local base = client.base_url(prefs)
  if not base then
    return false, nil, "unreachable"
  end

  local json = deps.json or require "st.json"
  local ltn12 = deps.ltn12 or require "ltn12"

  local payload
  if opts.body ~= nil then
    local ok, encoded = pcall(json.encode, opts.body)
    if not ok then
      return false, nil, "badrequest"
    end
    payload = encoded
  end

  local chunks = {}
  local req = {
    url = base .. (opts.path or ""),
    method = opts.method or "GET",
    headers = client.headers(prefs, payload and #payload or nil),
    sink = ltn12.sink.table(chunks),
  }
  if payload then
    req.source = ltn12.source.string(payload)
  end

  local called, result, code = pcall(request_fn(deps), req)
  if not called then
    -- A raised socket error is still just an unreachable PC.
    return false, nil, "unreachable"
  end
  if not result then
    return false, nil, "unreachable"
  end

  local raw = table.concat(chunks)
  local body
  if raw ~= "" then
    local decoded_ok, decoded = pcall(json.decode, raw)
    if decoded_ok and type(decoded) == "table" then
      body = decoded
    end
  else
    body = {}
  end

  local kind = client.classify(code)
  if kind then
    -- §4.1: an error body is `{"error": "..."}`. It comes back so the caller
    -- can put the service's own words in `pcStatus.message` (an unknown
    -- command says which one), and so a 400 from a newer service is readable.
    return false, body, kind
  end

  if body == nil then
    -- 2xx that is not the JSON we expect means we are not talking to a
    -- compatible service (a captive portal, another app on the port, ...).
    return false, nil, "incompatible"
  end

  if body.protocol ~= nil and body.protocol ~= client.PROTOCOL then
    return false, body, "incompatible"
  end
  if opts.expect_protocol and body.protocol == nil then
    return false, body, "incompatible"
  end

  return true, body, nil
end

--- `GET /st/v1/status` (§4.2).
function client.get_status(device, deps)
  return client.request(device, { method = "GET", path = "/status", expect_protocol = true }, deps)
end

--- `POST /st/v1/command` (§4.3). `minutes > 0` schedules instead of executing.
function client.command(device, cmd, mode, minutes, deps)
  return client.request(device, {
    method = "POST",
    path = "/command",
    body = {
      command = cmd,
      mode = mode or "default",
      minutes = math.floor(tonumber(minutes) or 0),
    },
  }, deps)
end

--- `DELETE /st/v1/schedule` (§4.4).
function client.cancel(device, deps)
  return client.request(device, { method = "DELETE", path = "/schedule" }, deps)
end

return client
