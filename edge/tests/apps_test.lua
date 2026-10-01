-- #123: one child device per watched app (src/apps.lua).
--
-- The children are driven by the parent's statuses, so most of this calls
-- `apps.sync` with a fake driver whose device list the test fills in the way
-- the hub would after a create (`added`). The lifecycle half goes through the
-- real handlers of init.lua.

local h = require "helpers"
local Driver = require "st.driver"
local apps = require "apps"
local caps = require "caps"
local client = require "client"
local discovery = require "discovery"
local features = require "features"
local json = require "st.json"
local poll = require "poll"
local profiles = require "profiles"
local push = require "push"
local state = require "state"

local init_driver = require "init"

local T = {}

local MACHINE_ID = "4c4c4544-0042-3510-8052-b4c04f4a3732"

--------------------------------------------------------------------------------
-- fixtures
--------------------------------------------------------------------------------

local STEAM = { id = "steam.exe", label = "Steam", running = true }
local OBS = { id = "obs64.exe", label = "OBS", running = false }
local CODE = { id = "code.exe", label = "VS Code", running = false }

local function copy(value)
  if type(value) ~= "table" then
    return value
  end
  local out = {}
  for k, v in pairs(value) do
    out[k] = copy(v)
  end
  return out
end

--- A v1.2.0 status whose watch list is `list` (copied).
local function status_with(list, opts)
  opts = opts or {}
  return {
    protocol = 1, service_version = "v1.2.0", power = "on",
    features = opts.features or { "activity", "awake" },
    activity = { enabled = opts.enabled ~= false, apps = copy(list), top = opts.top or "" },
  }
end

local function new_parent(id)
  local parent = h.fake_device({ ipAddress = "192.168.1.20", secret = "s", language = "ko" })
  parent.id = id or "pc-1"
  parent.device_network_id = discovery.DNI_PREFIX .. MACHINE_ID
  parent:set_field(discovery.MACHINE_FIELD, MACHINE_ID)
  parent:set_field(poll.ROWS_FIELD, poll.ROWS_VERSION)
  poll.set_state(parent, state.new(state.ON))
  return parent
end

--- The child the hub makes out of one `try_create_device` request.
local function child_from(spec, id)
  local child = h.fake_device({})
  child.id = id or ("child-" .. spec.parent_assigned_child_key)
  child.parent_device_id = spec.parent_device_id
  child.parent_assigned_child_key = spec.parent_assigned_child_key
  child.label = spec.label
  child.profile = { id = "profile-uuid", name = spec.profile, components = { main = { id = "main" } } }
  return child
end

--- A driver that holds `parent`, with a clock the test moves.
local function world()
  apps.reset()
  local w = { driver = Driver("apps", {}), parent = new_parent(), now = 1000000 }
  w.driver.devices = { w.parent }
  w.deps = { now = function() return w.now end }
  --- The hub answers every outstanding create: the children join the list.
  function w.deliver()
    local made = {}
    for _, spec in ipairs(w.driver.created or {}) do
      if not spec.delivered then
        spec.delivered = true
        local child = child_from(spec)
        w.driver.devices[#w.driver.devices + 1] = child
        made[#made + 1] = child
      end
    end
    return made
  end
  function w.child(key)
    for _, device in ipairs(w.driver.devices) do
      if device.parent_assigned_child_key == key then
        return device
      end
    end
    return nil
  end
  function w.sync(list, opts)
    local status = status_with(list, opts)
    poll.set_state(w.parent, features.remember(state.new(state.ON), status))
    return apps.sync(w.driver, w.parent, status, w.deps)
  end
  return w
end

local function running_rows(child)
  local out = {}
  for _, e in ipairs(h.emitted(child)) do
    if e.cap == caps.APP and e.attr == "running" then
      out[#out + 1] = { value = e.value, forced = (e.options or {}).state_change == true }
    end
  end
  return out
end

--------------------------------------------------------------------------------
-- creating
--------------------------------------------------------------------------------

function T.test_a_new_app_gets_a_child_device()
  local w = world()
  local plan = w.sync({ STEAM, OBS })
  h.assert_equal(#plan.create, 2)
  h.assert_equal(#w.driver.created, 2)
  local spec = w.driver.created[1]
  h.assert_deep_equal(spec, {
    type = "EDGE_CHILD",
    label = "Steam",
    profile = "pc-app.v1",
    parent_device_id = "pc-1",
    parent_assigned_child_key = "steam.exe",
    manufacturer = "Protomothis",
    model = "PC Control · 4c4c4544",
    vendor_provided_label = "Steam",
  })
  h.assert_nil(spec.device_network_id, "an EDGE_CHILD may not set one (platform notes)")
  for key, value in pairs(spec) do
    h.assert_equal(type(value), "string", key .. ": try_create_device takes strings only")
  end
  -- In priority order.
  h.assert_equal(w.driver.created[2].parent_assigned_child_key, "obs64.exe")
  h.assert_equal(w.driver.created[2].label, "OBS")
end

function T.test_a_create_that_is_still_on_its_way_is_not_asked_for_again()
  local w = world()
  w.sync({ STEAM })
  -- A push and a poll before the hub's `added`: no second Steam.
  w.now = w.now + 10
  w.sync({ STEAM })
  w.now = w.now + 30
  w.sync({ STEAM })
  h.assert_equal(#w.driver.created, 1, "one create per app")
  -- The child arrives; nothing more to create.
  w.deliver()
  w.now = w.now + apps.CREATE_RETRY_SECONDS
  h.assert_equal(#w.sync({ STEAM }).create, 0)
  h.assert_equal(#w.driver.created, 1)
end

function T.test_a_create_the_hub_never_answered_is_asked_again_later()
  local w = world()
  w.sync({ STEAM })
  w.now = w.now + apps.CREATE_RETRY_SECONDS - 1
  w.sync({ STEAM })
  h.assert_equal(#w.driver.created, 1)
  w.now = w.now + 1
  w.sync({ STEAM })
  h.assert_equal(#w.driver.created, 2, "after CREATE_RETRY_SECONDS the request goes out again")
end

function T.test_a_hub_that_refuses_the_create_is_not_asked_on_every_push()
  local w = world()
  local asked = 0
  w.driver.try_create_device = function()
    asked = asked + 1
    error("refused", 0)
  end
  w.sync({ STEAM })
  w.sync({ STEAM })
  h.assert_equal(asked, 1)
end

function T.test_two_children_with_one_key_are_one_too_many()
  local w = world()
  w.sync({ STEAM })
  w.deliver()
  -- A create that was answered twice.
  local twin = child_from(w.driver.created[1], "child-steam-twin")
  w.driver.devices[#w.driver.devices + 1] = twin
  w.sync({ STEAM })
  h.assert_deep_equal(w.driver.deleted, { "child-steam-twin" })
end

--------------------------------------------------------------------------------
-- deleting
--------------------------------------------------------------------------------

function T.test_an_app_that_leaves_the_list_loses_its_child()
  local w = world()
  w.sync({ STEAM, OBS })
  w.deliver()
  local plan = w.sync({ OBS })
  h.assert_equal(#plan.delete, 1)
  h.assert_deep_equal(w.driver.deleted, { "child-steam.exe" })
  h.assert_equal(#w.driver.created, 2, "nothing new to create")
  -- An emptied list deletes the rest.
  w.sync({})
  h.assert_deep_equal(w.driver.deleted, { "child-steam.exe", "child-obs64.exe" })
end

function T.test_a_hub_that_cannot_delete_leaves_the_child_offline_and_says_so()
  local w = world()
  w.sync({ STEAM })
  w.deliver()
  local tries = 0
  w.driver.try_delete_device = function()
    tries = tries + 1
    return nil, "hub does not support device delete functionality"
  end
  w.sync({})
  local child = w.child("steam.exe")
  h.assert_equal(tries, 1)
  h.assert_equal(child.health, "offline")
  h.assert_contains(h.event_value(h.emitted(w.parent), caps.STATUS, "message"), "Steam")
  -- Not asked again in this run.
  w.sync({})
  h.assert_equal(tries, 1)
end

function T.test_nothing_is_created_or_deleted_while_the_list_is_off_or_unknown()
  local w = world()
  w.sync({ STEAM })
  w.deliver()
  local child = w.child("steam.exe")
  local painted = #running_rows(child)
  -- Switched off on the PC: no entries, but not "every app was removed".
  local off = w.sync({}, { enabled = false, features = { "awake" } })
  h.assert_equal(off.mode, features.APPS_OFF)
  -- A service older than v1.2.0.
  local old = apps.sync(w.driver, w.parent, { service_version = "v1.1.2" }, w.deps)
  h.assert_equal(old.mode, features.APPS_OLD)
  -- The device list cannot be read.
  local broken = Driver("broken", {})
  broken.get_devices = function() error("busy", 0) end
  h.assert_equal(#apps.sync(broken, w.parent, status_with({}), w.deps).delete, 0)
  h.assert_nil(w.driver.deleted)
  h.assert_equal(#w.driver.created, 1)
  h.assert_equal(#running_rows(child), painted, "and the child keeps its value")
end

--------------------------------------------------------------------------------
-- painting
--------------------------------------------------------------------------------

function T.test_running_is_sent_only_when_it_flips_and_the_first_is_forced()
  local w = world()
  w.sync({ STEAM })
  w.deliver()
  w.sync({ STEAM })
  local child = w.child("steam.exe")
  h.assert_deep_equal(running_rows(child), { { value = "running", forced = true } },
    "the first emit of a run is forced (poll.FIRST_FIELD)")
  -- The same status again (a push, a poll): nothing.
  w.sync({ STEAM })
  w.sync({ STEAM })
  h.assert_equal(#running_rows(child), 1)
  -- Steam closes.
  w.sync({ { id = "steam.exe", label = "Steam", running = false } })
  w.sync({ { id = "steam.exe", label = "Steam", running = false } })
  h.assert_deep_equal(running_rows(child), {
    { value = "running", forced = true },
    { value = "stopped", forced = false },
  })
  h.assert_equal(child.health, "online")
end

function T.test_each_child_has_its_own_cache_and_the_pc_is_not_told()
  local w = world()
  w.sync({ STEAM, OBS })
  w.deliver()
  w.sync({ STEAM, OBS })
  h.assert_deep_equal(running_rows(w.child("steam.exe")), { { value = "running", forced = true } })
  h.assert_deep_equal(running_rows(w.child("obs64.exe")), { { value = "stopped", forced = true } })
  h.assert_false(h.has_capability(h.emitted(w.parent), caps.APP), "the PC has no pcApp row")
  h.assert_nil(poll.sent_value(w.parent, apps.ROW))
end

function T.test_several_flips_go_out_in_priority_order()
  local w = world()
  w.sync({ CODE, OBS, STEAM })
  w.deliver()
  local order = {}
  for _, key in ipairs({ "code.exe", "obs64.exe", "steam.exe" }) do
    local child = w.child(key)
    child.emit_event = function(_, event)
      order[#order + 1] = key .. "=" .. event.value
    end
  end
  w.sync({ CODE, OBS, STEAM })
  h.assert_deep_equal(order, { "code.exe=stopped", "obs64.exe=stopped", "steam.exe=running" })
end

function T.test_a_renamed_child_keeps_the_users_label()
  local w = world()
  w.sync({ STEAM })
  w.deliver()
  local child = w.child("steam.exe")
  -- The user renamed it in the app; the PC's label changes too.
  child.label = "거실 PC 스팀"
  local plan = w.sync({ { id = "steam.exe", label = "Steam Big Picture", running = true } })
  h.assert_equal(#plan.create, 0)
  h.assert_equal(#plan.delete, 0)
  h.assert_equal(#child.metadata_updates, 0, "a label is never written after creation")
  h.assert_equal(child.label, "거실 PC 스팀")
  h.assert_equal(#w.driver.created, 1)
end

function T.test_a_list_edit_does_not_fake_a_stop()
  -- Right after the list is edited the service lists every app as stopped
  -- until its next scan (#123 contract). A child that shows "running" waits
  -- for a status with an unchanged list before it believes a stop.
  local w = world()
  w.sync({ STEAM, OBS })
  w.deliver()
  w.sync({ STEAM, OBS })
  local child = w.child("steam.exe")
  local placeholder = { { id = "obs64.exe", label = "OBS", running = false },
    { id = "steam.exe", label = "Steam", running = false } }
  local plan = w.sync(placeholder)
  h.assert_deep_equal(plan.held, { "steam.exe" })
  h.assert_equal(#running_rows(child), 1, "the reorder alone is not a stop")
  -- The scan: Steam still runs, nothing to say.
  w.sync({ { id = "obs64.exe", label = "OBS", running = false }, STEAM })
  h.assert_equal(#running_rows(child), 1)
  -- A real stop with the list unchanged goes out.
  w.sync(placeholder)
  h.assert_equal(running_rows(child)[2].value, "stopped")
end

function T.test_an_unreachable_pc_leaves_every_child_where_it_was()
  local w = world()
  w.sync({ STEAM })
  w.deliver()
  w.sync({ STEAM })
  local child = w.child("steam.exe")
  local original = client.get_status
  client.get_status = function() return false, nil, "unreachable" end
  local ok, err = pcall(function()
    for _ = 1, 3 do
      poll.once(w.driver, w.parent, { deps = w.deps })
    end
  end)
  client.get_status = original
  if not ok then
    error(err, 0)
  end
  h.assert_deep_equal(running_rows(child), { { value = "running", forced = true } },
    "no made-up stop while the PC is away")
  h.assert_nil(w.driver.deleted)
end

--------------------------------------------------------------------------------
-- through a real poll and a real push, with the contract fixtures
--------------------------------------------------------------------------------

function T.test_a_poll_of_status_full_creates_the_two_children()
  local w = world()
  local status = h.fixture("status.full.json")
  local original = client.get_status
  client.get_status = function() return true, copy(status), nil end
  local ok, err = pcall(function() poll.once(w.driver, w.parent, { deps = w.deps }) end)
  client.get_status = original
  if not ok then
    error(err, 0)
  end
  h.assert_equal(#w.driver.created, 2)
  h.assert_equal(w.driver.created[1].parent_assigned_child_key, "steam.exe")
  h.assert_equal(w.driver.created[1].label, "Steam")
  h.assert_equal(w.driver.created[2].parent_assigned_child_key, "obs64.exe")
  h.assert_equal(w.driver.created[2].label, "OBS")
  h.assert_equal(h.event_value(h.emitted(w.parent), caps.APPS, "summary"), "Steam 실행 중")
  -- The hub delivers them; `added` paints each from the status just read.
  for _, child in ipairs(w.deliver()) do
    init_driver.lifecycle_handlers.added(w.driver, child)
  end
  h.assert_deep_equal(running_rows(w.child("steam.exe")), { { value = "running", forced = true } })
  h.assert_deep_equal(running_rows(w.child("obs64.exe")), { { value = "stopped", forced = true } })
end

function T.test_the_activity_push_paints_the_children_at_once()
  local w = world()
  w.sync({ STEAM, OBS })
  w.deliver()
  w.sync({ STEAM, OBS })
  local raw = h.read_file(h.FIXTURE_DIR .. "/push.activity.changed.json")
  local delivered = push.deliver(w.driver, raw, { devices = w.driver.devices, now = w.deps.now })
  h.assert_true(delivered)
  h.assert_deep_equal(running_rows(w.child("obs64.exe")), {
    { value = "stopped", forced = true },
    { value = "running", forced = false },
  }, "OBS started: the push says both run")
  h.assert_equal(#running_rows(w.child("steam.exe")), 1, "Steam did not change")
  h.assert_equal(h.last_value(h.emitted(w.parent), nil, caps.APPS, "summary"), "Steam 실행 중 · 외 1개")
  h.assert_equal(json.decode(raw).data.top, "steam.exe")
end

--------------------------------------------------------------------------------
-- the child's lifecycle
--------------------------------------------------------------------------------

local function lifecycle()
  return init_driver.lifecycle_handlers
end

function T.test_a_child_init_runs_none_of_the_pcs_logic()
  local w = world()
  w.sync({ STEAM })
  local child = w.deliver()[1]
  local timers = #w.driver.timers
  lifecycle().init(w.driver, child)
  lifecycle().doConfigure(w.driver, child)
  lifecycle().infoChanged(w.driver, child, "infoChanged", {})
  h.assert_equal(#w.driver.timers, timers, "no poll timer, no repaint for a child")
  h.assert_equal(#child.metadata_updates, 0, "no profile migration, no icon switch")
  h.assert_nil(child:get_field(poll.TIMER_FIELD))
  h.assert_nil(child:get_field(profiles.FIELD))
  h.assert_nil(w.driver.deleted, "an app child is no leftover display child")
  h.assert_equal(child.health, "online")
  h.assert_deep_equal(running_rows(child), { { value = "running", forced = true } },
    "painted from what its PC last said")
end

function T.test_a_child_whose_pc_is_not_known_yet_waits_for_it()
  local w = world()
  local child = child_from({ parent_device_id = "pc-9", parent_assigned_child_key = "steam.exe",
    label = "Steam", profile = "pc-app.v1" })
  w.driver.devices[#w.driver.devices + 1] = child
  lifecycle().init(w.driver, child)
  h.assert_equal(#running_rows(child), 0)
  h.assert_equal(#w.driver.timers, 0)
end

function T.test_refresh_on_a_child_polls_its_pc()
  local w = world()
  w.sync({ STEAM })
  local child = w.deliver()[1]
  local polled
  local original = poll.once
  poll.once = function(_, device) polled = device return true end
  local ok, err = pcall(function()
    init_driver.capability_handlers.refresh.refresh(w.driver, child, { command = "refresh", args = {} })
  end)
  poll.once = original
  if not ok then
    error(err, 0)
  end
  h.assert_equal(polled, w.parent)
end

function T.test_removing_the_pc_removes_its_children()
  local w = world()
  w.sync({ STEAM, OBS })
  w.deliver()
  lifecycle().removed(w.driver, w.parent)
  table.sort(w.driver.deleted)
  h.assert_deep_equal(w.driver.deleted, { "child-obs64.exe", "child-steam.exe" })
  -- And a child's own removal touches nothing else.
  lifecycle().removed(w.driver, w.child("steam.exe"))
  h.assert_equal(#w.driver.deleted, 2)
end

function T.test_children_are_never_mistaken_for_a_pc()
  local w = world()
  w.sync({ STEAM })
  local child = w.deliver()[1]
  -- A child whose DNI happens to look like a PC's.
  child.device_network_id = discovery.DNI_PREFIX .. MACHINE_ID
  h.assert_equal(discovery.find({ child, w.parent }, MACHINE_ID), w.parent)
  h.assert_false(profiles.is_legacy_child(child))
  h.assert_nil(profiles.migration_for(profiles.APP), "pc-app.v1 is not a PC profile to migrate")
  local display = h.fake_device({})
  display.parent_assigned_child_key = "display"
  h.assert_true(profiles.is_legacy_child(display), "#81's display child still is one")
end

return T
