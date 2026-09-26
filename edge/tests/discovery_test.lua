-- Discovery: the SSDP search, the multi-PC identity rules of §6.5 and the PC
-- id the device's model carries (#94). The socket and the description fetch
-- are injected, so this never multicasts anything.

local h = require "helpers"
local Driver = require "st.driver"
local caps = require "caps"
local client = require "client"
local discovery = require "discovery"
local i18n = require "i18n"
local json = require "st.json"

local T = {}

local function fake_driver(devices)
  local driver = Driver("test", {})
  driver.devices = devices or {}
  return driver
end

local function pc_device(dni, prefs)
  local device = h.fake_device(prefs or {})
  device.device_network_id = dni
  device.id = "device-" .. dni
  return device
end

-- The exact answer service/st_ssdp.go's ssdpResponseText builds.
local function ssdp_response(ip, port, machine_id)
  return table.concat({
    "HTTP/1.1 200 OK",
    "CACHE-CONTROL: max-age=1800",
    "DATE: Wed, 17 Sep 2026 14:05:00 GMT",
    "EXT:",
    string.format("LOCATION: http://%s:%d/st/v1/description", ip, port),
    "SERVER: Windows/10.0.22631 UPnP/1.0 smartthings-pc-control/v1.1.0",
    "ST: " .. discovery.SSDP_ST,
    string.format("USN: uuid:%s::%s", machine_id, discovery.SSDP_ST),
    "", "",
  }, "\r\n")
end

local function description(machine_id, hostname, port)
  return json.encode({
    protocol = 1,
    machine_id = machine_id,
    hostname = hostname,
    service_version = "v1.1.0",
    port = port or 5001,
    secret_set = true,
  })
end

--- A UDP module that hands out `responses` one receivefrom at a time.
local function fake_udp(responses)
  local sent = {}
  local index = 0
  local module = {
    sent = sent,
    udp = function()
      return {
        setsockname = function() return 1 end,
        settimeout = function() return 1 end,
        sendto = function(_, data, ip, port)
          sent[#sent + 1] = { data = data, ip = ip, port = port }
          return 1
        end,
        receivefrom = function()
          index = index + 1
          local response = responses[index]
          if response then
            return response.data, response.from
          end
          return nil, "timeout"
        end,
        close = function() return 1 end,
      }
    end,
  }
  return module
end

--- http that answers every description request with `bodies[url]`.
local function fake_descriptions(bodies)
  return function(req)
    local body = bodies[req.url]
    if not body then
      return nil, "connection refused"
    end
    req.sink(body)
    return 1, 200, {}, "HTTP/1.1 200 OK"
  end
end

--- A monotonically advancing clock, so the search loop terminates at once.
local function ticking_clock()
  local t = 0
  return function()
    t = t + 1
    return t
  end
end

local function search_deps(responses, bodies)
  return {
    socket = fake_udp(responses),
    http = fake_descriptions(bodies or {}),
    now = ticking_clock(),
  }
end

--------------------------------------------------------------------------------
-- a search that finds nothing (#94)
--------------------------------------------------------------------------------

function T.test_a_search_that_nobody_answers_creates_nothing()
  -- #94: the placeholder device (`PC Control (set IP in settings)`) is gone.
  -- A scan with the PC off has to leave the hub exactly as it was, or the next
  -- scan - for any brand of device - finds a blank PC Control device in the way.
  local driver = fake_driver()
  discovery.handle(driver, {}, function() return true end, search_deps({}))
  h.assert_nil(driver.created, "nothing answered, so nothing is added")
end

function T.test_a_search_that_finds_nothing_leaves_existing_devices_alone()
  local existing = pc_device("pc-control-9f3c-guid", { ipAddress = "192.168.1.20" })
  local driver = fake_driver({ existing })
  discovery.handle(driver, {}, function() return true end, search_deps({}))
  h.assert_nil(driver.created)
  h.assert_nil(existing:get_field(client.IP_FIELD), "no hit, no address change")
end

function T.test_the_empty_search_hint_names_the_precondition()
  -- The only thing the driver can still say: the PC and the service have to be
  -- running and UDP 1900 has to reach them. Korean and English in one line,
  -- because a log line belongs to no device and follows no preference.
  local hint = i18n.t(nil, "discovery_none")
  h.assert_contains(hint, "PC Control")
  h.assert_contains(hint, "1900")
  h.assert_contains(hint, "켜져 있고")
  h.assert_contains(i18n.t("en", "discovery_none"), "UDP 1900")
end

--------------------------------------------------------------------------------
-- the PC id in the device model (#94)
--------------------------------------------------------------------------------

function T.test_the_model_carries_the_first_eight_characters_of_the_id()
  h.assert_equal(discovery.short_id("58bff996-1c2d-4e5f-8a9b-0c1d2e3f4a5b"), "58bff996")
  h.assert_equal(discovery.model_for("58bff996-1c2d-4e5f"), "PC Control · 58bff996")
  -- A short id is taken whole rather than padded.
  h.assert_equal(discovery.model_for("abc"), "PC Control · abc")
  -- No identity: the bare model name every device had before #94.
  h.assert_equal(discovery.model_for(nil), "PC Control")
  h.assert_equal(discovery.model_for(""), "PC Control")
end

function T.test_a_created_device_carries_the_pc_id_in_its_model()
  local driver = fake_driver()
  discovery.create(driver, { ip = "192.168.1.20", hostname = "DESKTOP-ABC",
    machine_id = "58bff996-1c2d-4e5f" })
  local spec = driver.created[1]
  h.assert_equal(spec.model, "PC Control · 58bff996")
  -- The label is still the name a person gave the PC, and the vendor label
  -- still names the product.
  h.assert_equal(spec.label, "DESKTOP-ABC 컴퓨터")
  h.assert_equal(spec.vendor_provided_label, "PC Control")
  h.assert_equal(spec.manufacturer, "Protomothis")

  -- Created with the id in place, so the one-time update has nothing to do.
  local device = pc_device("pc-control-58bff996-1c2d-4e5f")
  discovery.adopt(device)
  h.assert_false(discovery.ensure_model(device))
  h.assert_equal(#device.metadata_updates, 0)
end

function T.test_an_existing_device_learns_the_model_once()
  -- A device added before #94: its model is the bare product name.
  local device = pc_device("pc-control-58bff996-1c2d-4e5f", { ipAddress = "192.168.1.20" })
  device.model = "PC Control"

  h.assert_true(discovery.ensure_model(device))
  h.assert_equal(#device.metadata_updates, 1)
  h.assert_equal(device.metadata_updates[1].model, "PC Control · 58bff996")
  h.assert_nil(device.metadata_updates[1].profile, "only the model is touched")
  h.assert_equal(device.model, "PC Control · 58bff996")

  h.assert_false(discovery.ensure_model(device), "once per device")
  h.assert_equal(#device.metadata_updates, 1)
end

function T.test_a_device_without_an_identity_keeps_its_model()
  -- §6.5: a device added by hand before #94 has no machine_id until its first
  -- successful poll, and there is nothing to put in the model until then.
  local manual = pc_device("pc-control-manual-abc-1", { ipAddress = "192.168.1.20" })
  h.assert_false(discovery.ensure_model(manual))
  h.assert_equal(#manual.metadata_updates, 0)

  manual:set_field(discovery.MACHINE_FIELD, "58bff996-1c2d-4e5f")
  h.assert_true(discovery.ensure_model(manual))
  h.assert_equal(manual.metadata_updates[1].model, "PC Control · 58bff996")
end

function T.test_a_hub_that_refuses_the_update_is_survived()
  local device = pc_device("pc-control-58bff996-1c2d", { ipAddress = "192.168.1.20" })
  function device:try_update_metadata()
    error("hub says no")
  end
  h.assert_false(discovery.ensure_model(device))
  h.assert_nil(device:get_field(discovery.MODEL_FIELD), "so the next run tries again")
end

function T.test_the_first_successful_poll_puts_the_id_in_the_model()
  -- §6.5: `remember_identity` is where a device learns or confirms its id, so
  -- it is where an existing device picks the model up (#94).
  local poll = require "poll"
  local device = pc_device("pc-control-manual-abc-1", { ipAddress = "192.168.1.20" })
  device.model = "PC Control"
  poll.remember_identity(device, { machine_id = "58bff996-1c2d-4e5f",
    hostname = "DESKTOP-ABC" })
  h.assert_equal(device.model, "PC Control · 58bff996")
  h.assert_equal(device:get_field(discovery.MACHINE_FIELD), "58bff996-1c2d-4e5f")

  -- Every later poll is a field read and nothing else.
  poll.remember_identity(device, { machine_id = "58bff996-1c2d-4e5f",
    hostname = "DESKTOP-ABC" })
  h.assert_equal(#device.metadata_updates, 1)
end

function T.test_network_ids_are_unique()
  local first = discovery.network_id(nil)
  local second = discovery.network_id(nil)
  h.assert_true(first ~= second, "two manual ids must differ")
end

function T.test_network_id_prefers_the_machine_id()
  -- §6.5: the identity is the machine_id, so the device survives an IP change.
  h.assert_equal(discovery.network_id("9f3c-guid"), "pc-control-9f3c-guid")
end

function T.test_a_discovered_host_is_labelled_with_its_hostname()
  local driver = fake_driver()
  discovery.create(driver, { ip = "192.168.1.20", hostname = "DESKTOP-ABC", machine_id = "guid" })
  h.assert_equal(driver.created[1].label, "DESKTOP-ABC 컴퓨터")
  h.assert_equal(driver.created[1].device_network_id, "pc-control-guid")
end

function T.test_a_created_device_adopts_the_address_it_was_found_at()
  local driver = fake_driver()
  discovery.create(driver, { ip = "192.168.1.20", port = 5002, hostname = "DESKTOP-ABC",
    machine_id = "guid" })
  -- The device object only exists once the platform created it.
  local device = pc_device("pc-control-guid")
  h.assert_true(discovery.adopt(device))
  h.assert_equal(device:get_field(client.IP_FIELD), "192.168.1.20")
  h.assert_equal(device:get_field(client.PORT_FIELD), 5002)
  h.assert_equal(device:get_field(discovery.MACHINE_FIELD), "guid")
  h.assert_equal(client.device_base_url(device), "http://192.168.1.20:5002/st/v1")
  h.assert_false(discovery.adopt(device), "the pending address is taken only once")
end

--------------------------------------------------------------------------------
-- SSDP text (§3.6)
--------------------------------------------------------------------------------

function T.test_msearch_matches_what_the_responder_requires()
  local text = discovery.msearch()
  h.assert_contains(text, "M-SEARCH * HTTP/1.1\r\n")
  h.assert_contains(text, "HOST: 239.255.255.250:1900")
  h.assert_contains(text, 'MAN: "ssdp:discover"')
  h.assert_contains(text, "MX: 2")
  h.assert_contains(text, "ST: urn:smartthings-pc-control:device:pc:1")
  h.assert_equal(text:sub(-4), "\r\n\r\n", "the message ends with a blank line")
end

function T.test_parse_response_reads_location_and_machine_id()
  local parsed = discovery.parse_response(ssdp_response("192.168.1.20", 5001, "9f3c-guid"))
  h.assert_equal(parsed.location, "http://192.168.1.20:5001/st/v1/description")
  h.assert_equal(parsed.machine_id, "9f3c-guid")
  h.assert_equal(parsed.st, discovery.SSDP_ST)
end

function T.test_parse_response_rejects_anything_else()
  h.assert_nil(discovery.parse_response("NOTIFY * HTTP/1.1\r\nNT: upnp:rootdevice\r\n\r\n"))
  h.assert_nil(discovery.parse_response("HTTP/1.1 404 Not Found\r\n\r\n"))
  h.assert_nil(discovery.parse_response("HTTP/1.1 200 OK\r\nUSN: uuid:x::y\r\n\r\n"),
    "no LOCATION, nothing to fetch")
  h.assert_nil(discovery.parse_response(nil))
end

function T.test_location_address()
  local ip, port = discovery.location_address("http://192.168.1.20:5001/st/v1/description")
  h.assert_equal(ip, "192.168.1.20")
  h.assert_equal(port, 5001)
end

function T.test_a_search_fetches_the_description_of_every_responder()
  local deps = search_deps({
    { data = ssdp_response("192.168.1.20", 5001, "guid-a"), from = "192.168.1.20" },
    { data = ssdp_response("192.168.1.21", 5002, "guid-b"), from = "192.168.1.21" },
  }, {
    ["http://192.168.1.20:5001/st/v1/description"] = description("guid-a", "DESKTOP-ABC", 5001),
    ["http://192.168.1.21:5002/st/v1/description"] = description("guid-b", "DESKTOP-XYZ", 5002),
  })
  local found = discovery.ssdp_search(4, deps)
  h.assert_equal(#found, 2)
  h.assert_equal(found[1].machine_id, "guid-a")
  h.assert_equal(found[1].ip, "192.168.1.20")
  h.assert_equal(found[1].hostname, "DESKTOP-ABC")
  h.assert_equal(found[2].port, 5002)
  h.assert_true(found[1].secret_set)
  -- The M-SEARCH went to the multicast group.
  h.assert_equal(deps.socket.sent[1].ip, "239.255.255.250")
  h.assert_equal(deps.socket.sent[1].port, 1900)
end

function T.test_a_responder_without_a_description_is_dropped()
  local found = discovery.ssdp_search(4, search_deps({
    { data = ssdp_response("192.168.1.20", 5001, "guid-a"), from = "192.168.1.20" },
  }, {}))
  h.assert_equal(#found, 0)
end

function T.test_two_responses_from_one_pc_are_merged()
  -- A PC with a wired and a wireless NIC answers twice, same machine_id.
  local found = discovery.ssdp_search(4, search_deps({
    { data = ssdp_response("192.168.1.20", 5001, "guid-a"), from = "192.168.1.20" },
    { data = ssdp_response("192.168.1.30", 5001, "guid-a"), from = "192.168.1.30" },
  }, {
    ["http://192.168.1.20:5001/st/v1/description"] = description("guid-a", "DESKTOP-ABC"),
    ["http://192.168.1.30:5001/st/v1/description"] = description("guid-a", "DESKTOP-ABC"),
  }))
  h.assert_equal(#found, 1, "one machine_id is one device")
  h.assert_equal(found[1].ip, "192.168.1.20")
  h.assert_nil(found[1].hostname_conflict)
end

function T.test_a_cloned_machine_id_is_flagged()
  -- §6.5: same MachineGuid, different hostname = an image clone.
  local found = discovery.ssdp_search(4, search_deps({
    { data = ssdp_response("192.168.1.20", 5001, "guid-a"), from = "192.168.1.20" },
    { data = ssdp_response("192.168.1.31", 5001, "guid-a"), from = "192.168.1.31" },
  }, {
    ["http://192.168.1.20:5001/st/v1/description"] = description("guid-a", "DESKTOP-ABC"),
    ["http://192.168.1.31:5001/st/v1/description"] = description("guid-a", "LAPTOP-XYZ"),
  }))
  h.assert_equal(#found, 1)
  h.assert_equal(found[1].hostname_conflict, "LAPTOP-XYZ")
end

--------------------------------------------------------------------------------
-- identity and duplicates (§6.5)
--------------------------------------------------------------------------------

function T.test_machine_id_comes_from_the_field_then_the_dni()
  h.assert_equal(discovery.machine_id_of(pc_device("pc-control-9f3c-guid")), "9f3c-guid")

  local manual = pc_device("pc-control-manual-abc-1")
  h.assert_nil(discovery.machine_id_of(manual), "a manual DNI carries no identity")
  manual:set_field(discovery.MACHINE_FIELD, "9f3c-guid")
  h.assert_equal(discovery.machine_id_of(manual), "9f3c-guid")

  -- The field wins, so a device that was renamed on the service keeps working.
  local renamed = pc_device("pc-control-old-guid")
  renamed:set_field(discovery.MACHINE_FIELD, "new-guid")
  h.assert_equal(discovery.machine_id_of(renamed), "new-guid")
end

function T.test_an_existing_dni_is_never_duplicated()
  local existing = pc_device("pc-control-9f3c-guid", { ipAddress = "" })
  local driver = fake_driver({ existing })
  discovery.handle(driver, {}, function() return true end, search_deps({
    { data = ssdp_response("192.168.1.25", 5001, "9f3c-guid"), from = "192.168.1.25" },
  }, {
    ["http://192.168.1.25:5001/st/v1/description"] = description("9f3c-guid", "DESKTOP-ABC"),
  }))
  h.assert_nil(driver.created, "the machine_id is already here")
  h.assert_equal(existing:get_field(client.IP_FIELD), "192.168.1.25")
  h.assert_equal(existing:get_field(discovery.HOSTNAME_FIELD), "DESKTOP-ABC")
end

function T.test_a_manual_device_is_adopted_by_its_stored_machine_id()
  -- §6.5: the DNI stays `manual-...`, the device is not duplicated.
  local manual = pc_device("pc-control-manual-abc-1", { ipAddress = "" })
  manual:set_field(discovery.MACHINE_FIELD, "9f3c-guid")
  local driver = fake_driver({ manual })
  discovery.handle(driver, {}, function() return true end, search_deps({
    { data = ssdp_response("192.168.1.25", 5001, "9f3c-guid"), from = "192.168.1.25" },
  }, {
    ["http://192.168.1.25:5001/st/v1/description"] = description("9f3c-guid", "DESKTOP-ABC"),
  }))
  h.assert_nil(driver.created)
  h.assert_equal(manual.device_network_id, "pc-control-manual-abc-1", "the DNI never changes")
  h.assert_equal(manual:get_field(client.IP_FIELD), "192.168.1.25")
end

function T.test_a_new_machine_id_is_created()
  local existing = pc_device("pc-control-guid-a", { ipAddress = "192.168.1.20" })
  local driver = fake_driver({ existing })
  discovery.handle(driver, {}, function() return true end, search_deps({
    { data = ssdp_response("192.168.1.25", 5001, "guid-b"), from = "192.168.1.25" },
  }, {
    ["http://192.168.1.25:5001/st/v1/description"] = description("guid-b", "DESKTOP-XYZ"),
  }))
  h.assert_equal(#driver.created, 1)
  h.assert_equal(driver.created[1].device_network_id, "pc-control-guid-b")
  h.assert_equal(driver.created[1].label, "DESKTOP-XYZ 컴퓨터")
end

--------------------------------------------------------------------------------
-- followDiscovery / ipAddress (§6.5)
--------------------------------------------------------------------------------

function T.test_follow_discovery_updates_the_ip_and_repolls()
  local device = pc_device("pc-control-9f3c-guid", { ipAddress = "" })
  device:set_field(client.IP_FIELD, "192.168.1.20")
  local plan = discovery.plan(device, { machine_id = "9f3c-guid", ip = "192.168.1.25",
    port = 5001, hostname = "DESKTOP-ABC" })
  h.assert_equal(plan.action, "update")
  h.assert_equal(plan.ip, "192.168.1.25")
  h.assert_true(plan.repoll)
end

function T.test_an_unchanged_ip_does_not_repoll()
  local device = pc_device("pc-control-9f3c-guid", { ipAddress = "" })
  device:set_field(client.IP_FIELD, "192.168.1.20")
  local plan = discovery.plan(device, { machine_id = "9f3c-guid", ip = "192.168.1.20" })
  h.assert_nil(plan.ip)
  h.assert_false(plan.repoll)
end

function T.test_follow_discovery_off_pins_the_address()
  local device = pc_device("pc-control-9f3c-guid", { ipAddress = "", followDiscovery = false })
  device:set_field(client.IP_FIELD, "192.168.1.20")
  local plan = discovery.plan(device, { machine_id = "9f3c-guid", ip = "192.168.1.25",
    hostname = "DESKTOP-ABC" })
  h.assert_nil(plan.ip, "followDiscovery = false keeps the address it has")
  h.assert_false(plan.repoll)
  h.assert_equal(plan.hostname, "DESKTOP-ABC", "the hostname is still learned")
end

function T.test_the_ip_preference_always_wins()
  -- §6.5: a filled-in ipAddress means the user declared a fixed address.
  local device = pc_device("pc-control-9f3c-guid", { ipAddress = "192.168.1.20" })
  local plan = discovery.plan(device, { machine_id = "9f3c-guid", ip = "192.168.1.25" })
  h.assert_nil(plan.ip)
  h.assert_equal(client.device_base_url(device), "http://192.168.1.20:5001/st/v1")

  -- And the preference still wins over a field SSDP wrote earlier.
  device:set_field(client.IP_FIELD, "192.168.1.25")
  h.assert_equal(client.device_base_url(device), "http://192.168.1.20:5001/st/v1")
end

function T.test_a_hostname_change_on_one_machine_id_warns()
  local device = pc_device("pc-control-9f3c-guid", { ipAddress = "" })
  device:set_field(discovery.HOSTNAME_FIELD, "DESKTOP-ABC")
  local plan = discovery.plan(device, { machine_id = "9f3c-guid", hostname = "LAPTOP-XYZ" })
  h.assert_equal(plan.warning, "hostname_mismatch")
  h.assert_equal(plan.conflict, "LAPTOP-XYZ")
end

function T.test_the_hostname_warning_reaches_the_status_message()
  local device = pc_device("pc-control-9f3c-guid", { ipAddress = "", language = "en" })
  device:set_field(discovery.HOSTNAME_FIELD, "DESKTOP-ABC")
  discovery.apply(fake_driver({ device }), device,
    { machine_id = "9f3c-guid", hostname = "LAPTOP-XYZ" }, {})
  local message = h.event_value(h.emitted(device), caps.STATUS, "message")
  h.assert_contains(message, "LAPTOP-XYZ")
  h.assert_contains(message, "machine_id")
end

function T.test_a_plan_for_an_unknown_machine_id_creates()
  local plan = discovery.plan(nil, { machine_id = "guid-b", ip = "192.168.1.25",
    hostname = "DESKTOP-XYZ" })
  h.assert_equal(plan.action, "create")
  h.assert_equal(plan.device_network_id, "pc-control-guid-b")
  h.assert_equal(plan.label, "DESKTOP-XYZ 컴퓨터")
end

--------------------------------------------------------------------------------
-- the targeted re-search (§6.5)
--------------------------------------------------------------------------------

function T.test_a_re_search_is_rate_limited_to_once_per_five_minutes()
  h.assert_true(discovery.should_search(nil, 1000), "never searched before")
  h.assert_false(discovery.should_search(1000, 1299))
  h.assert_true(discovery.should_search(1000, 1300), "five minutes later")
  h.assert_equal(discovery.SEARCH_COOLDOWN, 300)
end

function T.test_an_unreachable_device_searches_once_then_waits()
  local device = pc_device("pc-control-9f3c-guid", { ipAddress = "" })
  device:set_field(client.IP_FIELD, "192.168.1.20")
  local driver = fake_driver({ device })

  local searches = 0
  local deps = {
    now = function() return 1000 end,
    search = function()
      searches = searches + 1
      return { { machine_id = "9f3c-guid", ip = "192.168.1.25", port = 5001,
        hostname = "DESKTOP-ABC" } }
    end,
    -- The immediate re-poll after the address moved.
    http = function(req)
      req.sink('{"protocol":1,"service_version":"v1.1.0","machine_id":"9f3c-guid",'
        .. '"hostname":"DESKTOP-ABC","secret_set":true,"wol":{"ready":true},'
        .. '"update":{"available":false},"schedule":{"active":false},'
        .. '"session":{"exposed":false},"display":"unknown"}')
      return 1, 200, {}, "HTTP/1.1 200 OK"
    end,
  }

  h.assert_true(discovery.refresh(driver, device, deps))
  h.assert_equal(searches, 1)
  h.assert_equal(device:get_field(client.IP_FIELD), "192.168.1.25")

  local ok, why = discovery.refresh(driver, device, deps)
  h.assert_false(ok)
  h.assert_equal(why, "cooldown")
  h.assert_equal(searches, 1, "the second attempt inside five minutes does nothing")
end

function T.test_a_device_without_an_identity_does_not_search()
  local manual = pc_device("pc-control-manual-abc-1", { ipAddress = "192.168.1.20" })
  local ok, why = discovery.refresh(fake_driver({ manual }), manual, { now = function() return 1 end })
  h.assert_false(ok)
  h.assert_equal(why, "no machine id")
  h.assert_nil(manual:get_field(discovery.LAST_SEARCH_FIELD),
    "a search that cannot happen does not burn the cooldown")
end

return T
