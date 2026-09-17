local h = require "helpers"
local Driver = require "st.driver"
local discovery = require "discovery"

local T = {}

local function fake_driver(devices)
  local driver = Driver("test", {})
  driver.devices = devices or {}
  return driver
end

function T.test_manual_add_creates_a_placeholder_device()
  local driver = fake_driver()
  discovery.handle(driver, {}, function() return true end)
  h.assert_equal(#driver.created, 1)
  local spec = driver.created[1]
  h.assert_equal(spec.label, "PC Control (set IP in settings)")
  h.assert_equal(spec.profile_reference, "pc.v1")
  h.assert_equal(spec.type, "LAN")
  h.assert_contains(spec.device_network_id, "pc-control-manual-")
end

function T.test_scan_does_not_pile_up_blank_devices()
  local blank = h.fake_device({ ipAddress = "" })
  local driver = fake_driver({ blank })
  discovery.handle(driver, {}, function() return true end)
  h.assert_nil(driver.created, "a device still awaiting its IP already exists")
end

function T.test_scan_adds_another_device_once_the_first_is_configured()
  local configured = h.fake_device({ ipAddress = "192.168.1.20" })
  local driver = fake_driver({ configured })
  discovery.handle(driver, {}, function() return true end)
  h.assert_equal(#driver.created, 1, "a second PC can be added")
end

function T.test_network_ids_are_unique()
  local first = discovery.network_id(nil)
  local second = discovery.network_id(nil)
  h.assert_true(first ~= second, "two manual ids must differ")
end

function T.test_network_id_prefers_the_machine_id()
  -- #73: an SSDP hit carries machine_id, so the device survives an IP change.
  h.assert_equal(discovery.network_id("9f3c-guid"), "pc-control-9f3c-guid")
end

function T.test_ssdp_search_is_still_a_stub()
  -- #73 replaces this; the discovery handler already loops over its result.
  h.assert_deep_equal(discovery.ssdp_search(1), {})
end

function T.test_a_discovered_host_is_labelled_with_its_hostname()
  local driver = fake_driver()
  discovery.create(driver, { ip = "192.168.1.20", hostname = "DESKTOP-ABC", machine_id = "guid" })
  h.assert_equal(driver.created[1].label, "DESKTOP-ABC")
  h.assert_equal(driver.created[1].device_network_id, "pc-control-guid")
end

return T
