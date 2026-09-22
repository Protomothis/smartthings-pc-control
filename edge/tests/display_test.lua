-- The display child device (§5.2, §13.1). The decisions are pure functions, so
-- create/relabel/delete is asserted without a hub; the two glue functions are
-- driven with the st.driver mock and an injected http.

local h = require "helpers"
local Driver = require "st.driver"
local caps = require "caps"
local discovery = require "discovery"
local display = require "display"
local json = require "st.json"
local state = require "state"

local T = {}

local PREFS = { ipAddress = "192.168.1.20", port = 5001, secret = "s3cret" }

local function fake_driver(devices)
  local driver = Driver("test", {})
  driver.devices = devices or {}
  return driver
end

local function parent_device(prefs, machine_id, hostname)
  local device = h.fake_device(prefs or PREFS)
  device.device_network_id = discovery.DNI_PREFIX .. (machine_id or "9f3c-guid")
  device.id = "parent-1"
  if hostname then
    device:set_field(discovery.HOSTNAME_FIELD, hostname)
  end
  return device
end

local function child_device(machine_id, label)
  local device = h.fake_device({})
  device.device_network_id = display.dni(machine_id or "9f3c-guid")
  device.id = "child-1"
  device.label = label
  device.parent_assigned_child_key = display.CHILD_KEY
  function device:try_delete_device()
    self.deleted = true
    return true
  end
  return device
end

--------------------------------------------------------------------------------
-- pure decisions
--------------------------------------------------------------------------------

function T.test_the_child_dni_derives_from_the_machine_id()
  h.assert_equal(display.dni("9f3c-guid"), "pc-control-9f3c-guid-display")
  h.assert_nil(display.dni(nil))
  h.assert_nil(display.dni(""))
end

function T.test_is_child_recognises_the_suffix()
  h.assert_true(display.is_child(child_device()))
  h.assert_false(display.is_child(parent_device()))
  h.assert_false(display.is_child({}))
end

function T.test_the_label_is_the_hostname_plus_monitor()
  h.assert_equal(display.label("DESKTOP-ABC", "en"), "DESKTOP-ABC Monitor")
  -- auto resolves to Korean (§6.5)
  h.assert_equal(display.label("DESKTOP-ABC", "auto"), "DESKTOP-ABC 모니터")
  h.assert_equal(display.label("DESKTOP-ABC", "ko"), "DESKTOP-ABC 모니터")
  h.assert_equal(display.label(nil, "en"), "PC Monitor")
end

function T.test_the_child_is_wanted_while_the_preference_is_on()
  h.assert_true(display.wanted({ createDisplayDevice = true }, "9f3c-guid"))
  h.assert_true(display.wanted({}, "9f3c-guid"), "the preference defaults to true")
  h.assert_false(display.wanted({ createDisplayDevice = false }, "9f3c-guid"))
  -- Without a machine_id there is no stable DNI for the child (§13.1), so it
  -- waits for the first successful status.
  h.assert_false(display.wanted({}, nil))
  h.assert_false(display.wanted({}, ""))
end

function T.test_plan_creates_when_wanted_and_absent()
  local plan = display.plan({ wanted = true, exists = false, machine_id = "9f3c-guid",
    hostname = "DESKTOP-ABC", lang = "en" })
  h.assert_equal(plan.action, "create")
  h.assert_equal(plan.device_network_id, "pc-control-9f3c-guid-display")
  h.assert_equal(plan.label, "DESKTOP-ABC Monitor")
end

function T.test_plan_deletes_when_the_preference_goes_off()
  local plan = display.plan({ wanted = false, exists = true, machine_id = "9f3c-guid" })
  h.assert_equal(plan.action, "delete")
end

function T.test_plan_does_nothing_when_it_already_matches()
  h.assert_equal(display.plan({ wanted = false, exists = false }).action, "none")
  h.assert_equal(display.plan({ wanted = true, exists = true, machine_id = "9f3c-guid",
    hostname = "DESKTOP-ABC", lang = "en", current_label = "DESKTOP-ABC Monitor" }).action, "none")
end

function T.test_plan_renames_only_a_label_the_user_did_not_touch()
  -- §13.1: the PC was renamed, and the child still carries the old name.
  local plan = display.plan({ wanted = true, exists = true, machine_id = "9f3c-guid",
    hostname = "DESKTOP-NEW", previous_hostname = "DESKTOP-ABC", lang = "en",
    current_label = "DESKTOP-ABC Monitor" })
  h.assert_equal(plan.action, "relabel")
  h.assert_equal(plan.label, "DESKTOP-NEW Monitor")

  -- A label the user chose is never overwritten.
  local kept = display.plan({ wanted = true, exists = true, machine_id = "9f3c-guid",
    hostname = "DESKTOP-NEW", previous_hostname = "DESKTOP-ABC", lang = "en",
    current_label = "Living room screen" })
  h.assert_equal(kept.action, "none")
end

function T.test_the_switch_follows_status_display()
  h.assert_equal(display.switch_for("on"), "on")
  h.assert_equal(display.switch_for("off"), "off")
  -- §5.2: "unknown" leaves the switch showing whatever it last showed.
  h.assert_nil(display.switch_for("unknown"))
  h.assert_nil(display.switch_for(nil))
end

function T.test_the_commands_are_the_screen_commands()
  h.assert_equal(display.command_for("on"), "turnscreenon")
  h.assert_equal(display.command_for("off"), "turnscreenoff")
  h.assert_nil(display.command_for("maybe"))
end

--------------------------------------------------------------------------------
-- driver glue
--------------------------------------------------------------------------------

function T.test_ensure_creates_the_child_once_the_pc_has_answered()
  local parent = parent_device(PREFS, "9f3c-guid", "DESKTOP-ABC")
  local driver = fake_driver({ parent })
  local plan = display.ensure(driver, parent)
  h.assert_equal(plan.action, "create")
  local spec = driver.created[1]
  -- EDGE_CHILD must not carry a device_network_id (hub warning); the
  -- parent_assigned_child_key identifies it.
  h.assert_equal(spec.device_network_id, nil)
  h.assert_equal(spec.label, "DESKTOP-ABC 모니터")
  h.assert_equal(spec.parent_device_id, "parent-1")
  h.assert_equal(spec.profile, "pc-display.v3")
  h.assert_equal(spec.type, "EDGE_CHILD")
end

function T.test_ensure_waits_for_a_machine_id()
  local parent = h.fake_device(PREFS)
  parent.device_network_id = "pc-control-manual-abc-1"
  parent.id = "parent-1"
  local driver = fake_driver({ parent })
  h.assert_equal(display.ensure(driver, parent).action, "none")
  h.assert_nil(driver.created)
end

function T.test_ensure_removes_the_child_when_the_preference_goes_off()
  local prefs = { ipAddress = "192.168.1.20", createDisplayDevice = false }
  local parent = parent_device(prefs, "9f3c-guid", "DESKTOP-ABC")
  local child = child_device("9f3c-guid", "DESKTOP-ABC Monitor")
  local driver = fake_driver({ parent, child })
  h.assert_equal(display.ensure(driver, parent).action, "delete")
  h.assert_true(child.deleted)
end

function T.test_the_first_status_creates_the_child_for_a_manual_device()
  -- A manually added device has no identity until the PC answers (§13.1), so
  -- this is the moment its child can exist.
  local poll = require "poll"
  local parent = h.fake_device(PREFS)
  parent.device_network_id = "pc-control-manual-abc-1"
  parent.id = "parent-1"
  local driver = fake_driver({ parent })

  local http = function(req)
    req.sink('{"protocol":1,"service_version":"v1.1.0","machine_id":"9f3c-guid",'
      .. '"hostname":"DESKTOP-ABC","secret_set":true,"wol":{"ready":true},'
      .. '"update":{"available":false},"schedule":{"active":false},'
      .. '"session":{"exposed":false},"display":"on"}')
    return 1, 200, {}, "HTTP/1.1 200 OK"
  end
  h.assert_true(poll.once(driver, parent, { deps = { http = http } }))
  h.assert_equal(driver.created[1].device_network_id, nil)
  h.assert_equal(driver.created[1].parent_assigned_child_key, display.CHILD_KEY)
  h.assert_equal(driver.created[1].label, "DESKTOP-ABC 모니터")
end

function T.test_ensure_does_nothing_for_a_child()
  local driver = fake_driver({})
  h.assert_equal(display.ensure(driver, child_device()).action, "none")
  h.assert_nil(driver.created)
end

function T.test_sync_emits_the_switch_on_the_child()
  local parent = parent_device(PREFS, "9f3c-guid", "DESKTOP-ABC")
  local child = child_device("9f3c-guid")
  local driver = fake_driver({ parent, child })

  h.assert_true(display.sync(driver, parent, { display = "off" }))
  h.assert_equal(h.event_value(h.emitted(child), state.CAP_SWITCH, "switch"), "off")
  h.assert_equal(#parent.emitted, 0, "the PC's own switch is the power switch")

  -- The same value again is not re-emitted, and "unknown" changes nothing.
  h.assert_false(display.sync(driver, parent, { display = "off" }))
  h.assert_false(display.sync(driver, parent, { display = "unknown" }))
  h.assert_equal(#child.emitted, 1)
end

function T.test_sync_without_a_child_is_harmless()
  local parent = parent_device(PREFS, "9f3c-guid", "DESKTOP-ABC")
  h.assert_false(display.sync(fake_driver({ parent }), parent, { display = "on" }))
end

function T.test_switch_on_the_child_runs_turnscreenon_on_the_parent()
  local parent = parent_device(PREFS, "9f3c-guid", "DESKTOP-ABC")
  local child = child_device("9f3c-guid")
  local driver = fake_driver({ parent, child })

  local requests = {}
  local http = function(req)
    local body
    if req.source then
      local parts = {}
      while true do
        local chunk = req.source()
        if not chunk then
          break
        end
        parts[#parts + 1] = chunk
      end
      body = table.concat(parts)
    end
    requests[#requests + 1] = { url = req.url, method = req.method, body = body }
    req.sink('{"protocol":1,"service_version":"v1.1.0","machine_id":"9f3c-guid",'
      .. '"secret_set":true,"wol":{"ready":true},"update":{"available":false},'
      .. '"schedule":{"active":false},"session":{"exposed":false},"display":"on"}')
    return 1, 200, {}, "HTTP/1.1 200 OK"
  end

  h.assert_true(display.handle_switch(driver, child, "on", { http = http }))
  h.assert_equal(requests[1].url, "http://192.168.1.20:5001/st/v1/command")
  local sent = json.decode(requests[1].body)
  h.assert_equal(sent.command, "turnscreenon")
  -- §5.2: the screen commands have no grace period to wait out.
  h.assert_equal(sent.mode, "immediate")
  h.assert_equal(sent.minutes, 0)
  h.assert_equal(h.event_value(h.emitted(child), state.CAP_SWITCH, "switch"), "on")
  -- The parent is re-polled so its `lastCommand` tile catches up.
  h.assert_equal(h.event_value(h.emitted(parent), caps.COMMAND, "lastCommand"), "")
end

function T.test_switch_off_on_the_child_runs_turnscreenoff()
  local parent = parent_device(PREFS, "9f3c-guid", "DESKTOP-ABC")
  local child = child_device("9f3c-guid")
  local driver = fake_driver({ parent, child })
  local body
  local http = function(req)
    local parts = {}
    while true do
      local chunk = req.source()
      if not chunk then
        break
      end
      parts[#parts + 1] = chunk
    end
    body = table.concat(parts)
    req.sink("{}")
    return 1, 200, {}, "HTTP/1.1 200 OK"
  end
  display.handle_switch(driver, child, "off", { http = http })
  h.assert_equal(json.decode(body).command, "turnscreenoff")
end

function T.test_a_child_without_a_parent_does_nothing()
  local child = child_device("9f3c-guid")
  local ok, why = display.handle_switch(fake_driver({ child }), child, "on", {})
  h.assert_false(ok)
  h.assert_equal(why, "no parent")
end

return T
