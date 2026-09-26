-- Profile versions and the migration between them (#79, design §6.6).
--
-- The decision is a pure function (`migration_for`), so most of this needs no
-- device at all; the rest drives the real lifecycle handlers of init.lua with
-- the st.driver mock, because the point of the feature is that a device left
-- on an old profile is moved on its first init.
--
-- #90 reset the numbering for the first channel release: `KNOWN` holds one
-- name, so no real profile name migrates anywhere. The machinery still has to
-- work for the day `pc.v2` ships, so the tests that exercise it swap in a
-- pretend two-version history (`with_fake_versions`) instead of depending on
-- profile files that no longer exist.

local h = require "helpers"
local discovery = require "discovery"
local profiles = require "profiles"

local T = {}

local function device_on(profile_name, id)
  local device = h.fake_device({})
  device.id = id or ("device-" .. tostring(profile_name))
  if profile_name then
    device:set_field(profiles.FIELD, profile_name)
  end
  return device
end

-- The pretend history: `OLD` is a shipped-but-superseded name, `NEW` the
-- current one. Neither has a file in profiles/ and neither ever has to - the
-- migration decision is made from these constants alone.
local OLD, NEW = "pc.test1", "pc.test2"

--- Run `body` as if the driver had shipped two profile versions.
--
-- Restores the constants afterwards even when `body` fails, so one broken
-- expectation cannot leak a fake profile name into the rest of the suite.
local function with_fake_versions(body)
  local pc, known, legacy = profiles.PC, profiles.KNOWN, profiles.LEGACY
  profiles.PC, profiles.KNOWN, profiles.LEGACY = NEW, { OLD, NEW }, OLD
  local ok, err = pcall(body)
  profiles.PC, profiles.KNOWN, profiles.LEGACY = pc, known, legacy
  if not ok then
    error(err, 0)
  end
end

--------------------------------------------------------------------------------
-- pure: migration_for
--------------------------------------------------------------------------------

function T.test_the_profile_constants_are_the_current_version()
  -- discovery must not carry a second copy of the version (§6.6).
  h.assert_equal(discovery.PROFILE, profiles.PC)
  h.assert_equal(profiles.current(), profiles.PC)
end

function T.test_the_first_release_has_nothing_to_migrate()
  -- #90: one known name means `migration_for` answers nil for everything,
  -- including the development names that never left the author's hub.
  h.assert_deep_equal(profiles.KNOWN, { "pc.v1" })
  h.assert_equal(profiles.PC, "pc.v1")
  h.assert_nil(profiles.migration_for(profiles.PC))
  h.assert_nil(profiles.migration_for("pc.v2"))
  h.assert_nil(profiles.migration_for("pc.v17"))
end

function T.test_an_older_profile_migrates_to_the_current_one()
  with_fake_versions(function()
    h.assert_equal(profiles.migration_for(OLD), NEW)
  end)
end

function T.test_the_current_profile_does_not_migrate()
  h.assert_nil(profiles.migration_for(profiles.PC))
  with_fake_versions(function()
    h.assert_nil(profiles.migration_for(NEW))
  end)
end

function T.test_an_unknown_profile_is_left_alone()
  -- Another driver's device, or one from a version newer than this driver.
  h.assert_nil(profiles.migration_for("pc.v99"))
  h.assert_nil(profiles.migration_for("thermostat"))
  h.assert_nil(profiles.migration_for(""))
  h.assert_nil(profiles.migration_for(nil))
  h.assert_nil(profiles.migration_for(42))
  with_fake_versions(function()
    h.assert_nil(profiles.migration_for("pc.v1"),
      "a name outside KNOWN is never ours to move")
  end)
end

function T.test_a_removed_child_profile_is_never_migrated()
  -- #81: the pc-display series is gone. Such a device is deleted, not moved.
  h.assert_nil(profiles.migration_for("pc-display.v1"))
  h.assert_nil(profiles.migration_for("pc-display.v3"))
end

function T.test_every_shipped_profile_name_is_known()
  -- KNOWN is what makes a migration possible at all: the current name has to
  -- be in it, or the next version could not recognise this one.
  local function contains(list, value)
    for _, name in ipairs(list) do
      if name == value then
        return true
      end
    end
    return false
  end
  h.assert_true(contains(profiles.KNOWN, profiles.PC), "KNOWN is missing " .. profiles.PC)
  h.assert_true(contains(profiles.KNOWN, profiles.LEGACY))
end

--------------------------------------------------------------------------------
-- #81: leftover display children
--------------------------------------------------------------------------------

function T.test_a_child_key_marks_a_legacy_child()
  -- What an EDGE_CHILD created by an older driver looks like: no DNI of its
  -- own, identified by the key the parent assigned.
  local child = h.fake_device({})
  child.parent_assigned_child_key = "display"
  h.assert_true(profiles.is_legacy_child(child))
end

function T.test_a_display_profile_marks_a_legacy_child()
  -- The other mark: no child key on this firmware, but the profile name is
  -- from the removed series.
  local child = device_on("pc-display.v2", "leftover-child")
  h.assert_true(profiles.is_legacy_child(child))
  local reported = h.fake_device({})
  reported.profile = { id = "abc-123", name = "pc-display.v1", components = {} }
  h.assert_true(profiles.is_legacy_child(reported))
end

function T.test_a_pc_is_not_a_legacy_child()
  h.assert_false(profiles.is_legacy_child(device_on(profiles.PC, "a-pc")))
  -- A device with no name at all falls back to LEGACY, which is still a PC.
  h.assert_false(profiles.is_legacy_child(device_on(nil, "old-pc")))
  h.assert_false(profiles.is_legacy_child(nil))
  h.assert_false(profiles.is_legacy_child("not a device"))
end

function T.test_a_legacy_child_is_deleted_once()
  profiles.reset()
  local child = device_on("pc-display.v3", "doomed-child")
  child.deleted = 0
  function child:try_delete_device()
    self.deleted = self.deleted + 1
    return true
  end
  h.assert_true(profiles.remove_legacy_child(nil, child))
  h.assert_equal(child.deleted, 1)
  h.assert_false(profiles.remove_legacy_child(nil, child),
    "a second init must not ask the hub again")
  h.assert_equal(child.deleted, 1)
end

function T.test_a_hub_without_the_device_method_falls_back_to_the_driver()
  profiles.reset()
  local child = device_on("pc-display.v1", "stubborn-child")
  local asked = {}
  local driver = { try_delete_device = function(_, id) asked[#asked + 1] = id end }
  h.assert_true(profiles.remove_legacy_child(driver, child))
  h.assert_deep_equal(asked, { "stubborn-child" })
end

function T.test_remove_legacy_child_leaves_a_pc_alone()
  profiles.reset()
  local device = device_on(profiles.PC, "a-real-pc")
  function device:try_delete_device()
    error("the PC must never be deleted", 0)
  end
  h.assert_false(profiles.remove_legacy_child(nil, device))
end

--------------------------------------------------------------------------------
-- reading the name off a device
--------------------------------------------------------------------------------

function T.test_the_hub_reported_name_wins()
  local device = device_on(OLD)
  device.profile = { id = "abc-123", name = NEW, components = {} }
  h.assert_equal(profiles.name_of(device), NEW)
end

function T.test_a_profile_table_without_a_name_falls_back_to_the_field()
  -- What the hub actually gives us (platform notes "프로필과 화면 생성"): id and components, no name.
  local device = device_on(OLD)
  device.profile = { id = "abc-123", components = { { id = "main" } } }
  h.assert_equal(profiles.name_of(device), OLD)
end

function T.test_a_device_with_neither_is_from_before_the_field_existed()
  h.assert_equal(profiles.name_of(device_on(nil)), profiles.LEGACY)
end

--------------------------------------------------------------------------------
-- ensure
--------------------------------------------------------------------------------

function T.test_ensure_moves_an_old_device_and_records_it()
  profiles.reset()
  with_fake_versions(function()
    local device = device_on(OLD, "old-pc")
    h.assert_equal(profiles.ensure(device), NEW)
    h.assert_equal(#device.metadata_updates, 1)
    h.assert_deep_equal(device.metadata_updates[1], { profile = NEW })
    h.assert_equal(device:get_field(profiles.FIELD), NEW,
      "the new profile name has to be persisted, or init would retry forever")
  end)
end

function T.test_ensure_runs_at_most_once_per_device()
  profiles.reset()
  with_fake_versions(function()
    local device = device_on(OLD, "once-only")
    profiles.ensure(device)
    h.assert_nil(profiles.ensure(device), "a second attempt must be a no-op")
    h.assert_equal(#device.metadata_updates, 1)
  end)
end

function T.test_ensure_does_not_move_a_device_at_the_first_release()
  -- #90: with one known name every device is already where it belongs, so no
  -- install from the channel ever sees a `try_update_metadata` call.
  profiles.reset()
  for _, name in ipairs({ "pc.v1", "pc.v2", "pc.v17" }) do
    local device = device_on(name, "release-" .. name)
    h.assert_nil(profiles.ensure(device))
    h.assert_equal(#device.metadata_updates, 0)
  end
end

function T.test_ensure_does_nothing_for_a_current_device()
  profiles.reset()
  local device = device_on(profiles.PC, "current-pc")
  h.assert_nil(profiles.ensure(device))
  h.assert_equal(#device.metadata_updates, 0)
  h.assert_equal(device:get_field(profiles.FIELD), profiles.PC)
end

function T.test_ensure_leaves_a_foreign_profile_alone()
  profiles.reset()
  local device = device_on("someone-else.v1", "foreign")
  h.assert_nil(profiles.ensure(device))
  h.assert_equal(#device.metadata_updates, 0)
  h.assert_equal(device:get_field(profiles.FIELD), "someone-else.v1",
    "a foreign name must not be overwritten with ours")
end

function T.test_ensure_survives_a_hub_that_refuses_the_update()
  profiles.reset()
  with_fake_versions(function()
    local device = device_on(OLD, "grumpy-hub")
    function device:try_update_metadata()
      error("no such profile", 0)
    end
    h.assert_nil(profiles.ensure(device))
    -- The field still says the old name, so the next driver start tries again.
    h.assert_equal(device:get_field(profiles.FIELD), OLD)
  end)
end

--------------------------------------------------------------------------------
-- the lifecycle handlers (init.lua)
--------------------------------------------------------------------------------

local function lifecycle()
  return (require "init").lifecycle_handlers
end

local function fake_driver(devices)
  local driver = require "init"
  driver.devices = devices or {}
  return driver
end

function T.test_init_migrates_a_device_left_on_an_older_profile()
  profiles.reset()
  with_fake_versions(function()
    -- No profile_name field and no name on device.profile: the device falls
    -- back to LEGACY, which in the pretend history is the superseded name.
    local device = h.fake_device({ ipAddress = "192.168.1.20" })
    device.id = "init-pc"
    device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-1"
    device.profile = { id = "abc-123", components = { { id = "main" } } }
    lifecycle().init(fake_driver({ device }), device)
    h.assert_deep_equal(device.metadata_updates[1], { profile = NEW })
    h.assert_equal(device:get_field(profiles.FIELD), NEW)
  end)
end

function T.test_init_leaves_a_first_release_device_where_it_is()
  -- #90: the same device on the real constants is already current, so init
  -- touches no metadata at all.
  profiles.reset()
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v1"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-3"
  device.profile = { id = "abc-123", components = { { id = "main" } } }
  lifecycle().init(fake_driver({ device }), device)
  h.assert_equal(#device.metadata_updates, 0)
  h.assert_equal(device:get_field(profiles.FIELD), profiles.PC)
end

function T.test_init_deletes_a_leftover_display_child()
  -- #81: a child created by an older driver has no profile in the package any
  -- more, so init removes it instead of migrating it.
  profiles.reset()
  local child = h.fake_device({})
  child.id = "init-child"
  child.parent_assigned_child_key = "display"
  child.deleted = 0
  function child:try_delete_device()
    self.deleted = self.deleted + 1
    return true
  end
  lifecycle().init(fake_driver({ child }), child)
  h.assert_equal(child.deleted, 1)
  h.assert_equal(#child.metadata_updates, 0,
    "a device on its way out must not be migrated")
end

function T.test_added_records_the_profile_and_migrates_nothing()
  profiles.reset()
  -- `added` only fires for a device this driver run just created, so it is on
  -- the current profile already.
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "added-pc"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-2"
  lifecycle().added(fake_driver({ device }), device)
  h.assert_equal(device:get_field(profiles.FIELD), profiles.PC)
  h.assert_equal(#device.metadata_updates, 0,
    "a brand new device is already on the current profile")
end

return T
