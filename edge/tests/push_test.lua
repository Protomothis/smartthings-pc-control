-- The push listener (§4.5, §6.4, §13.3): HTTP parsing, routing by machine_id,
-- the state machine events a push carries, and the subscription/renewal math.
--
-- Nothing here opens a socket: `push.start` takes an injected socket module and
-- `push.ensure` an injected http, exactly as client.lua does.

local h = require "helpers"
local Driver = require "st.driver"
local caps = require "caps"
local client = require "client"
local discovery = require "discovery"
local json = require "st.json"
local poll = require "poll"
local push = require "push"
local state = require "state"

local T = {}

local PREFS = { ipAddress = "192.168.1.20", port = 5001, secret = "s3cret" }

local function pc_device(machine_id, prefs)
  local device = h.fake_device(prefs or PREFS)
  device.device_network_id = discovery.DNI_PREFIX .. machine_id
  device.id = "device-" .. machine_id
  return device
end

local function fake_driver(devices)
  local driver = Driver("test", {})
  driver.devices = devices or {}
  return driver
end

--- A `socket.http.request` stand-in, as in client_test.
local function fake_http(code, body)
  local captured = {}
  return function(req)
    captured.url = req.url
    captured.method = req.method
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
  end, captured
end

--- A TCP socket module whose listener binds to `port` and accepts nothing.
local function fake_socket(port)
  return {
    tcp = function()
      return {
        setoption = function() return 1 end,
        bind = function() return 1 end,
        listen = function() return 1 end,
        getsockname = function() return "0.0.0.0", port end,
        accept = function() return nil, "closed" end,
        close = function() return 1 end,
      }
    end,
  }
end

local function payload(overrides)
  local body = {
    protocol = 1,
    machine_id = "9f3c-guid",
    type = "power.started",
    at = "2026-09-17T23:05:00+09:00",
    data = {},
    status = {
      protocol = 1,
      service_version = "v1.1.0",
      machine_id = "9f3c-guid",
      hostname = "DESKTOP-ABC",
      secret_set = true,
      wol = { ready = true },
      update = { available = false },
      schedule = { active = false },
      session = { exposed = false },
      display = "on",
    },
  }
  for k, v in pairs(overrides or {}) do
    body[k] = v
  end
  return body
end

--------------------------------------------------------------------------------
-- HTTP parsing
--------------------------------------------------------------------------------

function T.test_parse_request_reads_method_path_and_body()
  local body = '{"machine_id":"9f3c-guid"}'
  local raw = "POST /pc/evt HTTP/1.1\r\nHost: 192.168.1.9:41234\r\n"
    .. "Content-Type: application/json\r\nContent-Length: " .. #body .. "\r\n\r\n" .. body
  local method, path, parsed, headers = push.parse_request(raw)
  h.assert_equal(method, "POST")
  h.assert_equal(path, "/pc/evt")
  h.assert_equal(parsed, body)
  h.assert_equal(headers["content-type"], "application/json")
end

function T.test_parse_request_accepts_a_body_without_content_length()
  -- The chunk-less case: no length header, the sender just closes.
  local raw = "POST /pc/evt HTTP/1.1\r\nHost: hub\r\n\r\n{\"type\":\"power.started\"}"
  local method, path, body = push.parse_request(raw)
  h.assert_equal(method, "POST")
  h.assert_equal(path, "/pc/evt")
  h.assert_equal(body, '{"type":"power.started"}')
end

function T.test_parse_request_ignores_the_query_string()
  local method, path = push.parse_request("POST /pc/evt?seq=3 HTTP/1.1\r\n\r\n")
  h.assert_equal(method, "POST")
  h.assert_equal(path, "/pc/evt")
end

function T.test_parse_request_waits_for_the_whole_body()
  local raw = "POST /pc/evt HTTP/1.1\r\nContent-Length: 40\r\n\r\n{\"machine_id\":"
  local method, _, _, err = push.parse_request(raw)
  h.assert_nil(method)
  h.assert_equal(err, "incomplete body")
end

function T.test_parse_request_waits_for_the_blank_line()
  local method, _, _, err = push.parse_request("POST /pc/evt HTTP/1.1\r\nContent-Len")
  h.assert_nil(method)
  h.assert_equal(err, "incomplete headers")
end

function T.test_parse_request_rejects_garbage()
  h.assert_nil((push.parse_request("hello there\r\n\r\n")))
  h.assert_nil((push.parse_request("")))
  h.assert_nil((push.parse_request(nil)))
end

function T.test_parse_request_trims_the_body_to_content_length()
  -- A keep-alive connection can already hold the start of the next request.
  local raw = "POST /pc/evt HTTP/1.1\r\nContent-Length: 2\r\n\r\n{}POST /pc/evt HTTP/1.1\r\n"
  local _, _, body = push.parse_request(raw)
  h.assert_equal(body, "{}")
end

function T.test_lf_only_requests_parse()
  -- Tolerated for the same reason the Go side tolerates bare LF in SSDP.
  local method, path, body = push.parse_request("POST /pc/evt HTTP/1.1\nContent-Length: 2\n\n{}")
  h.assert_equal(method, "POST")
  h.assert_equal(path, "/pc/evt")
  h.assert_equal(body, "{}")
end

function T.test_response_is_a_complete_http_message()
  local text = push.response(200)
  h.assert_contains(text, "HTTP/1.1 200 OK")
  h.assert_contains(text, "Content-Length: 0")
  h.assert_equal(text:sub(-4), "\r\n\r\n")
end

--------------------------------------------------------------------------------
-- events -> state machine (§6.2)
--------------------------------------------------------------------------------

function T.test_stopping_reasons_map_to_power_states()
  local cases = {
    { reason = "suspend", expect = state.SLEEPING, switch = "off" },
    { reason = "hibernate", expect = state.HIBERNATED, switch = "off" },
    { reason = "shutdown", expect = state.SHUTTING_DOWN, switch = "on" },
    { reason = "restart", expect = state.SHUTTING_DOWN, switch = "on" },
    { reason = "unknown", expect = state.SHUTTING_DOWN, switch = "on" },
  }
  for _, case in ipairs(cases) do
    local nxt, events = push.apply(state.new(state.ON), payload({
      type = "power.stopping",
      data = { reason = case.reason },
    }))
    h.assert_equal(nxt.power_state, case.expect, "reason " .. case.reason)
    h.assert_equal(nxt.last_stopping_reason, case.reason)
    h.assert_equal(h.event_value(events, state.CAP_SWITCH, "switch"), case.switch)
    h.assert_equal(h.event_value(events, caps.POWER_STATE, "powerState"), case.expect)
  end
end

function T.test_stopping_without_a_reason_is_unknown()
  local nxt = push.apply(state.new(state.ON), payload({ type = "power.stopping", data = {} }))
  h.assert_equal(nxt.last_stopping_reason, "unknown")
  h.assert_equal(nxt.power_state, state.SHUTTING_DOWN)
end

function T.test_schedule_cancelled_brings_the_switch_back_on()
  -- §6.2: cancelling the grace period on the PC itself.
  local stopping = state.transition(state.new(state.ON), "stopping", "shutdown")
  local nxt, events = push.apply(stopping, payload({ type = "schedule.cancelled" }))
  h.assert_equal(nxt.power_state, state.ON)
  h.assert_equal(h.event_value(events, state.CAP_SWITCH, "switch"), "on")
end

function T.test_any_other_event_means_the_pc_is_up()
  for _, event_type in ipairs({ "power.started", "power.resumed", "display.changed",
    "schedule.created", "system.update_available", "remote.command", "session.locked" }) do
    local nxt = push.apply(state.new(state.OFF), payload({ type = event_type }))
    h.assert_equal(nxt.power_state, state.ON, event_type)
  end
end

function T.test_a_push_while_waking_completes_the_wake()
  local waking = state.transition(state.new(state.OFF), "switch_on")
  h.assert_equal(waking.power_state, state.WAKING)
  local nxt, events = push.apply(waking, payload({ type = "power.started" }))
  h.assert_equal(nxt.power_state, state.ON)
  h.assert_nil(nxt.wake_from)
  -- §6.4: a good status clears whatever the wake attempt had written.
  h.assert_equal(h.event_value(events, caps.STATUS, "message"), "")
end

function T.test_a_push_without_a_status_only_moves_the_power_state()
  local nxt, events = push.apply(state.new(state.ON), { type = "power.stopping", data = { reason = "suspend" } })
  h.assert_equal(nxt.power_state, state.SLEEPING)
  h.assert_nil(events)
end

function T.test_the_status_block_goes_through_the_same_path_as_a_poll()
  local body = payload()
  body.status.schedule = { active = true, command = "shutdown", remaining_seconds = 120,
    execute_at = "2026-09-17T23:10:00+09:00", origin = "smartthings" }
  local nxt, events = push.apply(state.new(), body, { now = "14:05:00", lang = "en" })
  h.assert_true(nxt.schedule_active)
  h.assert_true(h.event_value(events, caps.SCHEDULE, "active"))
  h.assert_equal(h.event_value(events, caps.SCHEDULE, "executeAt"), "23:10")
  h.assert_equal(h.event_value(events, caps.STATUS, "lastSeen"), "14:05:00")
  h.assert_equal(h.event_value(events, caps.STATUS, "connection"), "ok")
end

--------------------------------------------------------------------------------
-- routing (§13.3)
--------------------------------------------------------------------------------

function T.test_a_push_is_routed_by_machine_id()
  local one = pc_device("9f3c-guid")
  local two = pc_device("other-guid")
  local driver = fake_driver({ two, one })

  local ok = push.route(driver, payload({ type = "power.stopping", data = { reason = "suspend" } }))
  h.assert_true(ok)
  h.assert_equal(poll.get_state(one).power_state, state.SLEEPING)
  h.assert_equal(poll.get_state(two).power_state, state.UNKNOWN, "the other PC is untouched")
  h.assert_equal(#two.emitted, 0)
end

function T.test_a_manual_device_is_found_by_its_stored_machine_id()
  -- §13.1: DNI stays `manual-...`, the field carries the identity.
  local manual = h.fake_device(PREFS)
  manual.device_network_id = discovery.DNI_PREFIX .. "manual-abc-1"
  manual:set_field(discovery.MACHINE_FIELD, "9f3c-guid")
  local driver = fake_driver({ manual })

  h.assert_true(push.route(driver, payload()))
  h.assert_equal(poll.get_state(manual).power_state, state.ON)
end

function T.test_an_unknown_machine_id_is_only_logged()
  local driver = fake_driver({ pc_device("9f3c-guid") })
  local ok, why = push.route(driver, payload({ machine_id = "someone-elses-pc" }))
  h.assert_false(ok)
  h.assert_equal(why, "unknown machine id")
end

function T.test_a_push_without_a_machine_id_is_refused()
  local driver = fake_driver({ pc_device("9f3c-guid") })
  local body = payload()
  body.machine_id = nil
  local ok, why = push.route(driver, body)
  h.assert_false(ok)
  h.assert_equal(why, "no machine id")
end

function T.test_deliver_decodes_and_routes_a_raw_body()
  local device = pc_device("9f3c-guid")
  local driver = fake_driver({ device })
  h.assert_true(push.deliver(driver, json.encode(payload()), {}))
  h.assert_equal(device.health, "online")
  h.assert_equal(h.event_value(h.emitted(device), caps.STATUS, "connection"), "ok")
end

function T.test_deliver_refuses_a_foreign_protocol()
  local driver = fake_driver({ pc_device("9f3c-guid") })
  local ok, why = push.deliver(driver, json.encode(payload({ protocol = 2 })), {})
  h.assert_false(ok)
  h.assert_equal(why, "incompatible")
end

function T.test_deliver_refuses_a_body_that_is_not_json()
  local ok, why = push.deliver(fake_driver({}), "not json at all", {})
  h.assert_false(ok)
  h.assert_equal(why, "bad json")
end

--------------------------------------------------------------------------------
-- subscriptions (§4.5)
--------------------------------------------------------------------------------

function T.test_renew_delay_is_80_percent_of_the_ttl()
  h.assert_equal(push.renew_delay(600), 480)
  h.assert_equal(push.renew_delay(60), 48)
  h.assert_equal(push.renew_delay(3600), 2880)
  h.assert_equal(push.renew_delay(nil), 480, "the default TTL is 600s")
end

function T.test_a_subscription_is_live_until_the_renewal_point()
  local sub = push.subscription({ id = "sub-1" }, "http://192.168.1.9:41234/pc/evt", 600, 1000)
  h.assert_equal(sub.renew_at, 1480)
  h.assert_true(push.subscription_live(sub, sub.callback, 1479))
  h.assert_false(push.subscription_live(sub, sub.callback, 1480), "renew at 80% of the TTL")
  h.assert_false(push.subscription_live(sub, "http://192.168.1.9:50000/pc/evt", 1000),
    "a listener on a new port needs a new subscription")
  h.assert_false(push.subscription_live(nil, sub.callback, 1000))
  h.assert_false(push.subscription_live({ id = "" }, sub.callback, 1000))
end

function T.test_callback_url()
  h.assert_equal(push.callback_url({ ip = "192.168.1.9", port = 41234 }),
    "http://192.168.1.9:41234/pc/evt")
  h.assert_nil(push.callback_url({ port = 41234 }), "no hub IP, no callback")
  h.assert_nil(push.callback_url({ ip = "192.168.1.9" }))
end

function T.test_start_opens_a_listener_and_reuses_it()
  local driver = fake_driver({})
  local listener = push.start(driver, {
    socket = fake_socket(41234), hub_ip = "192.168.1.9", spawn = function() end,
  })
  h.assert_equal(listener.port, 41234)
  h.assert_equal(listener.ip, "192.168.1.9")
  h.assert_equal(push.callback_url(listener), "http://192.168.1.9:41234/pc/evt")
  local again = push.start(driver, { socket = fake_socket(50000), spawn = function() end })
  h.assert_equal(again.port, 41234, "one listener per driver")
end

function T.test_subscribe_sends_the_fields_the_service_decodes()
  local driver = fake_driver({})
  push.start(driver, { socket = fake_socket(41234), hub_ip = "192.168.1.9", spawn = function() end })
  local device = pc_device("9f3c-guid")
  local http, captured = fake_http(200, '{"id":"sub-7","expires_at":"2026-09-17T23:15:00+09:00"}')

  local ok = push.ensure(driver, device, { http = http, now = function() return 1000 end })
  h.assert_true(ok)
  h.assert_equal(captured.method, "POST")
  h.assert_equal(captured.url, "http://192.168.1.20:5001/st/v1/subscribe")
  local body = json.decode(captured.body)
  h.assert_equal(body.callback, "http://192.168.1.9:41234/pc/evt")
  h.assert_equal(body.ttl_seconds, 600)
  h.assert_equal(body.driver_version, require "driver_version")

  local sub = device:get_field(push.SUB_FIELD)
  h.assert_equal(sub.id, "sub-7")
  h.assert_equal(sub.renew_at, 1480)
  -- §4.5: renewal is armed at 80% of the TTL.
  local timer
  for _, t in ipairs(driver.timers) do
    if t.name == "pc-push-renew" then
      timer = t
    end
  end
  h.assert_equal((timer or {}).delay, 480)
end

function T.test_a_live_subscription_is_not_renewed_early()
  local driver = fake_driver({})
  push.start(driver, { socket = fake_socket(41234), hub_ip = "192.168.1.9", spawn = function() end })
  local device = pc_device("9f3c-guid")
  local calls = 0
  local deps = {
    http = function(req)
      calls = calls + 1
      req.sink('{"id":"sub-7","expires_at":"x"}')
      return 1, 200, {}, "HTTP/1.1 200"
    end,
    now = function() return 1000 end,
  }
  push.ensure(driver, device, deps)
  push.ensure(driver, device, deps)
  h.assert_equal(calls, 1, "the second poll reuses the live subscription")

  deps.now = function() return 1480 end
  push.ensure(driver, device, deps)
  h.assert_equal(calls, 2, "at 80% of the TTL it renews")
end

function T.test_an_unauthorized_subscribe_falls_back_to_polling()
  local driver = fake_driver({})
  push.start(driver, { socket = fake_socket(41234), hub_ip = "192.168.1.9", spawn = function() end })
  local device = pc_device("9f3c-guid")
  -- A subscription that is due for renewal, so this is the renewal failing.
  device:set_field(push.SUB_FIELD, { id = "stale", callback = "http://192.168.1.9:41234/pc/evt",
    renew_at = 500 })

  local ok, kind = push.ensure(driver, device, {
    http = fake_http(401, '{"error":"unauthorized"}'), now = function() return 1000 end,
  })
  h.assert_false(ok)
  h.assert_equal(kind, "unauthorized")
  h.assert_nil(device:get_field(push.SUB_FIELD), "a refused subscription is dropped")
end

function T.test_without_a_listener_nothing_is_subscribed()
  local device = pc_device("9f3c-guid")
  local ok, why = push.ensure(fake_driver({}), device, { http = fake_http(200, "{}") })
  h.assert_false(ok)
  h.assert_equal(why, "no listener")
  h.assert_nil(device:get_field(push.SUB_FIELD))
end

function T.test_stop_unsubscribes_and_forgets()
  local driver = fake_driver({})
  push.start(driver, { socket = fake_socket(41234), hub_ip = "192.168.1.9", spawn = function() end })
  local device = pc_device("9f3c-guid")
  local http, captured = fake_http(200, '{"id":"sub-7","expires_at":"x"}')
  push.ensure(driver, device, { http = http, now = function() return 1000 end })

  local removed = fake_http(200, '{"removed":true}')
  h.assert_true(push.stop(driver, device, { http = removed }))
  h.assert_nil(device:get_field(push.SUB_FIELD))
  h.assert_nil(device:get_field(push.RENEW_TIMER_FIELD))
  h.assert_equal(captured.method, "POST", "the subscribe request is untouched")
end

function T.test_the_poll_subscribes_after_a_good_status()
  local driver = fake_driver({})
  push.start(driver, { socket = fake_socket(41234), hub_ip = "192.168.1.9", spawn = function() end })
  local device = pc_device("9f3c-guid")
  driver.devices = { device }

  local status = json.encode({
    protocol = 1, service_version = "v1.1.0", machine_id = "9f3c-guid",
    hostname = "DESKTOP-ABC", secret_set = true, wol = { ready = true },
    update = { available = false }, schedule = { active = false },
    session = { exposed = false }, display = "unknown",
  })
  local seen = {}
  local http = function(req)
    seen[#seen + 1] = req.url
    req.sink(req.url:find("subscribe", 1, true) and '{"id":"sub-1","expires_at":"x"}' or status)
    return 1, 200, {}, "HTTP/1.1 200"
  end

  h.assert_true(poll.once(driver, device, { deps = { http = http } }))
  h.assert_equal(seen[1], "http://192.168.1.20:5001/st/v1/status")
  h.assert_equal(seen[2], "http://192.168.1.20:5001/st/v1/subscribe")
  -- §13.1: the identity is remembered from the status body.
  h.assert_equal(device:get_field(discovery.MACHINE_FIELD), "9f3c-guid")
  h.assert_equal(device:get_field(discovery.HOSTNAME_FIELD), "DESKTOP-ABC")
  h.assert_equal(client.device_base_url(device), "http://192.168.1.20:5001/st/v1")
end

return T
