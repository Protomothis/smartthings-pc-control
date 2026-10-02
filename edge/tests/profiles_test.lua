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
  ["pc.v1"] = "pc.v8",
  ["pc-monitor.v1"] = "pc-monitor.v8",
  ["pc-switch.v1"] = "pc-switch.v8",
  ["pc-plug.v1"] = "pc-plug.v8",
  ["pc-tv.v1"] = "pc-tv.v8",
  ["pc-projector.v1"] = "pc-projector.v8",
  ["pc-network.v1"] = "pc-network.v8",
  ["pc-hub.v1"] = "pc-hub.v8",
  ["pc-theater.v1"] = "pc-theater.v8",
  ["pc-remote.v1"] = "pc-remote.v8",
}

-- Every name the six unpublished generations made (#107: ten styles, with
-- and without the battery component; v2 with the standard notification pair,
-- v3 with pcMessage, v4 with pcNotify, v5 with pcActivity, v6 with pcApps and
-- the app children, v7 with the first watch card), and where each of them
-- goes - the same style and the same battery half, on v8 (#123: the watch
-- card whose preview fits).
local DEV = {}
for _, version in ipairs({ 2, 3, 4, 5, 6, 7 }) do
  for _, battery in ipairs({ false, true }) do
    for _, style in ipairs(profiles.STYLES) do
      DEV[profiles.name_for(style, battery, version)] = profiles.name_for(style, battery, 8)
    end
  end
end

function T.test_every_v1_profile_migrates_to_the_current_one_of_its_style()
  -- #107: v2 replaced all ten v1 names at once, then v3 … v8. A v1 device
  -- goes straight to v8. The icon a device wears survives the move, and until
  -- a status has said "battery" it lands on the plain profile.
  h.assert_equal(profiles.PC, "pc.v8")
  for old, new in pairs(V1) do
    h.assert_equal(profiles.migration_for(old), new, old)
    h.assert_equal(profiles.migration_for(old, false), new, old)
  end
  -- The development names that never left the author's hub stay unknown, and
  -- so does a generation this driver has not shipped yet.
  h.assert_nil(profiles.migration_for("pc.v17"))
  h.assert_nil(profiles.migration_for("pc.v9"))
end

function T.test_every_v2_to_v7_profile_migrates_to_the_v8_of_its_style_and_battery_half()
  -- The 120 development names each land on their v8 twin.
  local count = 0
  for old, new in pairs(DEV) do
    count = count + 1
    h.assert_equal(profiles.migration_for(old), new, old)
    h.assert_equal(profiles.migration_for(old, false), new,
      old .. ": the name's battery half wins over a field that says no")
    h.assert_false(profiles.is_current(old), old .. " must not be current any more")
  end
  h.assert_equal(count, 120)
  -- A plain v2/v3 laptop whose statuses already said "battery" goes straight
  -- onto the battery twin.
  h.assert_equal(profiles.migration_for("pc-tv.v2", true), "pc-tv-battery.v8")
  h.assert_equal(profiles.migration_for("pc-tv.v3", true), "pc-tv-battery.v8")
  h.assert_equal(profiles.migration_for("pc-hub-battery.v3"), "pc-hub-battery.v8")
  -- pcToast: and the pcNotify screen the same way.
  h.assert_equal(profiles.migration_for("pc-tv-battery.v4"), "pc-tv-battery.v8")
  h.assert_equal(profiles.migration_for("pc-monitor.v4"), "pc-monitor.v8")
  h.assert_equal(profiles.migration_for("pc.v4", true), "pc-battery.v8")
end

function T.test_a_v1_profile_can_migrate_straight_onto_a_battery_profile()
  -- #107: the caller decides the battery half (a laptop whose status has said
  -- so), the name decides the style.
  h.assert_equal(profiles.migration_for("pc.v1", true), "pc-battery.v8")
  h.assert_equal(profiles.migration_for("pc-tv.v1", true), "pc-tv-battery.v8")
end

function T.test_known_is_every_v1_name_then_every_dev_generation_name_then_every_current_one()
  local expected = {}
  for _, style in ipairs(profiles.STYLES) do
    expected[#expected + 1] = style == "others" and "pc.v1" or ("pc-" .. style .. ".v1")
  end
  for _, version in ipairs({ 2, 3, 4, 5, 6, 7 }) do
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
  h.assert_equal(#profiles.KNOWN, 150)
end

function T.test_only_v1_and_the_current_generation_are_shipped()
  -- v2 to v7 never left the Dev channel, so their files are not in the
  -- package (the 655360-byte limit) - but their names are still KNOWN.
  h.assert_nil(profiles.UNSHIPPED_VERSIONS[1])
  for version = 2, 7 do
    h.assert_true(profiles.UNSHIPPED_VERSIONS[version] == true, "v" .. version)
  end
  h.assert_nil(profiles.UNSHIPPED_VERSIONS[8])
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
  h.assert_equal(profiles.for_style("others"), "pc.v8")
  h.assert_equal(profiles.style_of("pc.v8"), "others")
  local expected = {
    monitor = "pc-monitor.v8", switch = "pc-switch.v8", plug = "pc-plug.v8",
    tv = "pc-tv.v8", projector = "pc-projector.v8", network = "pc-network.v8",
    hub = "pc-hub.v8", theater = "pc-theater.v8", remote = "pc-remote.v8",
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
  h.assert_equal(profiles.BATTERY, "pc-battery.v8")
  h.assert_equal(profiles.for_style("others", true), "pc-battery.v8")
  h.assert_equal(profiles.for_style("tv", true), "pc-tv-battery.v8")
  h.assert_equal(profiles.for_style("bogus", true), "pc-battery.v8")
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
  h.assert_nil(profiles.style_of("pc.v9"))
  h.assert_nil(profiles.style_of("pc.v6"), "#123: nor is v6")
  h.assert_nil(profiles.style_of("pc-tv-battery.v6"))
  h.assert_nil(profiles.style_of("pc.v7"), "nor is v7, the first watch card")
  h.assert_nil(profiles.style_of("pc-tv-battery.v7"))
  h.assert_nil(profiles.style_of("pc-app.v1"), "the app child was no PC profile")
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
  h.assert_equal(profiles.apply_style(device), "pc-monitor.v8")
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-monitor.v8" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-monitor.v8")
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
  h.assert_equal(profiles.apply_style(device), "pc-tv.v8")
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
  local unset = device_on("pc-hub.v8", "unset-pc")
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
-- leftover child devices (#81's display child, #123's app children)
--------------------------------------------------------------------------------

--- A driver whose `try_delete_device` records the ids and answers `answer`
--- (a function of the id: return values, or raise).
local function deleting_driver(devices, answer)
  local driver = { devices = devices or {}, deleted = {} }
  function driver:get_devices()
    return self.devices
  end
  function driver:try_delete_device(id)
    self.deleted[#self.deleted + 1] = id
    if answer then
      return answer(id)
    end
    return true
  end
  return driver
end

local function app_child(key, id)
  local child = h.fake_device({})
  child.id = id or ("child-" .. key)
  child.parent_assigned_child_key = key
  child.parent_device_id = "a-pc"
  child.profile = { id = "pc-app-profile", name = "pc-app.v1", components = { main = { id = "main" } } }
  return child
end

function T.test_a_child_key_marks_a_leftover_child()
  -- What an EDGE_CHILD looks like: no DNI of its own, identified by the key
  -- the parent assigned - "display" (#81) or a process name (#123).
  local child = h.fake_device({})
  child.parent_assigned_child_key = "display"
  h.assert_true(profiles.is_child(child))
  local app = h.fake_device({})
  app.parent_assigned_child_key = "steam.exe"
  h.assert_true(profiles.is_child(app))
end

function T.test_a_child_profile_marks_a_leftover_child()
  -- The other mark: no child key on this firmware, but the profile name is
  -- from a removed series.
  h.assert_true(profiles.is_child(device_on("pc-display.v3", "leftover-display")))
  h.assert_true(profiles.is_child(device_on("pc-app.v1", "leftover-app")))
  local reported = h.fake_device({})
  reported.profile = { id = "abc-123", name = "pc-display.v1", components = {} }
  h.assert_true(profiles.is_child(reported))
  h.assert_true(profiles.is_child_profile("pc-app.v1"))
  h.assert_false(profiles.is_child_profile(profiles.PC))
end

function T.test_a_pc_is_not_a_child()
  h.assert_false(profiles.is_child(device_on(profiles.PC, "a-pc")))
  -- A device with no name at all falls back to LEGACY, which is still a PC.
  h.assert_false(profiles.is_child(device_on(nil, "old-pc")))
  local blank = device_on(profiles.PC, "blank-key")
  blank.parent_assigned_child_key = ""
  h.assert_false(profiles.is_child(blank))
  h.assert_false(profiles.is_child(nil))
  h.assert_false(profiles.is_child("not a device"))
end

function T.test_a_leftover_child_is_deleted_once_through_the_driver()
  profiles.reset()
  local child = app_child("steam.exe")
  local driver = deleting_driver({ child })
  h.assert_true(profiles.remove_child(driver, child))
  h.assert_deep_equal(driver.deleted, { "child-steam.exe" })
  h.assert_false(profiles.remove_child(driver, child), "a second init must not ask the hub again")
  h.assert_equal(profiles.remove_children(driver), 0, "nor the next PC's init")
  h.assert_deep_equal(driver.deleted, { "child-steam.exe" })
end

function T.test_a_child_the_hub_will_not_delete_is_left_and_not_asked_again()
  -- `nil, "<why>"` (a hub without the feature) and a raise are both survived
  -- and logged; the child is asked once per run, like any other.
  for i, answer in ipairs({
    function() return nil, "hub does not support device delete functionality" end,
    function() error("boom", 0) end,
  }) do
    profiles.reset()
    local child = app_child("obs64.exe", "stubborn-" .. i)
    local driver = deleting_driver({ child }, answer)
    h.assert_true(profiles.remove_child(driver, child))
    h.assert_false(profiles.remove_child(driver, child))
    h.assert_equal(#driver.deleted, 1)
  end
  -- No driver at all: nothing to ask, nothing raised.
  profiles.reset()
  h.assert_true(profiles.remove_child(nil, app_child("x.exe")))
end

function T.test_remove_children_deletes_every_leftover_and_no_pc()
  profiles.reset()
  local pc = device_on(profiles.PC, "a-real-pc")
  local display = device_on("pc-display.v1", "old-display")
  local steam, obs = app_child("steam.exe"), app_child("obs64.exe")
  local driver = deleting_driver({ pc, steam, display, obs })
  h.assert_equal(profiles.remove_children(driver), 3)
  h.assert_deep_equal(driver.deleted, { "child-steam.exe", "old-display", "child-obs64.exe" })
  -- A device list that cannot be read is no reason to fail.
  local broken = deleting_driver()
  function broken:get_devices() error("no list", 0) end
  h.assert_equal(profiles.remove_children(broken), 0)
end

function T.test_remove_child_leaves_a_pc_alone()
  profiles.reset()
  local device = device_on(profiles.PC, "a-real-pc")
  local driver = deleting_driver({ device }, function()
    error("the PC must never be deleted", 0)
  end)
  h.assert_false(profiles.remove_child(driver, device))
  h.assert_equal(#driver.deleted, 0)
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
  for _, name in ipairs({ "pc.v8", "pc-tv-battery.v8", "pc.v17" }) do
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
  h.assert_equal(profiles.ensure(device), "pc-tv.v8")
  h.assert_equal(profiles.apply_style(device), "pc-hub.v8")
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc-tv.v8" }, { profile = "pc-hub.v8" } })
end

function T.test_apply_style_keeps_the_battery_component()
  -- #107: a laptop that changes its icon keeps its battery card.
  profiles.reset()
  local device = device_on("pc-battery.v8", "laptop")
  device.preferences.iconStyle = "monitor"
  h.assert_equal(profiles.apply_style(device), "pc-monitor-battery.v8")
  device.preferences.iconStyle = "others"
  h.assert_equal(profiles.apply_style(device), "pc-battery.v8")
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

function T.test_init_moves_a_v1_device_to_v8_and_repaints_it()
  -- #107: the same device on the real constants is a v1 device (no name, no
  -- field: LEGACY), so its first init after the update moves it to the current
  -- profile (pc.v8 since the watch card's preview fix) and paints the new generation of rows.
  profiles.reset()
  local poll = require "poll"
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v1"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-3"
  device.profile = { id = "abc-123", components = { { id = "main" } } }
  device:set_field(fields.ROWS_PAINTED, "1")
  lifecycle().init(fake_driver({ device }), device)
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc.v8" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc.v8")
  h.assert_equal(device:get_field(fields.ROWS_PAINTED), poll.ROWS_VERSION,
    "the rows of the new capabilities start unset and are painted once")
  -- #129: the row generation is the profile generation.
  h.assert_equal(poll.ROWS_VERSION, tostring(profiles.VERSION))
end

-- The Dev channel generations (never packaged, still known): a device on any
-- of them moves to the v8 of its style and battery half on its first init
-- after the update and paints the new row generation. A row the new profile
-- adds is painted at once, forced, because the cloud record of the new
-- profile starts empty: pcToast's `lastMessage` for a v4 device (whose
-- pcNotify text row was bound to nothing and spun into "네트워크 오류"),
-- and the watch card (#123, on its own component) for a v5 one (the
-- kind-based pcActivity row) and a v6 one (pcApps and the app children).
-- A v7 device already has the card, but its new profile's record is empty
-- too, so every slot is painted again - and its style and battery stay.
function T.test_init_moves_a_dev_generation_device_to_v8_keeping_style_and_battery()
  local caps = require "caps"
  for i, c in ipairs({
    { "pc-battery.v2", "others", "pc-battery.v8" },
    -- the reviewer's own device: pcMessage's two text fields
    { "pc-tv-battery.v3", "tv", "pc-tv-battery.v8" },
    { "pc-tv-battery.v4", "tv", "pc-tv-battery.v8", nil, caps.TOAST, "lastMessage", "없음" },
    { "pc-hub-battery.v5", "hub", "pc-hub-battery.v8", "apps", caps.WATCH, "summary", "없음" },
    -- #123: the v6 device of the app-children build; every slot is new too.
    { "pc-plug.v6", "plug", "pc-plug.v8", "apps", caps.WATCH, "names", "없음" },
    { "pc-plug-battery.v6", "plug", "pc-plug-battery.v8", "apps", caps.WATCH, "slotFive", "empty" },
    -- The first watch card (v7, the preview that wrapped): same card, new screen.
    { "pc-monitor.v7", "monitor", "pc-monitor.v8", "apps", caps.WATCH, "slotOne", "empty" },
    { "pc-tv-battery.v7", "tv", "pc-tv-battery.v8", "apps", caps.WATCH, "summary", "없음" },
  }) do
    local from, style, to, component, cap, attr, want = table.unpack(c, 1, 7)
    profiles.reset()
    local device = h.fake_device({ ipAddress = "192.168.1.20", iconStyle = style })
    device.id = "init-" .. from
    device.device_network_id = discovery.DNI_PREFIX .. "manual-dev-" .. i
    device.profile = { id = "abc-123", name = from,
      components = { { id = "main" }, { id = "awake" }, { id = "battery" } } }
    device:set_field(fields.ROWS_PAINTED, from:match("%.v(%d+)$"))
    local driver = fake_driver({ device })
    lifecycle().init(driver, device)
    -- The rest of the spread repaint (emit.paint).
    h.fire_all(driver, "repaint-batch")
    h.assert_deep_equal(device.metadata_updates, { { profile = to } }, from)
    h.assert_equal(device:get_field(fields.PROFILE_NAME), to, from)
    h.assert_equal(device:get_field(fields.ROWS_PAINTED), (require "poll").ROWS_VERSION, from)
    if cap then
      local events = h.emitted(device)
      h.assert_equal(h.component_value(events, component, cap, attr), want, from)
      h.assert_true(h.component_forced(events, component, cap, attr), from)
    end
  end
  -- And the plain twin keeps its half too.
  h.assert_equal(profiles.migration_for("pc-tv.v5"), "pc-tv.v8")
  h.assert_equal(profiles.migration_for("pc.v5", true), "pc-battery.v8")
end

function T.test_init_leaves_a_v8_device_where_it_is()
  profiles.reset()
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v8"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-4"
  device.profile = { id = "abc-123", name = "pc.v8", components = { { id = "main" } } }
  lifecycle().init(fake_driver({ device }), device)
  h.assert_equal(#device.metadata_updates, 0)
  h.assert_equal(device:get_field(fields.PROFILE_NAME), profiles.PC)
end

function T.test_init_deletes_a_leftover_child_and_touches_nothing_else()
  -- #81/#123: a child created by an older build has no profile in the package
  -- any more, so init asks the hub to delete it instead of migrating it, and
  -- no lifecycle event of it - a second init, added, infoChanged, doConfigure,
  -- refresh, removed - takes a PC path (poll, push, emit) or raises.
  for _, key in ipairs({ "display", "steam.exe" }) do
    profiles.reset()
    local child = h.fake_device({})
    child.id = "init-child-" .. key
    child.parent_assigned_child_key = key
    local driver = fake_driver({ child })
    driver.deleted = {}
    driver.timers = {}
    lifecycle().init(driver, child)
    h.assert_deep_equal(driver.deleted, { child.id }, key)
    h.assert_equal(#child.metadata_updates, 0, "a device on its way out must not be migrated")
    lifecycle().init(driver, child)
    lifecycle().added(driver, child)
    lifecycle().infoChanged(driver, child, "infoChanged", {})
    lifecycle().doConfigure(driver, child)
    h.assert_false((require "handlers.power").refresh(driver, child))
    lifecycle().removed(driver, child)
    h.assert_deep_equal(driver.deleted, { child.id }, key .. ": asked once per run")
    h.assert_equal(#h.emitted(child), 0, key .. ": nothing is emitted on a leftover child")
    h.assert_equal(#driver.timers, 0, key .. ": no poll timer for a leftover child")
  end
end

function T.test_a_pc_init_deletes_the_leftover_children_once()
  -- #123: the app children of the v6 build, found from the PC's own init
  -- (their own init may come later, or not at all on an older hub).
  profiles.reset()
  local pc = h.fake_device({ ipAddress = "192.168.1.20" })
  pc.id = "pc-with-children"
  pc.device_network_id = discovery.DNI_PREFIX .. "manual-children"
  pc.profile = { id = "abc-123", name = profiles.PC, components = h.components_for(profiles.PC) }
  local steam = h.fake_device({})
  steam.id = "child-steam"
  steam.parent_assigned_child_key = "steam.exe"
  steam.parent_device_id = pc.id
  local obs = h.fake_device({})
  obs.id = "child-obs"
  obs.profile = { id = "app-profile", name = "pc-app.v1", components = {} }
  local driver = fake_driver({ pc, steam, obs })
  driver.deleted = {}
  -- The hub refuses one of them: logged, not asked again, nothing raised.
  local real = driver.try_delete_device
  function driver:try_delete_device(id)
    real(self, id)
    if id == "child-obs" then
      return nil, "hub does not support device delete functionality"
    end
    return true
  end
  lifecycle().init(driver, pc)
  h.assert_deep_equal(driver.deleted, { "child-steam", "child-obs" })
  lifecycle().init(driver, steam)
  lifecycle().init(driver, obs)
  lifecycle().added(driver, pc)
  h.assert_deep_equal(driver.deleted, { "child-steam", "child-obs" }, "once per child per run")
  driver.try_delete_device = nil
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
    h.assert_deep_equal(device.metadata_updates, { { profile = "pc-projector.v8" } })
    h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc-projector.v8")
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
    device.profile.name = "pc-hub.v8"
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
    h.assert_deep_equal(device.metadata_updates, { { profile = "pc-theater.v8" } })
    h.assert_equal(#repaints, 1)
  end)
end

function T.test_init_leaves_a_matching_style_alone()
  profiles.reset()
  counting_repaints(function(repaints)
    local device = styled_device("settled-pc", "remote")
    device.profile.name = "pc-remote.v8"
    device:set_field(fields.PROFILE_NAME, "pc-remote.v8")
    lifecycle().init(fake_driver({ device }), device)
    h.assert_equal(#device.metadata_updates, 0)
    h.assert_equal(#repaints, 0, "nothing changed, nothing to repaint")
  end)
end

return T
