local h = require "helpers"
local client = require "client"
local json = require "st.json"
-- The other half of error classification (err_kind -> connection/message,
-- §3.1) lives in poll.lua, so the two are asserted together.
local caps = require "caps"
local fields = require "device.fields"
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

-- §3.2, trimmed to what a poll test reads back. #96/#97: `wol.selected` is the
-- adapter the service chose for Wake-on-LAN, and `adapters[]` carries the same
-- answer as `ip` + `selected` on the row; `overrides.wol` replaces the whole
-- block, which is how the old-shape (adapters only) tests below are written.
local function status_body(overrides)
  local body = {
    protocol = 1, service_version = "v1.1.0", power = "on", secret_set = true,
    wol = {
      ready = true,
      selected = {
        name = "Ethernet", mac = "AA:BB:CC:DD:EE:FF", ip = "192.168.1.20",
        wol_enabled = true, wol_capable = true, source = "auto",
      },
      adapters = {
        { name = "Wi-Fi", mac = "11:22:33:44:55:66", ip = "192.168.1.31",
          wol_enabled = true, wol_capable = true, selected = false },
        { name = "Ethernet", mac = "AA:BB:CC:DD:EE:FF", ip = "192.168.1.20",
          wol_enabled = true, wol_capable = true, selected = true },
      },
    },
  }
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
  local ok, _, err = client.get_status(device(), { http = broken_http("No route to host") })
  h.assert_false(ok)
  h.assert_equal(err, "unreachable")
end

-- A refused connection is the PC answering with a RST: it is on, its app is
-- not listening (the service's inbound allow rule makes Windows refuse rather
-- than drop). Every other transport failure stays `unreachable`.
function T.test_a_refused_connection_is_app_down()
  for _, message in ipairs({
    "connection refused", "Connection refused", "Connection refused (os error 111)",
    "ECONNREFUSED", "connect: CONNECTION REFUSED",
  }) do
    h.assert_equal(client.transport_kind(message), "app_down", message)
    h.assert_equal(client.classify(message), "app_down", message)
    local ok, body, err = client.get_status(device(), { http = broken_http(message) })
    h.assert_false(ok, message)
    h.assert_nil(body, message)
    h.assert_equal(err, "app_down", message)
  end
  -- Raised instead of returned: the same rule.
  local _, _, err = client.get_status(device(), {
    http = function() error("connection refused", 0) end,
  })
  h.assert_equal(err, "app_down", "a raised refusal")
end

function T.test_app_down_is_worded_apart_from_an_unreachable_pc()
  -- The enum has no value of its own (pcInfo.json is published): `unreachable`.
  h.assert_equal(poll.connection_for("app_down"), "unreachable")
  h.assert_equal(poll.message_for("app_down", nil, "ko"),
    "PC는 켜져 있지만 PC 앱이 응답하지 않습니다 · PC에서 앱을 다시 실행하세요")
  h.assert_equal(poll.message_for("app_down", nil, "en"),
    "The PC is on but the PC app isn't responding · restart the app on the PC")
  -- Unreachable: the old sentence while the PC may still be on, the likely
  -- reasons once it counts as off.
  h.assert_equal(poll.message_for("unreachable", nil, "ko"), "PC에 연결할 수 없습니다")
  h.assert_equal(poll.message_for("unreachable", nil, "ko", state.ON), "PC에 연결할 수 없습니다")
  h.assert_equal(poll.message_for("unreachable", nil, "ko", state.OFF),
    "PC가 꺼져 있거나 네트워크에 연결되지 않았습니다")
  h.assert_equal(poll.message_for("unreachable", nil, "en", state.OFF), "The PC is off or offline")
  h.assert_equal(poll.message_for("app_down", nil, "en", state.OFF),
    poll.message_for("app_down", nil, "en"), "a refusal is never \"off\"")
end

function T.test_a_refused_poll_ends_a_wake()
  -- The PC came up (it refuses), the app has not: on, and no "깨우기 실패" 90 s
  -- later from a timer that is still waiting for the app.
  local d = device()
  fields.set_state(d, state.transition(state.new(state.OFF), "switch_on"))
  local cancelled = {}
  local driver = { cancel_timer = function(_, timer) cancelled[#cancelled + 1] = timer end }
  d:set_field(fields.WAKE_TIMER, "wake-timer")
  local ok, kind = poll.once(driver, d, { deps = { http = broken_http("Connection refused") } })
  h.assert_false(ok)
  h.assert_equal(kind, "app_down")
  h.assert_equal(fields.state(d).power_state, state.ON)
  h.assert_nil(d:get_field(fields.WAKE_TIMER))
  h.assert_deep_equal(cancelled, { "wake-timer" })
  h.assert_equal(h.last_value(h.emitted(d), nil, caps.POWER_STATE, "powerState"), state.ON)
  h.assert_equal(d.health, "online")
end

function T.test_a_refused_command_says_the_app_is_down()
  -- A command that is refused paints the same words as a refused poll.
  local common = require "handlers.common"
  local d = device({ language = "en" })
  common.report_error(d, "app_down", nil)
  local emitted = h.emitted(d)
  h.assert_equal(h.event_value(emitted, caps.STATUS, "connection"), "unreachable")
  h.assert_equal(h.event_value(emitted, caps.STATUS, "summary"), "PC app not responding")
  h.assert_contains(h.event_value(emitted, caps.STATUS, "message"), "restart the app")
end

function T.test_every_other_transport_failure_is_unreachable()
  for _, message in ipairs({
    "timeout", "Operation timed out", "No route to host", "Host is unreachable",
    "Network is unreachable", "host not found", "Name or service not known",
    -- luasocket says "closed" for a reset and for a reply cut short alike; the
    -- message does not say it happened at connect time.
    "closed", "connection reset by peer", "", nil,
  }) do
    h.assert_equal(client.transport_kind(message), "unreachable", tostring(message))
    local _, _, err = client.get_status(device(), { http = broken_http(message or "timeout") })
    h.assert_equal(err, "unreachable", tostring(message))
  end
end

-- After the service's own `power.stopping` `app_stop` (state `app_stopped`),
-- a reset is read as the refusal it most likely is: the hub may word the RST
-- a closed port answers with as "closed". A timeout stays a timeout.
function T.test_a_reset_after_app_stop_is_app_down()
  for _, message in ipairs({ "closed", "connection reset by peer", "ECONNRESET" }) do
    h.assert_equal(client.transport_kind(message, true), "app_down", message)
    h.assert_equal(client.transport_kind(message, false), "unreachable", message)
  end
  for _, message in ipairs({ "timeout", "No route to host", "Host is unreachable" }) do
    h.assert_equal(client.transport_kind(message, true), "unreachable", message)
  end

  -- Through a request: the device's state decides.
  local d = device()
  fields.set_state(d, state.transition(state.new(state.ON), "stopping", "app_stop"))
  local _, _, err = client.get_status(d, { http = broken_http("closed") })
  h.assert_equal(err, "app_down", "closed right after app_stop")
  _, _, err = client.get_status(d, { http = function() error("closed", 0) end })
  h.assert_equal(err, "app_down", "a raised reset after app_stop")
  _, _, err = client.get_status(d, { http = broken_http("timeout") })
  h.assert_equal(err, "unreachable", "a timeout after app_stop")
  -- The outage is over (the PC counted off): "closed" is unreachable again.
  local s = fields.state(d)
  s = state.transition(state.transition(s, "unreachable"), "unreachable")
  fields.set_state(d, s)
  _, _, err = client.get_status(d, { http = broken_http("closed") })
  h.assert_equal(err, "unreachable", "closed in a later outage")
  -- And without any app_stop at all.
  _, _, err = client.get_status(device(), { http = broken_http("closed") })
  h.assert_equal(err, "unreachable")
end

-- C1 end to end: the push, then the polls of a PC whose app is gone.
function T.test_after_app_stop_the_polls_keep_the_app_down_until_two_timeouts()
  local push = require "push"
  local d = device({ language = "ko" })
  local driver = { cancel_timer = function() end }
  fields.set_state(d, state.new(state.ON))
  push.apply_to_device(driver, d, { machine_id = "x", type = "power.stopping", data = { reason = "app_stop" } })
  h.assert_equal(fields.state(d).power_state, state.ON)
  h.assert_equal(h.last_value(h.emitted(d), nil, caps.STATUS, "summary"), "PC 앱 응답 없음")
  h.assert_equal(h.last_value(h.emitted(d), nil, caps.POWER_STATE, "powerState"), state.ON)

  for _, message in ipairs({ "connection refused", "closed", "closed", "Connection refused" }) do
    local _, kind = poll.once(driver, d, { deps = { http = broken_http(message) } })
    h.assert_equal(kind, "app_down", message)
    h.assert_equal(fields.state(d).power_state, state.ON, message)
    h.assert_equal(fields.state(d).unreachable_count, 0, message)
  end
  h.assert_equal(h.last_value(h.emitted(d), nil, caps.STATUS, "summary"), "PC 앱 응답 없음")

  -- The PC goes away after all: timeouts count, two of them are off.
  local _, kind = poll.once(driver, d, { deps = { http = broken_http("timeout") } })
  h.assert_equal(kind, "unreachable")
  h.assert_equal(fields.state(d).power_state, state.ON, "one timeout is not off")
  poll.once(driver, d, { deps = { http = broken_http("timeout") } })
  h.assert_equal(fields.state(d).power_state, state.OFF)
  h.assert_equal(h.last_value(h.emitted(d), nil, caps.POWER_STATE, "powerState"), state.OFF)
  -- A new outage: "closed" is no longer read as a refusal.
  _, kind = poll.once(driver, d, { deps = { http = broken_http("closed") } })
  h.assert_equal(kind, "unreachable")
  h.assert_equal(fields.state(d).power_state, state.OFF)
end

local function transport_lines()
  local log = require "log"
  local out = {}
  for _, entry in ipairs(log.entries) do
    if entry.level == "info" and tostring(entry[1]):find("^transport error: ") then
      out[#out + 1] = entry[1]
    end
  end
  return out
end

-- The hub's own wording for a refused or reset connection has never been
-- seen: logged at info, the raw text and the classification, once per change.
function T.test_a_transport_error_is_logged_when_it_changes()
  local log = require "log"
  log.reset()
  local d = device()
  d.id = "pc-1"
  for _ = 1, 3 do
    client.get_status(d, { http = broken_http("connection refused") })
  end
  h.assert_deep_equal(transport_lines(), { "transport error: connection refused -> app_down (pc-1)" },
    "the same error every poll is logged once")

  client.get_status(d, { http = broken_http("timeout") })
  client.get_status(d, { http = broken_http("timeout") })
  h.assert_equal(#transport_lines(), 2, "new raw text: logged")
  h.assert_equal(transport_lines()[2], "transport error: timeout -> unreachable (pc-1)")

  -- Same raw text, new classification: logged.
  fields.set_state(d, state.transition(state.new(state.ON), "stopping", "app_stop"))
  client.get_status(d, { http = broken_http("closed") })
  client.get_status(d, { http = broken_http("closed") })
  h.assert_equal(#transport_lines(), 3)
  h.assert_equal(transport_lines()[3], "transport error: closed -> app_down (pc-1)")
  fields.set_state(d, state.new(state.ON))
  client.get_status(d, { http = broken_http("closed") })
  h.assert_equal(transport_lines()[4], "transport error: closed -> unreachable (pc-1)")

  -- An HTTP answer ends the outage: the next one is logged even with the same
  -- words.
  client.get_status(d, { http = fake_http(401, "{}") })
  client.get_status(d, { http = broken_http("closed") })
  h.assert_equal(#transport_lines(), 5, "a new outage logs again")

  -- Per device.
  local other = device()
  other.id = "pc-2"
  client.get_status(other, { http = broken_http("closed") })
  h.assert_equal(#transport_lines(), 6, "another device has its own last line")
  log.reset()
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
  h.assert_equal(poll.message_for(err, body, "en"), "Driver update required (hub)")
  h.assert_equal(poll.message_for(err, body, "ko"), "드라이버 업데이트 필요 (허브)")
end

function T.test_a_status_without_protocol_is_incompatible()
  local http = fake_http(200, '{"power":"on"}')
  local _, body, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "incompatible")
  -- No `protocol` at all means the service predates it: it is the old side.
  h.assert_contains(poll.message_for(err, body, "en"), "Requires PC app v1.1.0")
end

function T.test_an_older_protocol_says_the_service_is_too_old()
  local http = fake_http(200, status_body({ protocol = 0 }))
  local _, body, err = client.get_status(device(), { http = http })
  h.assert_equal(err, "incompatible")
  h.assert_contains(poll.message_for(err, body, "ko"), "PC 앱 v1.1.0 이상 필요")
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
  fields.set_state(d, state.new(state.ON))
  local ok, kind = poll.once(nil, d, { deps = { http = fake_http(429, '{"error":"rate limited"}') } })
  h.assert_false(ok)
  h.assert_equal(kind, "ratelimited")
  h.assert_equal(#d.emitted, 0, "a 429 must not repaint any attribute")
  h.assert_nil(d.health, "a 429 must not touch health")
  h.assert_equal(fields.state(d).power_state, state.ON)
end

function T.test_an_unreachable_poll_does_report()
  -- The contrast to the test above: a real failure is shown.
  local d = device()
  fields.set_state(d, state.new(state.ON))
  local ok, kind = poll.once(nil, d, { deps = { http = broken_http("timeout") } })
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
  h.assert_nil(fields.service_version(d), "nothing is remembered before the first poll")

  h.assert_true(poll.once(nil, d, { deps = { http = fake_http(200, status_body()) } }))
  h.assert_equal(d:get_field(fields.SERVICE_VERSION), "v1.1.0",
    "a successful poll persists the service version")

  local ok = poll.once(nil, d, { deps = { http = broken_http("timeout") } })
  h.assert_false(ok)
  -- The last value of each row: an unchanged one is not emitted again (the
  -- event budget), so what the row shows is what it was last told.
  local emitted = h.emitted(d)
  h.assert_equal(h.last_value(emitted, nil, caps.STATUS, "connection"), "unreachable")
  local kept = state.versions("v1.1.0", "ko")
  h.assert_equal(h.last_value(emitted, nil, caps.VERSION, "versions"), kept)
  h.assert_equal(h.last_value(emitted, nil, caps.STATUS, "versions"), kept)
  h.assert_contains(kept, "v1.1.0")
  h.assert_equal(kept:find("업데이트", 1, true), nil,
    "the update half is never remembered - only a live answer can offer one")
end

function T.test_the_last_seen_time_survives_a_failed_poll()
  -- #102: a successful poll remembers when (persisted, fields.LAST_SEEN),
  -- and the failures after it read it back instead of overwriting it.
  local d = device({ language = "ko" })
  local now = 3000000
  local clock = function() return now end

  -- Never seen: the row keeps its old words.
  poll.once(nil, d, { deps = { http = broken_http("timeout"), now = clock } })
  h.assert_equal(h.event_value(h.emitted(d), caps.STATUS, "summary"), "연결 안 됨 · 응답 없음")
  h.assert_nil(fields.last_seen(d), "a failed poll is not a sighting")

  d.emitted = {}
  h.assert_true(poll.once(nil, d, {
    deps = { http = fake_http(200, status_body({ uptime_seconds = 266400 })), now = clock },
  }))
  h.assert_equal(d:get_field(fields.LAST_SEEN), 3000000)
  -- The sample is a v1.1.0 PC: older than features.RECOMMENDED_SERVICE_VERSION.
  h.assert_equal(h.event_value(h.emitted(d), caps.STATUS, "summary"), "연결됨 · 3일 2시간 · 앱 업데이트 필요")

  for _, minutes in ipairs({ 5, 12 }) do
    now = 3000000 + minutes * 60
    d.emitted = {}
    h.assert_false(poll.once(nil, d, { deps = { http = broken_http("timeout"), now = clock } }))
    h.assert_equal(h.event_value(h.emitted(d), caps.STATUS, "summary"),
      string.format("응답 없음 · 마지막 확인 %d분 전", minutes))
    h.assert_equal(fields.last_seen(d), 3000000, "the failure leaves the time alone")
  end
end

function T.test_a_poll_remembers_the_mac_the_service_chose()
  -- §6.4/#97: the wake happens while the PC is off, so everything it needs is
  -- persisted on the way past. The Wi-Fi card is listed first and has WoL on -
  -- the guess this replaced would have taken it - but the service picked the
  -- Ethernet one, and that is the MAC the magic packet goes to.
  local d = device()
  h.assert_true(poll.once(nil, d, { deps = { http = fake_http(200, status_body()) } }))
  h.assert_equal(d:get_field(fields.WOL_MAC), "AA:BB:CC:DD:EE:FF")
  h.assert_equal(d:get_field(fields.WOL_ADAPTER), "Ethernet")
  h.assert_equal(d:get_field(fields.WOL_READY), true)

  -- The chosen adapter has WoL off: the warning is about that adapter, even
  -- though `wol.ready` and the other NIC both say everything is fine.
  local off = status_body({
    wol = {
      ready = true,
      selected = { name = "Ethernet", mac = "AA:BB:CC:DD:EE:FF", wol_enabled = false },
      adapters = { { name = "Wi-Fi", mac = "11:22:33:44:55:66", wol_enabled = true } },
    },
  })
  h.assert_true(poll.once(nil, d, { deps = { http = fake_http(200, off) } }))
  h.assert_equal(d:get_field(fields.WOL_READY), false)
  h.assert_equal(d:get_field(fields.WOL_MAC), "AA:BB:CC:DD:EE:FF")
end

function T.test_a_poll_falls_back_to_the_old_adapter_guess()
  -- An old-shape body, from a service that only lists adapters (pre-#96): the
  -- driver picks the first one with WoL on, exactly as it always did.
  local d = device()
  local body = status_body({
    wol = {
      ready = true,
      adapters = {
        { name = "Wi-Fi", mac = "11:22:33:44:55:66", wol_enabled = false },
        { name = "Ethernet", mac = "AA:BB:CC:DD:EE:FF", wol_enabled = true },
      },
    },
  })
  h.assert_true(poll.once(nil, d, { deps = { http = fake_http(200, body) } }))
  h.assert_equal(d:get_field(fields.WOL_MAC), "AA:BB:CC:DD:EE:FF")
  h.assert_nil(d:get_field(fields.WOL_ADAPTER), "nothing to name the adapter with")
  h.assert_equal(d:get_field(fields.WOL_READY), true)
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
  h.assert_equal(client.classify("timeout"), "unreachable")
  h.assert_equal(client.classify("connection refused"), "app_down")
  h.assert_equal(client.classify(nil), "unreachable")
end

return T
