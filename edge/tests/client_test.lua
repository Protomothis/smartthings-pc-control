local h = require "helpers"
local client = require "client"
local json = require "st.json"
-- The other half of error classification (err_kind -> connection/message,
-- §3.1) lives in poll.lua, so the two are asserted together.
local caps = require "caps"
local poll = require "poll"
local state = require "state"

local T = {}

local PREFS = {
  ipAddress = "192.168.1.20",
  port = 5001,
  secret = "s3cret",
}

local function device(overrides)
  local prefs = {}
  for k, v in pairs(PREFS) do
    prefs[k] = v
  end
  for k, v in pairs(overrides or {}) do
    prefs[k] = v
  end
  return h.fake_device(prefs)
end

--- A fake `socket.http.request` that always answers with `code` and `body`.
--- The captured request table is returned alongside so tests can inspect it.
local function fake_http(code, body)
  local captured = {}
  local fn = function(req)
    captured.url = req.url
    captured.method = req.method
    captured.headers = req.headers
    if req.source then
      local parts = {}
      while true do
        local chunk = req.source()
        if not chunk then
          break
        end
        parts[#parts + 1] = chunk
      end
      captured.body = table.concat(parts)
    end
    if body then
      req.sink(body)
    end
    return 1, code, {}, "HTTP/1.1 " .. tostring(code)
  end
  return fn, captured
end

--- A fake that fails the way luasocket does: nil plus a message.
local function broken_http(message)
  return function()
    return nil, message or "connection refused"
  end
end

local function status_body(overrides)
  local body = { protocol = 1, service_version = "v1.1.0", power = "on", secret_set = true }
  for k, v in pairs(overrides or {}) do
    body[k] = v
  end
  return json.encode(body)
end

--------------------------------------------------------------------------------
-- url and headers
--------------------------------------------------------------------------------

function T.test_base_url()
  h.assert_equal(client.base_url(PREFS), "http://192.168.1.20:5001/st/v1")
  h.assert_equal(client.base_url({ ipAddress = "10.0.0.5" }), "http://10.0.0.5:5001/st/v1")
  h.assert_equal(client.base_url({ ipAddress = "10.0.0.5", port = "8080" }),
    "http://10.0.0.5:8080/st/v1")
end

function T.test_base_url_is_nil_without_an_ip()
  h.assert_nil(client.base_url({}))
  h.assert_nil(client.base_url({ ipAddress = "" }))
  h.assert_nil(client.base_url(nil))
end

function T.test_headers_carry_the_secret_and_user_agent()
  local headers = client.headers(PREFS)
  -- §3.1/§8: header auth, never in the URL.
  h.assert_equal(headers["X-PC-Secret"], "s3cret")
  h.assert_contains(headers["User-Agent"], "smartthings-pc-control-edge/")
end

function T.test_headers_omit_an_empty_secret()
  h.assert_nil(client.headers({ ipAddress = "1.2.3.4" })["X-PC-Secret"])
  h.assert_nil(client.headers({ secret = "" })["X-PC-Secret"])
end

function T.test_headers_describe_a_json_body()
  local headers = client.headers(PREFS, 42)
  h.assert_equal(headers["Content-Type"], "application/json")
  h.assert_equal(headers["Content-Length"], "42")
end

--------------------------------------------------------------------------------
-- requests
--------------------------------------------------------------------------------

function T.test_get_status_returns_the_decoded_body()
  local http, captured = fake_http(200, status_body())
  local ok, body, err = client.get_status(device(), { http = http })
  h.assert_true(ok)
  h.assert_nil(err)
  h.assert_equal(body.service_version, "v1.1.0")
  h.assert_equal(captured.url, "http://192.168.1.20:5001/st/v1/status")
  h.assert_equal(captured.method, "GET")
end

function T.test_command_posts_the_documented_body()
  local http, captured = fake_http(200, '{"accepted":true,"executed":false}')
  local ok, body = client.command(device(), "shutdown", "immediate", 15, { http = http })
  h.assert_true(ok)
  h.assert_equal(body.accepted, true)
  h.assert_equal(captured.url, "http://192.168.1.20:5001/st/v1/command")
  h.assert_equal(captured.method, "POST")
  h.assert_deep_equal(json.decode(captured.body),
    { command = "shutdown", mode = "immediate", minutes = 15 })
  h.assert_equal(captured.headers["Content-Type"], "application/json")
end

function T.test_command_defaults_mode_and_minutes()
  local http, captured = fake_http(200, '{"accepted":true}')
  client.command(device(), "lock", nil, nil, { http = http })
  h.assert_deep_equal(json.decode(captured.body),
    { command = "lock", mode = "default", minutes = 0 })
end

function T.test_cancel_uses_delete()
  local http, captured = fake_http(200, '{"cancelled":true}')
  local ok, body = client.cancel(device(), { http = http })
  h.assert_true(ok)
  h.assert_equal(body.cancelled, true)
  h.assert_equal(captured.method, "DELETE")
  h.assert_equal(captured.url, "http://192.168.1.20:5001/st/v1/schedule")
end

function T.test_a_body_without_protocol_is_fine_outside_status()
  -- §3.3/§3.4 responses have no `protocol` field, and must not be rejected.
  local http = fake_http(200, '{"cancelled":false}')
  local ok, _, err = client.cancel(device(), { http = http })
  h.assert_true(ok)
  h.assert_nil(err)
end

--------------------------------------------------------------------------------
-- error classification (§3.1)
--------------------------------------------------------------------------------

function T.test_401_is_unauthorized()
  local http = fake_http(401, '{"error":"unauthorized"}')
  local ok, body, err = client.get_status(device(), { http = http })
  h.assert_false(ok)
  h.assert_equal(err, "unauthorized")
  -- §3.1: the service's error body comes back with the failure.
  h.assert_equal(body.error, "unauthorized")
end

function T.test_403_is_forbidden()
  -- Hub missing from smartthings.allowed_hubs (§3.1). It shows up as
  -- `connection = unauthorized`, but with its own message, so the kind that
  -- reaches poll.lua has to stay distinguishable from a wrong secret.
  local http = fake_http(403, '{"error":"hub not allowed"}')
  local _, body, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "forbidden")
  h.assert_equal(body.error, "hub not allowed")
  h.assert_equal(poll.connection_for("forbidden"), "unauthorized")
  h.assert_contains(poll.message_for("forbidden", nil, "en"), "allow-list")
  h.assert_contains(poll.message_for("forbidden", nil, "ko"), "허용 목록")
end

function T.test_a_connection_failure_is_unreachable()
  local ok, _, err = client.get_status(device(), { http = broken_http("connection refused") })
  h.assert_false(ok)
  h.assert_equal(err, "unreachable")
end

function T.test_a_timeout_is_unreachable()
  local _, _, err = client.get_status(device(), { http = broken_http("timeout") })
  h.assert_equal(err, "unreachable")
end

function T.test_a_raised_socket_error_is_unreachable()
  local http = function() error("socket exploded", 0) end
  local _, _, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "unreachable")
end

function T.test_a_missing_ip_is_unreachable()
  local http = fake_http(200, status_body())
  local _, _, err = client.get_status(device({ ipAddress = "" }), { http = http })
  h.assert_equal(err, "unreachable")
end

function T.test_a_protocol_mismatch_is_incompatible()
  local http = fake_http(200, status_body({ protocol = 2 }))
  local ok, body, err = client.get_status(device(), { http = http })
  h.assert_false(ok)
  h.assert_equal(err, "incompatible")
  -- The body comes back so the caller can say "driver too old" (§3.1).
  h.assert_equal(body.protocol, 2)
  h.assert_equal(poll.connection_for(err), "incompatible")
  h.assert_equal(poll.message_for(err, body, "en"), "Driver update required")
  h.assert_equal(poll.message_for(err, body, "ko"), "드라이버 업데이트 필요")
end

function T.test_a_status_without_protocol_is_incompatible()
  local http = fake_http(200, '{"power":"on"}')
  local _, body, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "incompatible")
  -- No `protocol` at all means the service predates it: it is the old side.
  h.assert_contains(poll.message_for(err, body, "en"), "Requires service v1.1.0")
end

function T.test_an_older_protocol_says_the_service_is_too_old()
  local http = fake_http(200, status_body({ protocol = 0 }))
  local _, body, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "incompatible")
  h.assert_contains(poll.message_for(err, body, "ko"), "서비스 v1.1.0 이상 필요")
end

function T.test_404_is_incompatible()
  -- A service older than v1.1.0 has no /st/v1 routes.
  local http = fake_http(404, "not found")
  local _, _, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "incompatible")
end

function T.test_a_non_json_2xx_body_is_incompatible()
  local http = fake_http(200, "<html>some other app on this port</html>")
  local _, _, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "incompatible")
end

function T.test_400_is_badrequest()
  local http = fake_http(400, '{"error":"unknown command"}')
  local _, body, err = client.command(device(), "nonsense", "default", 0, { http = http })
  h.assert_equal(err, "badrequest")
  h.assert_equal(poll.connection_for(err), "incompatible")
  -- The service says which command it refused; quote it (§3.3).
  h.assert_contains(poll.message_for(err, body, "en"), "unknown command")
end

function T.test_429_keeps_the_last_state()
  -- Rate limited (§3.1): reachable, authenticated and healthy, just refused this
  -- one request. Nothing about the PC changed, so no attribute is repainted.
  local http = fake_http(429, '{"error":"rate limited"}')
  local ok, _, err = client.get_status(device(), { http = http })
  h.assert_false(ok)
  h.assert_equal(err, "ratelimited")
  h.assert_nil(poll.connection_for(err),
    "a rate-limited poll must not change `connection`")
  h.assert_contains(poll.message_for(err, nil, "en"), "Too many requests")
end

function T.test_a_rate_limited_poll_leaves_the_device_alone()
  local d = device()
  poll.set_state(d, state.new(state.ON))
  local ok, kind = poll.once(nil, d, { deps = { http = fake_http(429, '{"error":"rate limited"}') } })
  h.assert_false(ok)
  h.assert_equal(kind, "ratelimited")
  h.assert_equal(#d.emitted, 0, "a 429 must not repaint any attribute")
  h.assert_nil(d.health, "a 429 must not touch health")
  h.assert_equal(poll.get_state(d).power_state, state.ON)
end

function T.test_an_unreachable_poll_does_report()
  -- The contrast to the test above: a real failure is shown.
  local d = device()
  poll.set_state(d, state.new(state.ON))
  local ok, kind = poll.once(nil, d, { deps = { http = broken_http("connection refused") } })
  h.assert_false(ok)
  h.assert_equal(kind, "unreachable")
  -- health stays online: an offline device cannot be switched on (WoL) in the app
  h.assert_equal(d.health, "online")
  h.assert_equal(h.event_value(h.emitted(d), caps.STATUS, "connection"), "unreachable")
end

function T.test_an_unreachable_poll_keeps_the_last_service_version()
  -- #92: a PC that is off has not changed its version. Before this, the row
  -- fell back to "v? · 드라이버 1.0" every night, and the one number the row
  -- exists for disappeared exactly when nothing else on screen was moving.
  local d = device({ language = "ko" })
  h.assert_nil(poll.last_service_version(d), "nothing is remembered before the first poll")

  h.assert_true(poll.once(nil, d, { deps = { http = fake_http(200, status_body()) } }))
  h.assert_equal(d:get_field(poll.SERVICE_VERSION_FIELD), "v1.1.0",
    "a successful poll persists the service version")

  d.emitted = {}
  local ok = poll.once(nil, d, { deps = { http = broken_http("connection refused") } })
  h.assert_false(ok)
  local emitted = h.emitted(d)
  h.assert_equal(h.event_value(emitted, caps.STATUS, "connection"), "unreachable")
  local kept = state.versions("v1.1.0", "ko")
  h.assert_equal(h.event_value(emitted, caps.VERSION, "versions"), kept)
  h.assert_equal(h.event_value(emitted, caps.STATUS, "versions"), kept)
  h.assert_contains(kept, "v1.1.0")
  h.assert_equal(kept:find("업데이트", 1, true), nil,
    "the update half is never remembered - only a live answer can offer one")
end

function T.test_5xx_is_unreachable()
  local http = fake_http(500, "boom")
  local _, _, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "unreachable")
end

function T.test_classify_table()
  h.assert_nil(client.classify(200))
  h.assert_nil(client.classify(204))
  h.assert_equal(client.classify(401), "unauthorized")
  h.assert_equal(client.classify(403), "forbidden")
  h.assert_equal(client.classify(400), "badrequest")
  h.assert_equal(client.classify(404), "incompatible")
  h.assert_equal(client.classify(429), "ratelimited")
  h.assert_equal(client.classify(502), "unreachable")
  h.assert_equal(client.classify("connection refused"), "unreachable")
  h.assert_equal(client.classify(nil), "unreachable")
end

return T
