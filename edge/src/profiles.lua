-- Profile names and the migration between their versions (design doc §6.6).
--
-- Measured on the hub (#79): a device's screen definition is generated from
-- the capability presentations *at device-creation time* and is never
-- regenerated. Re-publishing the same profile name (pc.v1) picked up new
-- preferences but kept the old detail view; only moving the device to a
-- profile with a **new name** regenerates it.
--
-- So every presentation change bumps the profile version: a new
-- `profiles/pc-vN.yml` with `name: pc.vN`, the older files kept in the package
-- (existing devices still reference them until they are moved), and this
-- module moves each device over on its first `init` after the update.
--
-- #90: at the first channel release the numbering was reset to `pc.v1`. The
-- pc.v2…pc.v17 of the development phase never reached a user's hub, so nothing
-- has to be migrated from them and `KNOWN` holds the one current name - which
-- makes `migration_for` a no-op for every device. The machinery stays: the
-- next screen change ships `pc.v2` and appends it here.
--
-- #100: the icon. The app draws a device's icon from its profile's category,
-- `Others` offers no icon choice, and a category is fixed per profile - so the
-- `iconStyle` preference is served by one profile per style, identical to
-- `pc.v1` except for `name` and the category (`pc-monitor.v1`, …). All of
-- them are "current" (`CURRENT`): a device on any of them is where it belongs
-- and `ensure` never moves it. `apply_style` moves a device between them when
-- the preference and the profile disagree.
--
-- A version bump therefore bumps EVERY variant together: `pc.v2` comes with
-- `pc-monitor.v2` and the rest, `PC` and `VARIANTS` get the new names, and
-- every v1 name (`pc.v1` and each `pc-<style>.v1`) goes into `KNOWN`.
-- `migration_for` keeps the style it reads off the old name, so a device on
-- `pc-monitor.v1` lands on `pc-monitor.v2`, not on the default.
--
-- Everything here is pure except `remember`, `ensure`, `apply_style` and
-- `remove_legacy_child`, which touch the device, and all of them are guarded:
-- a hub that refuses `try_update_metadata` or `try_delete_device` must not
-- take the lifecycle handler down with it.

local profiles = {}

-- What new devices are created with.
profiles.PC = "pc.v1"

-- Every profile name this driver has ever shipped, oldest first. A name that
-- is not in here belongs to another driver, or to a version newer than this
-- one, and is left alone. Only the current name is in here at the first
-- release (#90), so there is nothing to migrate from; a `pc.v2` appends.
profiles.KNOWN = { "pc.v1" }

-- #100: the `iconStyle` preference, whose default is served by `PC` itself.
profiles.DEFAULT_STYLE = "others"

-- Every other style and the profile that carries its category. The files are
-- `profiles/pc-<style>.yml`; tests/capabilities_test.lua holds each of them to
-- pc.yml line by line.
profiles.VARIANTS = {
  monitor = "pc-monitor.v1",
  switch = "pc-switch.v1",
  plug = "pc-plug.v1",
  tv = "pc-tv.v1",
  projector = "pc-projector.v1",
  network = "pc-network.v1",
  hub = "pc-hub.v1",
  theater = "pc-theater.v1",
  remote = "pc-remote.v1",
}

-- The styles in the order the preference lists them, and the category each
-- profile file declares - what the app picks the icon from. Documentation as
-- much as data: the tests check both against pc.yml and the variant files.
profiles.STYLES = {
  "others", "monitor", "switch", "plug", "tv",
  "projector", "network", "hub", "theater", "remote",
}
profiles.CATEGORIES = {
  others = "Others",
  monitor = "SmartMonitor",
  switch = "Switch",
  plug = "SmartPlug",
  tv = "Television",
  projector = "Projector",
  network = "Networking",
  hub = "Hub",
  theater = "HomeTheater",
  remote = "RemoteController",
}

-- Every name a device may sit on without being migrated: `PC` and each
-- variant. A set, `CURRENT[name] == true`.
profiles.CURRENT = { [profiles.PC] = true }
for _, name in pairs(profiles.VARIANTS) do
  profiles.CURRENT[name] = true
end

-- The profile name is written here at creation time and after a migration,
-- because `device.profile` does not always carry a name (see `name_of`).
profiles.FIELD = "profile_name"

-- What a device with neither a name nor the field is assumed to be on: the
-- oldest name this driver ever created a device with. At v1.0.0 that is also
-- the current one, so the assumption costs nothing.
profiles.LEGACY = "pc.v1"

-- #81: the display child device is gone. Its profiles (`pc-display.vN`) are no
-- longer in the package, so a child left on a hub from an older driver has
-- nothing to render and is deleted on the next init.
--
-- Only a development hub can still carry such a child, since #81 predates the
-- first channel release (#90). Kept anyway: the check is one pure comparison
-- per device per driver run and the author's own hub is such a hub.
profiles.DISPLAY_PREFIX = "pc-display"

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

--------------------------------------------------------------------------------
-- pure
--------------------------------------------------------------------------------

--- The profile a device is created with now.
function profiles.current()
  return profiles.PC
end

--- Every profile name this driver has shipped.
function profiles.known()
  return profiles.KNOWN
end

--- #100: true when `name` is one of the profiles a device is created on or
--- switched between now - `PC` or any variant.
function profiles.is_current(name)
  if type(name) ~= "string" or name == "" then
    return false
  end
  return name == profiles.PC or profiles.CURRENT[name] == true
end

--- #100: the profile an `iconStyle` value asks for. Anything that is not a
--- known style (nil, a typo, a value a newer driver added) is the default.
function profiles.for_style(style)
  return profiles.VARIANTS[style] or profiles.PC
end

--- #100: the `iconStyle` a current profile name stands for, or nil for a name
--- that is not current (older, foreign, or none).
function profiles.style_of(name)
  if type(name) ~= "string" then
    return nil
  end
  if name == profiles.PC then
    return profiles.DEFAULT_STYLE
  end
  for style, variant in pairs(profiles.VARIANTS) do
    if variant == name then
      return style
    end
  end
  return nil
end

--- The style an OLDER name of this driver carried: `pc-<style>.vN` with a
--- style that still exists, else the default. Pure pattern reading, because a
--- superseded name is by definition no longer in `VARIANTS`.
local function style_in_name(name)
  local style = name:match("^pc%-(%a+)%.v%d+$")
  if style and profiles.VARIANTS[style] then
    return style
  end
  return profiles.DEFAULT_STYLE
end

--- The profile `device_profile_name` has to move to, or nil.
--
-- nil covers all three "do nothing" cases: already current (any variant,
-- #100), not a name this driver ever used (foreign device, or a version from
-- the future), and no name at all. A `pc-display.vN` name is not ours to
-- migrate either: #81 removed that series, and such a device is deleted
-- instead (`is_legacy_child`). A known older name keeps its style.
function profiles.migration_for(device_profile_name)
  if type(device_profile_name) ~= "string" or device_profile_name == "" then
    return nil
  end
  if profiles.is_current(device_profile_name) then
    return nil
  end
  for _, known in ipairs(profiles.known()) do
    if device_profile_name == known then
      return profiles.for_style(style_in_name(device_profile_name))
    end
  end
  return nil
end

--- #81: true when `device` is a leftover display child of an older driver.
--
-- Two independent marks, because a hub may only carry one of them: the
-- `parent_assigned_child_key` an EDGE_CHILD was created with (the child had no
-- DNI of its own), and a profile name from the removed `pc-display` series.
-- Pure, so the decision is testable without a hub.
function profiles.is_legacy_child(device)
  if type(device) ~= "table" then
    return false
  end
  local key = device.parent_assigned_child_key
  if type(key) == "string" and key ~= "" then
    return true
  end
  local name = profiles.name_of(device)
  return type(name) == "string"
    and name:sub(1, #profiles.DISPLAY_PREFIX) == profiles.DISPLAY_PREFIX
end

--------------------------------------------------------------------------------
-- reading the name off a device
--------------------------------------------------------------------------------

local function stored_name(device)
  if type(device) ~= "table" or type(device.get_field) ~= "function" then
    return nil
  end
  local ok, value = pcall(function() return device:get_field(profiles.FIELD) end)
  if ok and type(value) == "string" and value ~= "" then
    return value
  end
  return nil
end

--- The profile name of `device`.
--
-- The Edge API exposes `device.profile` as a table of `id` and `components`;
-- the *name* is there on some firmwares only, so it wins when present and the
-- field written at creation time (and after every migration) is the fallback.
-- A device with neither predates #79 and is therefore on v1.
function profiles.name_of(device)
  local profile = (device or {}).profile
  if type(profile) == "table" and type(profile.name) == "string" and profile.name ~= "" then
    return profile.name
  end
  if type(profile) == "string" and profile ~= "" then
    return profile
  end
  return stored_name(device) or profiles.LEGACY
end

--------------------------------------------------------------------------------
-- driver glue
--------------------------------------------------------------------------------

--- Record that `device` is on the current profile, so the next driver version
--- can tell which version it has to move it from.
function profiles.remember(device, name)
  if type(device) ~= "table" or type(device.set_field) ~= "function" then
    return nil
  end
  name = name or profiles.current()
  pcall(function() device:set_field(profiles.FIELD, name, { persist = true }) end)
  return name
end

-- Devices this driver run has already looked at. One attempt per device per
-- driver lifetime: `added` and `init` both call `ensure`, and a hub that
-- refuses the update must not be asked again in a loop.
local attempted = {}

local function attempt_key(device)
  local id = device.id
  if type(id) == "string" and id ~= "" then
    return id
  end
  return device
end

--- Move `device` onto the current profile if it is due (§6.6).
--
-- Returns the new profile name, or nil when nothing was done. Called from
-- `init` and `added`; only the first call per device does anything.
function profiles.ensure(device)
  if type(device) ~= "table" then
    return nil
  end
  local key = attempt_key(device)
  if attempted[key] then
    return nil
  end
  attempted[key] = true

  local name = profiles.name_of(device)
  local target = profiles.migration_for(name)
  if not target then
    -- Nothing to do, but keep the field in step with reality when the device
    -- is already on a current profile (the default or a variant, #100).
    if profiles.is_current(name) then
      profiles.remember(device, name)
    end
    return nil
  end

  local ok, err = pcall(function()
    return device:try_update_metadata({ profile = target })
  end)
  if not ok then
    logger().warn(string.format("could not migrate %s to %s: %s",
      tostring(device.id), target, tostring(err)))
    return nil
  end
  profiles.remember(device, target)
  logger().info(string.format("migrated %s to %s", tostring(device.id), target))
  return target
end

-- #100: the loop guard of `apply_style`, two maps per driver run.
--
-- `switched` is the profile this run last moved each device to. It stands in
-- for `name_of` afterwards: the switch itself fires `infoChanged` again, and on
-- a hub that is slow to (or never does) put the new name on `device.profile`,
-- `name_of` would still read the old one and ask for the same switch again,
-- and again.
--
-- `refused` is the target the hub last turned down. The same preference is
-- not asked for twice in one run; a different value, or the next driver start,
-- asks again.
local switched = {}
local refused = {}

--- #100: move `device` onto the profile its `iconStyle` preference asks for.
--
-- Returns the new profile name, or nil when nothing was done. Called from
-- `infoChanged` (the user changed the preference) and from `init` (the driver
-- restarted between the preference changing and the switch landing).
--
-- Left alone: a device with no `iconStyle` at all (the preferences have not
-- arrived - it is not a request for the default), and a device that is not on
-- a current profile (foreign, or an older version that `ensure` moves first).
-- At most one `try_update_metadata` per device and target per driver run.
function profiles.apply_style(device)
  if type(device) ~= "table" then
    return nil
  end
  local style = (device.preferences or {}).iconStyle
  if style == nil then
    return nil
  end
  local key = attempt_key(device)
  local name = switched[key] or profiles.name_of(device)
  if not profiles.is_current(name) then
    return nil
  end
  local target = profiles.for_style(style)
  if name == target or refused[key] == target then
    return nil
  end

  local ok, err = pcall(function()
    return device:try_update_metadata({ profile = target })
  end)
  if not ok then
    refused[key] = target
    logger().warn(string.format("could not switch %s to %s: %s",
      tostring(device.id), target, tostring(err)))
    return nil
  end
  switched[key] = target
  refused[key] = nil
  profiles.remember(device, target)
  logger().info(string.format("switched %s from %s to %s (iconStyle %s)",
    tostring(device.id), name, target, tostring(style)))
  return target
end

-- Devices this driver run has already tried to delete (#81), for the same
-- reason `attempted` exists: `init` fires more than once per device.
local removed = {}

--- #81: delete a leftover display child, once per device per driver run.
--
-- Returns true when the delete was attempted. Both APIs are tried because
-- `try_delete_device` sits on the device on some firmwares and on the driver
-- on others, and neither may take the lifecycle handler down.
function profiles.remove_legacy_child(driver, device)
  if not profiles.is_legacy_child(device) then
    return false
  end
  local key = attempt_key(device)
  if removed[key] then
    return false
  end
  removed[key] = true

  logger().info("removing legacy display child " .. tostring(device.id))
  local ok = pcall(function() return device:try_delete_device() end)
  if not ok and driver then
    pcall(function() return driver:try_delete_device(device.id) end)
  end
  return true
end

--- Forget the one-attempt-per-device guards (tests only).
function profiles.reset()
  attempted = {}
  removed = {}
  switched = {}
  refused = {}
end

return profiles
