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
  local current, for_cards = profiles.CURRENT, profiles.for_cards
  profiles.PC, profiles.KNOWN, profiles.LEGACY = NEW, { OLD, NEW }, OLD
  profiles.CURRENT, profiles.for_cards = { [NEW] = true }, function() return NEW end
  local ok, err = pcall(body)
  profiles.PC, profiles.KNOWN, profiles.LEGACY = pc, known, legacy
  profiles.CURRENT, profiles.for_cards = current, for_cards
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

-- #107: every name edge-v1.0.x shipped, and where each of them goes. #136:
-- all of them land on the one current profile; the icon is the user's to
-- pick in the app now.
local V1 = {
  ["pc.v1"] = "pc.v15",
  ["pc-monitor.v1"] = "pc.v15",
  ["pc-switch.v1"] = "pc.v15",
  ["pc-plug.v1"] = "pc.v15",
  ["pc-tv.v1"] = "pc.v15",
  ["pc-projector.v1"] = "pc.v15",
  ["pc-network.v1"] = "pc.v15",
  ["pc-hub.v1"] = "pc.v15",
  ["pc-theater.v1"] = "pc.v15",
  ["pc-remote.v1"] = "pc.v15",
}

-- Every name the seven unpublished generations made (#107: ten styles, with
-- and without the battery component; v2 with the standard notification pair,
-- v3 with pcMessage, v4 with pcNotify, v5 with pcActivity, v6 with pcApps and
-- the app children, v7 with the first watch card, v8 with the card whose
-- preview fits but whose screen was built from the old capability
-- presentation, v9 the same under a fresh name - and the same old screen),
-- and where each of them goes - the same battery half, and the watch list
-- from v7 on (#158: the cards the old profile had).
local DEV = {}
for _, version in ipairs({ 2, 3, 4, 5, 6, 7, 8, 9 }) do
  for _, battery in ipairs({ false, true }) do
    for _, style in ipairs(profiles.STYLES) do
      DEV[profiles.name_for(style, battery, version)] =
        profiles.for_cards({ battery = battery, watch = version >= 7 })
    end
  end
end

function T.test_every_v1_profile_migrates_to_the_current_one_of_its_style()
  -- #107: v2 replaced all ten v1 names at once, then v3 … v15. A v1 device
  -- goes straight to v15 - no watch list (v1 had none), and until a status
  -- has said "battery" the plain profile.
  h.assert_equal(profiles.PC, "pc.v15")
  for old, new in pairs(V1) do
    h.assert_equal(profiles.migration_for(old), new, old)
    h.assert_equal(profiles.migration_for(old, { battery = false }), new, old)
  end
  -- The development names that never left the author's hub stay unknown, and
  -- so does a generation this driver has not shipped yet.
  h.assert_nil(profiles.migration_for("pc.v17"))
  h.assert_nil(profiles.migration_for("pc.v15"))
end

function T.test_every_v2_to_v9_profile_migrates_keeping_its_battery_half_and_watch_card()
  -- The 160 development names each land on their v15 twin.
  local count = 0
  for old, new in pairs(DEV) do
    count = count + 1
    h.assert_equal(profiles.migration_for(old), new, old)
    h.assert_equal(profiles.migration_for(old, { battery = false }), new,
      old .. ": the name's battery half wins over a field that says no")
    h.assert_false(profiles.is_current(old), old .. " must not be current any more")
  end
  h.assert_equal(count, 160)
  -- A plain v2/v3 laptop whose statuses already said "battery" goes straight
  -- onto the battery twin.
  h.assert_equal(profiles.migration_for("pc-tv.v2", { battery = true }), "pc-battery.v15")
  h.assert_equal(profiles.migration_for("pc-tv.v3", { battery = true }), "pc-battery.v15")
  h.assert_equal(profiles.migration_for("pc-hub-battery.v3"), "pc-battery.v15")
  -- pcToast: and the pcNotify screen the same way.
  h.assert_equal(profiles.migration_for("pc-tv-battery.v4"), "pc-battery.v15")
  h.assert_equal(profiles.migration_for("pc-monitor.v4"), "pc.v15", "the style name is no monitor card")
  h.assert_equal(profiles.migration_for("pc.v4", { battery = true }), "pc-battery.v15")
  -- v7 to v9 had the watch card.
  h.assert_equal(profiles.migration_for("pc-monitor.v9"), "pc-watch.v15")
end

function T.test_every_v10_profile_moves_to_v15_with_its_watch_list()
  -- #136: v10 is what edge 1.1.0 published. Its bare name was Others; the
  -- style goes, the battery half stays, and so does the watch list (#158:
  -- a routine may read its slots). v10 stays packaged for the devices that
  -- have not moved yet.
  h.assert_equal(profiles.migration_for("pc.v10"), "pc-watch.v15")
  h.assert_equal(profiles.migration_for("pc-battery.v10"), "pc-watch-battery.v15")
  h.assert_equal(profiles.migration_for("pc-monitor.v10"), "pc-watch.v15")
  h.assert_equal(profiles.migration_for("pc-monitor-battery.v10"), "pc-watch-battery.v15")
  h.assert_equal(profiles.migration_for("pc-tv-battery.v10"), "pc-watch-battery.v15")
  h.assert_equal(profiles.migration_for("pc.v10", { battery = true }), "pc-watch-battery.v15")
  for _, name in ipairs(profiles.KNOWN) do
    h.assert_true(name ~= "pc-others.v10", "v10 never had an others name")
  end
  h.assert_true(profiles.is_shipped("pc.v10"))
  h.assert_true(profiles.is_shipped("pc-tv-battery.v10"))
end

function T.test_the_dev_only_v11_and_v12_move_to_v15_and_drop_their_style()
  -- #136: v11 (monitor as its bare name) and v12 (ten styles, Switch the
  -- default), Dev channel only, land on the current profile like every older
  -- name. Their files are not packaged.
  h.assert_equal(profiles.migration_for("pc.v11"), "pc-watch.v15")
  h.assert_equal(profiles.migration_for("pc-battery.v11"), "pc-watch-battery.v15")
  h.assert_equal(profiles.migration_for("pc-others.v11"), "pc-watch.v15")
  h.assert_equal(profiles.migration_for("pc.v12"), "pc-watch.v15")
  h.assert_equal(profiles.migration_for("pc-monitor-battery.v12"), "pc-watch-battery.v15")
  h.assert_equal(profiles.migration_for("pc-others.v12", { battery = true }), "pc-watch-battery.v15")
  h.assert_false(profiles.is_shipped("pc.v11"))
  h.assert_false(profiles.is_shipped("pc.v12"))
end

function T.test_the_published_v13_moves_to_v15_keeping_its_cards()
  -- v13 (edge 1.2.x, two names, Switch) is published, so its files stay in
  -- the package. #158: it always had the watch list, so a device lands on a
  -- `-watch` profile until this driver's statuses have settled the card.
  h.assert_equal(profiles.migration_for("pc.v13"), "pc-watch.v15")
  h.assert_equal(profiles.migration_for("pc.v13", { battery = true }), "pc-watch-battery.v15")
  h.assert_equal(profiles.migration_for("pc-battery.v13"), "pc-watch-battery.v15")
  h.assert_equal(profiles.migration_for("pc-battery.v13", { battery = false }), "pc-watch-battery.v15")
  h.assert_nil(profiles.migration_for("pc-tv.v13"), "v13 had no styled names")
  h.assert_true(profiles.is_shipped("pc.v13"))
  h.assert_true(profiles.is_shipped("pc-battery.v13"))
  h.assert_nil(profiles.UNSHIPPED_VERSIONS[13])
end

function T.test_the_dev_only_v14_moves_to_v15_with_its_monitor_card()
  -- v14 (Dev channel only) had every card. Its files are gone.
  h.assert_equal(profiles.migration_for("pc.v14"), "pc-monitor-watch.v15")
  h.assert_equal(profiles.migration_for("pc-battery.v14"), "pc-monitor-watch-battery.v15")
  h.assert_false(profiles.is_shipped("pc.v14"))
  h.assert_false(profiles.is_shipped("pc-battery.v14"))
end

function T.test_a_settled_card_wins_over_what_the_old_profile_had()
  -- #158: `fields.HAS_MONITOR` / `HAS_WATCH`, written once two statuses agreed.
  h.assert_equal(profiles.migration_for("pc.v13", { watch = false }), "pc.v15")
  h.assert_equal(profiles.migration_for("pc.v13", { monitor = true, watch = true }), "pc-monitor-watch.v15")
  h.assert_equal(profiles.migration_for("pc-battery.v14", { monitor = false, watch = false, battery = true }),
    "pc-battery.v15")
  h.assert_equal(profiles.migration_for("pc.v1", { monitor = true }), "pc-monitor.v15")
  -- Through `ensure`, from the persisted fields.
  profiles.reset()
  local device = device_on("pc.v13", "settled-v13")
  device:set_field(fields.HAS_WATCH, false)
  device:set_field(fields.HAS_MONITOR, true)
  h.assert_equal(profiles.ensure(device), "pc-monitor.v15")
  h.assert_deep_equal(profiles.stored_cards(device), { monitor = true, watch = false })
end

function T.test_the_cards_of_a_name()
  h.assert_deep_equal(profiles.cards_of("pc-monitor-watch-battery.v15"),
    { monitor = true, watch = true, battery = true })
  h.assert_deep_equal(profiles.cards_of("pc-watch.v15"), { monitor = false, watch = true, battery = false })
  h.assert_deep_equal(profiles.cards_of("pc.v15"), { monitor = false, watch = false, battery = false })
  -- An older name: what its generation had, a style is no card.
  h.assert_deep_equal(profiles.cards_of("pc-monitor.v10"), { monitor = false, watch = true, battery = false })
  h.assert_deep_equal(profiles.cards_of("pc-battery.v14"), { monitor = true, watch = true, battery = true })
  h.assert_deep_equal(profiles.cards_of("pc-tv-battery.v6"), { monitor = false, watch = false, battery = true })
  h.assert_nil(profiles.cards_of("thermostat"))
end

function T.test_a_v1_profile_can_migrate_straight_onto_a_battery_profile()
  -- #107: the caller decides the battery half (a laptop whose status has said
  -- so), the name decides the style.
  h.assert_equal(profiles.migration_for("pc.v1", { battery = true }), "pc-battery.v15")
  h.assert_equal(profiles.migration_for("pc-tv.v1", { battery = true }), "pc-battery.v15")
end

function T.test_known_is_every_v1_name_then_every_dev_generation_name_then_every_current_one()
  local expected = {}
  for _, style in ipairs(profiles.STYLES) do
    expected[#expected + 1] = style == "others" and "pc.v1" or ("pc-" .. style .. ".v1")
  end
  for _, version in ipairs({ 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12 }) do
    for _, battery in ipairs({ false, true }) do
      for _, style in ipairs(profiles.STYLES) do
        expected[#expected + 1] = profiles.name_for(style, battery, version)
      end
    end
  end
  -- #136: v13 and v14 had two names each, no style; #158: eight from v15.
  for _, name in ipairs({ "pc.v13", "pc-battery.v13", "pc.v14", "pc-battery.v14",
    "pc.v15", "pc-battery.v15", "pc-watch.v15", "pc-watch-battery.v15",
    "pc-monitor.v15", "pc-monitor-battery.v15", "pc-monitor-watch.v15", "pc-monitor-watch-battery.v15" }) do
    expected[#expected + 1] = name
  end
  h.assert_deep_equal(profiles.KNOWN, expected)
  h.assert_equal(#profiles.KNOWN, 242)
  for _, name in ipairs(profiles.KNOWN) do
    h.assert_true(name == "pc-battery.v13" or not name:match("^pc%-%a+%.v13$"),
      name .. ": v13 never had a styled name")
  end
end

function T.test_only_v1_and_the_current_generation_are_shipped()
  -- v2 to v9 never left the Dev channel, so their files are not in the
  -- package (the 655360-byte limit) - but their names are still KNOWN.
  h.assert_nil(profiles.UNSHIPPED_VERSIONS[1])
  for version = 2, 9 do
    h.assert_true(profiles.UNSHIPPED_VERSIONS[version] == true, "v" .. version)
  end
  h.assert_nil(profiles.UNSHIPPED_VERSIONS[10])
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

function T.test_the_card_variants_are_named_after_their_cards()
  -- #158: eight current names, `pc[-monitor][-watch][-battery].v15`.
  h.assert_equal(profiles.PC, "pc.v15")
  h.assert_equal(profiles.BATTERY, "pc-battery.v15")
  h.assert_equal(profiles.for_cards({ battery = true }), "pc-battery.v15")
  h.assert_equal(profiles.for_cards({}), "pc.v15")
  h.assert_equal(profiles.for_cards({ watch = true, monitor = true }), "pc-monitor-watch.v15")
  h.assert_deep_equal(profiles.CURRENT_NAMES, {
    "pc.v15", "pc-battery.v15", "pc-watch.v15", "pc-watch-battery.v15",
    "pc-monitor.v15", "pc-monitor-battery.v15", "pc-monitor-watch.v15", "pc-monitor-watch-battery.v15",
  })
  for _, name in ipairs(profiles.CURRENT_NAMES) do
    h.assert_true(profiles.is_current(name), name)
    h.assert_equal(profiles.for_cards(profiles.cards_of(name)), name, name .. " round trip")
  end
  local count = 0
  for _ in pairs(profiles.CURRENT) do
    count = count + 1
  end
  h.assert_equal(count, 8)
  h.assert_equal(profiles.CATEGORY, "Switch")
end

function T.test_a_name_that_is_not_current_is_not_current()
  h.assert_false(profiles.is_current("pc.v1"), "#107: v1 is not current any more")
  h.assert_false(profiles.is_current("pc.v2"), "nor is v2")
  h.assert_false(profiles.is_current("pc-tv-battery.v2"))
  h.assert_false(profiles.is_current("pc.v3"), "pcNotify: nor is v3")
  h.assert_false(profiles.is_current("pc-tv-battery.v3"))
  h.assert_false(profiles.is_current("pc.v4"), "pcToast: nor is v4")
  h.assert_false(profiles.is_current("pc-tv-battery.v4"))
  h.assert_false(profiles.is_current("pc.v5"), "#123: nor is v5")
  h.assert_false(profiles.is_current("pc-tv-battery.v5"))
  h.assert_false(profiles.is_current("pc.v10"), "#136: nor is v10, edge 1.1.0")
  h.assert_false(profiles.is_current("pc-monitor.v10"), "a v10 style name is no v15 card name")
  h.assert_false(profiles.is_current("pc.v11"), "#136: nor is v11, Dev only")
  h.assert_false(profiles.is_current("pc.v12"), "#136: nor is v12, Dev only")
  h.assert_false(profiles.is_current("pc-tv.v12"))
  h.assert_false(profiles.is_current("pc.v13"), "#151: nor is v13, edge 1.2.x")
  h.assert_false(profiles.is_current("pc-battery.v13"))
  h.assert_false(profiles.is_current("pc.v14"), "#158: nor is v14, Dev only")
  h.assert_false(profiles.is_current("pc-battery.v14"))
  h.assert_false(profiles.is_current("pc.v16"))
  h.assert_false(profiles.is_current("pc-tv.v15"), "no style in v15")
  h.assert_false(profiles.is_current("pc.v6"), "#123: nor is v6")
  h.assert_false(profiles.is_current("pc-tv-battery.v6"))
  h.assert_false(profiles.is_current("pc.v7"), "nor is v7, the first watch card")
  h.assert_false(profiles.is_current("pc-tv-battery.v7"))
  h.assert_false(profiles.is_current("pc.v8"), "nor is v8, packaged before the presentation landed")
  h.assert_false(profiles.is_current("pc-tv-battery.v8"))
  h.assert_false(profiles.is_current("pc.v9"), "nor is v9, the screen frozen on pcWatch's first presentation")
  h.assert_false(profiles.is_current("pc-tv-battery.v9"))
  h.assert_false(profiles.is_current("pc-app.v1"), "the app child was no PC profile")
  h.assert_false(profiles.is_current("pc-display.v1"))
  h.assert_false(profiles.is_current("thermostat"))
  h.assert_false(profiles.is_current(nil))
  h.assert_false(profiles.is_current("pc-display.v1"))
  h.assert_false(profiles.is_current(nil))
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
  for _, name in ipairs({ "pc.v15", "pc-monitor-watch-battery.v15", "pc.v17" }) do
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

function T.test_init_moves_a_v1_device_to_v15_and_repaints_it()
  -- #107: the same device on the real constants is a v1 device (no name, no
  -- field: LEGACY), so its first init after the update moves it to the current
  -- profile (pc.v15: no watch list, v1 had none) and paints the new generation of rows.
  profiles.reset()
  local poll = require "poll"
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v1"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-3"
  device.profile = { id = "abc-123", components = { { id = "main" } } }
  device:set_field(fields.ROWS_PAINTED, "1")
  lifecycle().init(fake_driver({ device }), device)
  h.assert_deep_equal(device.metadata_updates, { { profile = "pc.v15" } })
  h.assert_equal(device:get_field(fields.PROFILE_NAME), "pc.v15")
  h.assert_equal(device:get_field(fields.ROWS_PAINTED), poll.ROWS_VERSION,
    "the rows of the new capabilities start unset and are painted once")
  -- #129: the row generation is the profile generation.
  h.assert_equal(poll.ROWS_VERSION, tostring(profiles.VERSION))
end

-- The Dev channel generations (never packaged, still known): a device on any
-- of them moves to v15 with its battery half on its first init after the
-- update and paints the new row generation. A row the new profile adds is
-- painted at once, forced, because the cloud record of the new profile
-- starts empty: pcToast's `lastMessage` for a v4 or v5 device (whose pcNotify
-- text row was bound to nothing and spun into "네트워크 오류"). A v7, v8 or v9
-- device had the watch card (#158: so it keeps it), but its new profile's
-- record is empty too (and pcWatchList is a new capability), so every slot is
-- painted again. v5 and v6 had no `apps` component, so no watch list.
function T.test_init_moves_a_dev_generation_device_to_v15_keeping_battery_and_watch()
  local caps = require "caps"
  for i, c in ipairs({
    { "pc-battery.v2", "switch", "pc-battery.v15" },
    -- the reviewer's own device: pcMessage's two text fields
    { "pc-tv-battery.v3", "tv", "pc-battery.v15" },
    { "pc-tv-battery.v4", "tv", "pc-battery.v15", nil, caps.TOAST, "lastMessage", "없음" },
    { "pc-hub-battery.v5", "hub", "pc-battery.v15", nil, caps.TOAST, "lastMessage", "없음" },
    -- #123: the v6 device of the app-children build; every slot is new too.
    { "pc-plug.v6", "plug", "pc.v15" },
    { "pc-plug-battery.v6", "plug", "pc-battery.v15" },
    -- The first watch card (v7, the preview that wrapped): same card, new screen.
    { "pc-monitor.v7", "monitor", "pc-watch.v15", "apps", caps.WATCH, "slotOne", "empty" },
    { "pc-tv-battery.v7", "tv", "pc-watch-battery.v15", "apps", caps.WATCH, "summary", "없음" },
    -- v8: the same card, a screen built from the old capability presentation.
    { "pc-monitor.v8", "monitor", "pc-watch.v15", "apps", caps.WATCH, "slotOne", "empty" },
    { "pc-tv-battery.v8", "tv", "pc-watch-battery.v15", "apps", caps.WATCH, "summary", "없음" },
    -- v9: the same again, the screen still frozen on pcWatch's first presentation.
    { "pc-monitor.v9", "monitor", "pc-watch.v15", "apps", caps.WATCH, "slotOne", "empty" },
    { "pc-tv-battery.v9", "tv", "pc-watch-battery.v15", "apps", caps.WATCH, "summary", "없음" },
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
  h.assert_equal(profiles.migration_for("pc-tv.v5"), "pc.v15")
  h.assert_equal(profiles.migration_for("pc.v5", { battery = true }), "pc-battery.v15")
end

function T.test_init_leaves_a_v15_device_where_it_is()
  profiles.reset()
  local device = h.fake_device({ ipAddress = "192.168.1.20" })
  device.id = "init-pc-v15"
  device.device_network_id = discovery.DNI_PREFIX .. "manual-abc-4"
  device.profile = { id = "abc-123", name = "pc.v15", components = { { id = "main" } } }
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
  -- Rows already painted, so any repaint below comes from infoChanged.
  device:set_field(fields.ROWS_PAINTED, (require "poll").ROWS_VERSION)
  return device
end

function T.test_info_changed_without_a_new_style_touches_no_metadata()
  profiles.reset()
  counting_repaints(function(repaints)
    local device = styled_device("plain-pc", "switch")
    lifecycle().infoChanged(fake_driver({ device }), device, "infoChanged", {})
    h.assert_equal(#device.metadata_updates, 0)
    -- Any other preference change still repaints, as before #100.
    h.assert_equal(#repaints, 1)
  end)
end

return T
