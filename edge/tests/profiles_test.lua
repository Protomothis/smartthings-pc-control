-- Profile versions and the migration between them (#79, design §6.6).
--
-- The decision is a pure function (`migration_for`), so most of this needs no
-- device at all; the rest drives the real lifecycle handlers of init.lua with
-- the st.driver mock, because the point of the feature is that a device left
-- on an old profile is moved on its first init.
--
-- #90 reset the numbering for the first channel release; #107 made `pc.v2`,
-- pcMessage `pc.v3`, pcNotify `pc.v4` and pcToast `pc.v5`, so the real history now has ten v1
-- names and twenty each of v2, v3 and v4 that migrate (`V1` and `DEV` below - v2,
-- v3 and v4 only ever lived on the Dev channel). Some of
-- the older tests still swap in a pretend two-version history
-- (`with_fake_versions`), which exercises the same machinery without depending
-- on which names happen to be current.

local fields = require "device.fields"
local h = require "helpers"
local discovery = require "discovery"
local profiles = require "profiles"

local T = {}

local function device_on(profile_name, id)
  local device = h.fake_device({})
  device.id = id or ("device-" .. tostring(profile_name))
  if profile_name then
    device:set_field(fields.PROFILE_NAME, profile_name)
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

-- #107: every name edge-v1.0.x shipped, and where each of them goes.
local V1 = {
  ["pc.v1"] = "pc.v6",
  ["pc-monitor.v1"] = "pc-monitor.v6",
  ["pc-switch.v1"] = "pc-switch.v6",
  ["pc-plug.v1"] = "pc-plug.v6",
  ["pc-tv.v1"] = "pc-tv.v6",
  ["pc-projector.v1"] = "pc-projector.v6",
  ["pc-network.v1"] = "pc-network.v6",
  ["pc-hub.v1"] = "pc-hub.v6",
  ["pc-theater.v1"] = "pc-theater.v6",
  ["pc-remote.v1"] = "pc-remote.v6",
}

-- pcToast: every name the three unpublished generations made (#107: ten
-- styles, with and without the battery component; v2 with the standard
-- notification pair, v3 with pcMessage, v4 with pcNotify), and where each of
-- them goes - the same style and the same battery half, on v5.
local DEV = {}
for _, version in ipairs({ 2, 3, 4, 5 }) do
  for _, battery in ipairs({ false, true }) do
    for _, style in ipairs(profiles.STYLES) do
      DEV[profiles.name_for(style, battery, version)] = profiles.name_for(style, battery, 6)
    end
  end
end

function T.test_every_v1_profile_migrates_to_the_current_one_of_its_style()
  -- #107: v2 replaced all ten v1 names at once, then v3, then v4, then v5. A v1 device
  -- goes straight to v5. The icon a device wears survives the move, and until
  -- a status has said "battery" it lands on the plain profile.
  h.assert_equal(profiles.PC, "pc.v6")
  for old, new in pairs(V1) do
    h.assert_equal(profiles.migration_for(old), new, old)
    h.assert_equal(profiles.migration_for(old, false), new, old)
  end
  -- The development names that never left the author's hub stay unknown, and
  -- so does a generation this driver has not shipped yet.
  h.assert_nil(profiles.migration_for("pc.v17"))
  h.assert_nil(profiles.migration_for("pc.v7"))
end

function T.test_every_v2_to_v5_profile_migrates_to_the_v6_of_its_style_and_battery_half()
  -- pcToast: the sixty development names each land on their v5 twin.
  local count = 0
  for old, new in pairs(DEV) do
    count = count + 1
    h.assert_equal(profiles.migration_for(old), new, old)
    h.assert_equal(profiles.migration_for(old, false), new,
      old .. ": the name's battery half wins over a field that says no")
    h.assert_false(profiles.is_current(old), old .. " must not be current any more")
  end
  h.assert_equal(count, 80)
  -- A plain v2/v3 laptop whose statuses already said "battery" goes straight
  -- onto the battery twin.
  h.assert_equal(profiles.migration_for("pc-tv.v2", true), "pc-tv-battery.v6")
  h.assert_equal(profiles.migration_for("pc-tv.v3", true), "pc-tv-battery.v6")
  h.assert_equal(profiles.migration_for("pc-hub-battery.v3"), "pc-hub-battery.v6")
  -- pcToast: and the pcNotify screen the same way.
  h.assert_equal(profiles.migration_for("pc-tv-battery.v4"), "pc-tv-battery.v6")
  h.assert_equal(profiles.migration_for("pc-monitor.v4"), "pc-monitor.v6")
  h.assert_equal(profiles.migration_for("pc.v4", true), "pc-battery.v6")
end

function T.test_a_v1_profile_can_migrate_straight_onto_a_battery_profile()
  -- #107: the caller decides the battery half (a laptop whose status has said
  -- so), the name decides the style.
  h.assert_equal(profiles.migration_for("pc.v1", true), "pc-battery.v6")
  h.assert_equal(profiles.migration_for("pc-tv.v1", true), "pc-tv-battery.v6")
end

function T.test_known_is_every_v1_name_then_every_v2_v3_v4_name_then_every_current_one()
  local expected = {}
  for _, style in ipairs(profiles.STYLES) do
    expected[#expected + 1] = style == "others" and "pc.v1" or ("pc-" .. style .. ".v1")
  end
  for _, version in ipairs({ 2, 3, 4, 5 }) do
    for _, battery in ipairs({ false, true }) do
      for _, style in ipairs(profiles.STYLES) do
        expected[#expected + 1] = profiles.name_for(style, battery, version)
      end
    end
  end
  for _, battery in ipairs({ false, true }) do
    for _, style in ipairs(profiles.STYLES) do
      expected[#expected + 1] = profiles.for_style(style, battery)
    end
  end
  h.assert_deep_equal(profiles.KNOWN, expected)
  h.assert_equal(#profiles.KNOWN, 110)
end

function T.test_only_v1_and_the_current_generation_are_shipped()
  -- v2, v3 and v4 never left the Dev channel, so their files are not in the
  -- package (the 655360-byte limit) - but their names are still KNOWN.
  h.assert_true(profiles.UNSHIPPED_VERSIONS[2] == true)
  h.assert_true(profiles.UNSHIPPED_VERSIONS[3] == true)
  h.assert_true(profiles.UNSHIPPED_VERSIONS[4] == true)
  h.assert_nil(profiles.UNSHIPPED_VERSIONS[1])
  h.assert_true(profiles.UNSHIPPED_VERSIONS[5] == true)
  h.assert_nil(profiles.UNSHIPPED_VERSIONS[6])
  h.assert_true(profiles.is_shipped("pc.v1"))
  h.assert_true(profiles.is_shipped("pc-tv.v1"))
  h.assert_true(profiles.is_shipped(profiles.PC))
  h.assert_true(profiles.is_shipped(profiles.BATTERY))
  for old in pairs(DEV) do
    h.assert_false(profiles.is_shipped(old), old .. " must not be packaged")
  end
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
  h.assert_nil(profiles.migration_for("pc-display.v4"))
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
-- #100: icon styles
--------------------------------------------------------------------------------

function T.test_every_style_maps_to_its_profile_and_back()
  h.assert_equal(profiles.for_style("others"), "pc.v6")
  h.assert_equal(profiles.style_of("pc.v6"), "others")
  local expected = {
    monitor = "pc-monitor.v6", switch = "pc-switch.v6", plug = "pc-plug.v6",
    tv = "pc-tv.v6", projector = "pc-projector.v6", network = "pc-network.v6",
    hub = "pc-hub.v6", theater = "pc-theater.v6", remote = "pc-remote.v6",
  }
  h.assert_deep_equal(profiles.VARIANTS, expected)
  for _, style in ipairs(profiles.STYLES) do
    for _, battery in ipairs({ false, true }) do
      local name = profiles.for_style(style, battery)
      h.assert_equal(profiles.style_of(name), style, "round trip of " .. style)
      h.assert_equal(profiles.battery_of(name), battery, "battery half of " .. name)
      h.assert_true(profiles.is_current(name), name .. " is not current")
      h.assert_true(profiles.CURRENT[name] == true, name .. " is missing from CURRENT")
    end
  end
  local count = 0
  for _ in pairs(profiles.CURRENT) do
    count = count + 1
  end
  h.assert_equal(count, 20, "ten styles, with and without the battery")
end

function T.test_the_battery_variants_are_named_after_the_plain_ones()
  -- #107: `pc-<style>-battery.v5`, and `pc-battery.v5` for the default style.
  h.assert_equal(profiles.BATTERY, "pc-battery.v6")
  h.assert_equal(profiles.for_style("others", true), "pc-battery.v6")
  h.assert_equal(profiles.for_style("tv", true), "pc-tv-battery.v6")
  h.assert_equal(profiles.for_style("bogus", true), "pc-battery.v6")
  h.assert_nil(profiles.battery_of("pc.v1"), "a name that is not current has no battery half")
  h.assert_nil(profiles.battery_of("pc-battery.v1"))
end

function T.test_an_unknown_style_is_the_default()
  h.assert_equal(profiles.for_style(nil), profiles.PC)
  h.assert_equal(profiles.for_style("bogus"), profiles.PC)
  h.assert_equal(profiles.for_style(""), profiles.PC)
  h.assert_equal(profiles.for_style(42), profiles.PC)
end

function T.test_a_name_that_is_not_current_has_no_style()
  h.assert_nil(profiles.style_of("pc.v1"), "#107: v1 is not current any more")
  h.assert_nil(profiles.style_of("pc.v2"), "nor is v2")
  h.assert_nil(profiles.style_of("pc-tv-battery.v2"))
  h.assert_nil(profiles.style_of("pc.v3"), "pcNotify: nor is v3")
  h.assert_nil(profiles.style_of("pc-tv-battery.v3"))
  h.assert_nil(profiles.style_of("pc.v4"), "pcToast: nor is v4")
  h.assert_nil(profiles.style_of("pc-tv-battery.v4"))
  h.assert_nil(profiles.style_of("pc.v5"), "#123: nor is v5")
  h.assert_nil(profiles.style_of("pc-tv-battery.v5"))
  h.assert_nil(profiles.style_of("pc.v7"))
  h.assert_nil(profiles.style_of(profiles.APP), "the app child is no PC profile")
  h.assert_nil(profiles.style_of("pc-display.v1"))
  h.assert_nil(profiles.style_of("thermostat"))
  h.assert_nil(profiles.style_of(nil))
  h.assert_false(profiles.is_current("pc-display.v1"))
  h.assert_false(profiles.is_current(nil))
end

function T.test_no_variant_is_ever_migrated()
  -- Every variant is as current as pc.v1: ensure must not move it back.
  profiles.reset()
  for style, name in pairs(profiles.VARIANTS) do
    h.assert_nil(profiles.migration_for(name), name)
    local device = device_on(name, "variant-" .. style)
    h.assert_nil(profiles.ensure(device))
    h.assert_equal(#device.metadata_updates, 0, name .. " was moved")
    h.assert_equal(device:get_field(fields.PROFILE_NAME), name)
  end
end

function T.test_a_version_bump_keeps_the_style()
  -- The rule of the module header: v2 bumps every variant, every v1 name goes
  -- into KNOWN, and a device lands on the v2 of the style it wore.
  local pc, known, variants, current = profiles.PC, profiles.KNOWN, profiles.VARIANTS, profiles.CURRENT
  profiles.PC = "pc.v2"
  profiles.VARIANTS = { monitor = "pc-monitor.v2" }
  profiles.CURRENT = { ["pc.v2"] = true, ["pc-monitor.v2"] = true }
  profiles.KNOWN = { "pc.v1", "pc-monitor.v1", "pc.v2", "pc-monitor.v2" }
  local ok, err = pcall(function()
    h.assert_equal(profiles.migration_for("pc.v1"), "pc.v2")
    h.assert_equal(profiles.migration_for("pc-monitor.v1"), "pc-monitor.v2")
    h.assert_nil(profiles.migration_for("pc-monitor.v2"))
  end)
  profiles.PC, profiles.KNOWN, profiles.VARIANTS, profiles.CURRENT = pc, known, variants, current
  if not ok then
    error(err, 0)
  end
end

function T.test_apply_style_switches_once_and_remembers()
  profiles.reset()
  local device = device_on(profiles.PC, "styled-pc")
  device.preferences.iconStyle = "monitor"
  h.assert_equal(profiles.apply_style(device), "pc-monitor.v6")
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-monitor.v6" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-monitor.v6")
  h.assert_nil(profiles.apply_style(device), "the same preference asks for nothing")
  h.assert_equal(#device.metadata_updates, 1)
end

function T.test_apply_style_does_not_loop_on_a_hub_that_keeps_the_old_name()
  -- The hub may leave `device.profile.name` on the old name after the switch;
  -- infoChanged then fires again with the same preference. One update only.
  profiles.reset()
  local device = device_on(profiles.PC, "stale-name-pc")
  device.profile = { id = "abc", name = profiles.PC, components = {} }
  device.preferences.iconStyle = "tv"
  function device:try_update_metadata(update)
    self.metadata_updates[#self.metadata_updates + 1] = update
    return true
  end
  h.assert_equal(profiles.apply_style(device), "pc-tv.v6")
  h.assert_nil(profiles.apply_style(device))
  h.assert_nil(profiles.apply_style(device))
  h.assert_equal(#device.metadata_updates, 1)
  -- A real change of mind still goes through, and back again.
  device.preferences.iconStyle = "others"
  h.assert_equal(profiles.apply_style(device), profiles.PC)
  h.assert_equal(#device.metadata_updates, 2)
end

function T.test_apply_style_leaves_alone_what_it_does_not_own()
  profiles.reset()
  -- No preference yet: not a request for the default.
  local unset = device_on("pc-hub.v6", "unset-pc")
  h.assert_nil(profiles.apply_style(unset))
  -- A foreign or superseded profile is `ensure`'s business, not the icon's.
  local foreign = device_on("someone-else.v1", "foreign-pc")
  foreign.preferences.iconStyle = "monitor"
  h.assert_nil(profiles.apply_style(foreign))
  h.assert_equal(#unset.metadata_updates + #foreign.metadata_updates, 0)
end

function T.test_apply_style_survives_a_hub_that_refuses_the_update()
  profiles.reset()
  local device = device_on(profiles.PC, "refusing-hub")
  device.preferences.iconStyle = "plug"
  local tries = 0
  function device:try_update_metadata()
    tries = tries + 1
    error("no such profile", 0)
  end
  h.assert_nil(profiles.apply_style(device))
  h.assert_equal(device:get_field(fields.PROFILE_NAME), profiles.PC,
    "a refused switch must not be recorded as done")
  -- Not asked again for the same value in this run...
  h.assert_nil(profiles.apply_style(device))
  h.assert_equal(tries, 1)
  -- ... but a different value is a new request, and so is the old one after it.
  device.preferences.iconStyle = "hub"
  h.assert_nil(profiles.apply_style(device))
  h.assert_equal(tries, 2)
  device.preferences.iconStyle = "plug"
  h.assert_nil(profiles.apply_style(device))
  h.assert_equal(tries, 3)
  -- The next driver start (a fresh run) asks once more.
  profiles.reset()
  h.assert_nil(profiles.apply_style(device))
  h.assert_equal(tries, 4)
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
  local child = device_on("pc-display.v3", "leftover-child")
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
    h.assert_equal(device:get_field(fields.PROFILE_NAME), NEW,
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

function T.test_ensure_moves_every_v1_device_once_and_keeps_its_style()
  -- #107: the real history, no pretend constants.
  profiles.reset()
  for old, new in pairs(V1) do
    local device = device_on(old, "v1-" .. old)
    h.assert_equal(profiles.ensure(device), new, old)
    h.assert_deep_equal(device.metadata_updates, { { profile = new } })
    h.assert_equal(device:get_field(fields.PROFILE_NAME), new)
  end
  -- Neither a current device nor a development name moves.
  for _, name in ipairs({ "pc.v6", "pc-tv-battery.v6", "pc.v17" }) do
    local device = device_on(name, "release-" .. name)
    h.assert_nil(profiles.ensure(device))
    h.assert_equal(#device.metadata_updates, 0)
  end
end

function T.test_ensure_moves_every_v2_v3_and_v4_device_once_and_keeps_style_and_battery()
  -- A laptop on `pc-hub-battery.v2`, `.v3` or `.v4` must not lose its
  -- battery card on the way to v5, even when the persisted battery answer is
  -- missing.
  profiles.reset()
  for old, new in pairs(DEV) do
    local device = device_on(old, "dev-" .. old)
    h.assert_equal(profiles.ensure(device), new, old)
    h.assert_deep_equal(device.metadata_updates, { { profile = new } })
    h.assert_equal(device:get_field(fields.PROFILE_NAME), new)
    h.assert_nil(profiles.ensure(device), "one attempt per device per run")
  end
end

function T.test_an_icon_switch_right_after_a_migration_starts_from_the_new_name()
  -- #107: the hub may keep reporting `pc-tv.v1` on `device.profile` for a
  -- while after the move. `apply_style` in the same init must read the v5 name
  -- `ensure` just asked for, or it would see a non-current name and refuse.
  profiles.reset()
  local device = device_on("pc-tv.v1", "stale-after-migration")
  device.profile = { id = "abc", name = "pc-tv.v1", components = {} }
  function device:try_update_metadata(update)
    self.metadata_updates[#self.metadata_updates + 1] = update
    return true
  end
  device.preferences.iconStyle = "hub"
  h.assert_equal(profiles.ensure(device), "pc-tv.v6")
  h.assert_equal(profiles.apply_style(device), "pc-hub.v6")
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-tv.v6" }, { profile = "pc-hub.v6" } })
end

function T.test_apply_style_keeps_the_battery_component()
  -- #107: a laptop that changes its icon keeps its battery card.
  profiles.reset()
  local device = device_on("pc-battery.v6", "laptop")
  device.preferences.iconStyle = "monitor"
  h.assert_equal(profiles.apply_style(device), "pc-monitor-battery.v6")
  device.preferences.iconStyle = "others"
  h.assert_equal(profiles.apply_style(device), "pc-battery.v6")
end

function T.test_ensure_does_nothing_for_a_current_device()
  profiles.reset()
  local device = device_on(profiles.PC, "current-pc")
  h.assert_nil(profiles.ensure(device))
  h.assert_equal(#device.metadata_updates, 0)
  h.assert_equal(device:get_field(fields.PROFILE_NAME), profiles.PC)
end

function T.test_ensure_leaves_a_foreign_profile_alone()
  profiles.reset()
  local device = device_on("someone-else.v1", "foreign")
  h.assert_nil(profiles.ensure(device))
  h.assert_equal(#device.metadata_updates, 0)
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "someone-else.v1",
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
    h.assert_equal(device:get_field(fields.PROFILE_NAME), OLD)
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
    h.assert_equal(device:get_field(fields.PROFILE_NAME), NEW)
  end)
end

function T.test_init_moves_a_v1_device_to_v6_and_repaints_it()
  -- #107: the same device on the real constants is a v1 device (no name, no
  -- field: LEGACY), so its first init after the update moves it to the current
  -- profile (pc.v5 since pcToast) and paints the new generation of rows.
  profiles.reset()
  local poll = require "poll"
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v1"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-3"
  device.profile = { id = "abc-123", components = { { id = "main" } } }
  device:set_field(fields.ROWS_PAINTED, "1")
  lifecycle().init(fake_driver({ device }), device)
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc.v6" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc.v6")
  h.assert_equal(device:get_field(fields.ROWS_PAINTED), poll.ROWS_VERSION,
    "the rows of the new capabilities start unset and are painted once")
  -- #129: the row generation is the profile generation.
  h.assert_equal(poll.ROWS_VERSION, tostring(profiles.VERSION))
  h.assert_equal(poll.ROWS_VERSION, "6")
end

function T.test_init_moves_a_v2_device_to_v6_and_repaints_it()
  -- A development device still on `pc-battery.v2` with the rows of generation
  -- "2" painted: the first init after the update moves it to `pc-battery.v5`
  -- (the screen with pcToast) and paints generation "5".
  profiles.reset()
  local poll = require "poll"
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v2-laptop"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-5"
  device.profile = { id = "abc-123", name = "pc-battery.v2",
    components = { { id = "main" }, { id = "awake" }, { id = "battery" } } }
  device:set_field(fields.ROWS_PAINTED, "2")
  lifecycle().init(fake_driver({ device }), device)
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-battery.v6" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-battery.v6")
  h.assert_equal(device:get_field(fields.ROWS_PAINTED), "6")
end

function T.test_init_moves_a_v3_device_to_v6_and_repaints_it()
  -- pcNotify: the reviewer's own device. It sits on `pc-tv-battery.v3` (the
  -- screen with pcMessage's two text fields) with generation "3" painted; the
  -- first init after the update moves it to `pc-tv-battery.v5` - same icon,
  -- same battery card, one text field - and paints generation "5".
  profiles.reset()
  local poll = require "poll"
  local device = h.fake_device({ ipAddress = "192.168.1.20", iconStyle = "tv" })
  device.id = "init-pc-v3-laptop"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-6"
  device.profile = { id = "abc-123", name = "pc-tv-battery.v3",
    components = { { id = "main" }, { id = "awake" }, { id = "battery" } } }
  device:set_field(fields.ROWS_PAINTED, "3")
  lifecycle().init(fake_driver({ device }), device)
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-tv-battery.v6" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-tv-battery.v6")
  h.assert_equal(device:get_field(fields.ROWS_PAINTED), "6")
end

function T.test_init_moves_a_v4_device_to_v6_and_paints_the_message_row()
  -- pcToast: a development device on `pc-tv-battery.v4` (pcNotify, whose text
  -- row was bound to nothing and spun into "네트워크 오류") moves to
  -- `pc-tv-battery.v5`, and the new `lastMessage` row gets a value right away
  -- - forced, because the cloud record of the new profile starts empty.
  profiles.reset()
  local poll = require "poll"
  local caps = require "caps"
  local device = h.fake_device({ ipAddress = "192.168.1.20", iconStyle = "tv" })
  device.id = "init-pc-v4-laptop"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-7"
  device.profile = { id = "abc-123", name = "pc-tv-battery.v4",
    components = { { id = "main" }, { id = "awake" }, { id = "battery" } } }
  device:set_field(fields.ROWS_PAINTED, "4")
  lifecycle().init(fake_driver({ device }), device)
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-tv-battery.v6" } })
  h.assert_equal(device:get_field(fields.ROWS_PAINTED), "6")
  local events = h.emitted(device)
  h.assert_equal(h.event_value(events, caps.TOAST, "lastMessage"), "없음")
  h.assert_true(h.event_forced(events, caps.TOAST, "lastMessage"))
end

function T.test_init_moves_a_v5_device_to_v6_keeping_style_and_battery()
  -- #123: the Dev channel device on `pc-hub-battery.v5` (the kind-based
  -- pcActivity row) moves to `pc-hub-battery.v6` - same icon, same battery
  -- card - and the new `pcApps.summary` row is painted at once, forced: the
  -- cloud record of the new profile starts empty.
  profiles.reset()
  local poll = require "poll"
  local caps = require "caps"
  local device = h.fake_device({ ipAddress = "192.168.1.20", iconStyle = "hub" })
  device.id = "init-pc-v5-laptop"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-8"
  device.profile = { id = "abc-123", name = "pc-hub-battery.v5",
    components = { { id = "main" }, { id = "awake" }, { id = "battery" } } }
  device:set_field(fields.ROWS_PAINTED, "5")
  lifecycle().init(fake_driver({ device }), device)
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-hub-battery.v6" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-hub-battery.v6")
  h.assert_equal(device:get_field(fields.ROWS_PAINTED), "6")
  local events = h.emitted(device)
  h.assert_equal(h.event_value(events, caps.APPS, "summary"), "없음")
  h.assert_true(h.event_forced(events, caps.APPS, "summary"))
  -- And the plain twin keeps its half too.
  h.assert_equal(profiles.migration_for("pc-tv.v5"), "pc-tv.v6")
  h.assert_equal(profiles.migration_for("pc.v5", true), "pc-battery.v6")
end

function T.test_init_leaves_a_v6_device_where_it_is()
  profiles.reset()
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v6"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-4"
  device.profile = { id = "abc-123", name = "pc.v6", components = { { id = "main" } } }
  lifecycle().init(fake_driver({ device }), device)
  h.assert_equal(#device.metadata_updates, 0)
  h.assert_equal(device:get_field(fields.PROFILE_NAME), profiles.PC)
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
  h.assert_equal(device:get_field(fields.PROFILE_NAME), profiles.PC)
  h.assert_equal(#device.metadata_updates, 0,
    "a brand new device is already on the current profile")
end

--------------------------------------------------------------------------------
-- #100: the icon preference in the lifecycle handlers
--------------------------------------------------------------------------------

--- Run `fn(repaints)` with `poll.repaint_soon` counting instead of painting,
--- and `poll.start` inert. `repaints` is a list of the devices repainted.
local function counting_repaints(fn)
  local poll = require "poll"
  local original_repaint, original_start = poll.repaint_soon, poll.start
  local repaints = {}
  poll.repaint_soon = function(_driver, device)
    repaints[#repaints + 1] = device
  end
  poll.start = function() end
  local ok, err = pcall(fn, repaints)
  poll.repaint_soon, poll.start = original_repaint, original_start
  if not ok then
    error(err, 0)
  end
end

local function styled_device(id, style)
  local device = h.fake_device({ ipAddress = "192.168.1.20", iconStyle = style })
  device.id = id
  device.device_network_id = discovery.DNI_PREFIX .. "manual-" .. id
  device.profile = { id = "abc-123", name = profiles.PC, components = {} }
  device:set_field(fields.PROFILE_NAME, profiles.PC)
  -- Rows already painted, so any repaint below comes from the icon switch.
  device:set_field(fields.ROWS_PAINTED, (require "poll").ROWS_VERSION)
  return device
end

function T.test_info_changed_switches_the_profile_once_and_repaints()
  profiles.reset()
  counting_repaints(function(repaints)
    local device = styled_device("icon-pc", "projector")
    local driver = fake_driver({ device })
    lifecycle().infoChanged(driver, device, "infoChanged", {})
    h.assert_deep_equal(device.metadata_updates, { { profile = "pc-projector.v6" } })
    h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-projector.v6")
    h.assert_equal(#repaints, 1, "the new profile starts with empty rows")
    -- The switch landing fires infoChanged again: no second update.
    lifecycle().infoChanged(driver, device, "infoChanged", {})
    h.assert_equal(#device.metadata_updates, 1)
    h.assert_equal(#repaints, 2, "the landing is repainted like any profile change")
  end)
end

function T.test_info_changed_without_a_new_style_touches_no_metadata()
  profiles.reset()
  counting_repaints(function(repaints)
    local device = styled_device("plain-pc", "others")
    lifecycle().infoChanged(fake_driver({ device }), device, "infoChanged", {})
    h.assert_equal(#device.metadata_updates, 0)
    -- Any other preference change still repaints, as before #100.
    h.assert_equal(#repaints, 1)
  end)
end

function T.test_info_changed_with_an_unknown_style_goes_back_to_the_default()
  profiles.reset()
  counting_repaints(function()
    local device = styled_device("typo-pc", "others")
    device.profile.name = "pc-hub.v6"
    device.preferences.iconStyle = "sparkly"
    lifecycle().infoChanged(fake_driver({ device }), device, "infoChanged", {})
    h.assert_deep_equal(device.metadata_updates, { { profile = profiles.PC } })
  end)
end

function T.test_init_reconciles_a_style_the_profile_does_not_match()
  -- The preference changed, the driver restarted before the switch landed.
  profiles.reset()
  counting_repaints(function(repaints)
    local device = styled_device("restarted-pc", "theater")
    lifecycle().init(fake_driver({ device }), device)
    h.assert_deep_equal(device.metadata_updates, { { profile = "pc-theater.v6" } })
    h.assert_equal(#repaints, 1)
  end)
end

function T.test_init_leaves_a_matching_style_alone()
  profiles.reset()
  counting_repaints(function(repaints)
    local device = styled_device("settled-pc", "remote")
    device.profile.name = "pc-remote.v6"
    device:set_field(fields.PROFILE_NAME, "pc-remote.v6")
    lifecycle().init(fake_driver({ device }), device)
    h.assert_equal(#device.metadata_updates, 0)
    h.assert_equal(#repaints, 0, "nothing changed, nothing to repaint")
  end)
end

return T
