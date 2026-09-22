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
-- Everything here is pure except `remember`, `ensure` and
-- `remove_legacy_child`, which touch the device, and all three are guarded: a
-- hub that refuses `try_update_metadata` or `try_delete_device` must not take
-- the lifecycle handler down with it.

local profiles = {}

-- What new devices are created with.
profiles.PC = "pc.v14"

-- Every profile name this driver has ever shipped, oldest first. A name that
-- is not in here belongs to another driver, or to a version newer than this
-- one, and is left alone.
profiles.KNOWN = { "pc.v1", "pc.v2", "pc.v3", "pc.v4", "pc.v5", "pc.v6", "pc.v7", "pc.v8", "pc.v9", "pc.v10", "pc.v11", "pc.v12", "pc.v13", "pc.v14" }

-- The profile name is written here at creation time and after a migration,
-- because `device.profile` does not always carry a name (see `name_of`).
profiles.FIELD = "profile_name"

-- What a device with neither a name nor the field is assumed to be on: every
-- device that existed before #79 was created with the v1 profile.
profiles.LEGACY = "pc.v1"

-- #81: the display child device is gone. Its profiles (`pc-display.vN`) are no
-- longer in the package, so a child left on a hub from an older driver has
-- nothing to render and is deleted on the next init.
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

--- The profile `device_profile_name` has to move to, or nil.
--
-- nil covers all three "do nothing" cases: already current, not a name this
-- driver ever used (foreign device, or a version from the future), and no name
-- at all. A `pc-display.vN` name is not ours to migrate either: #81 removed
-- that series, and such a device is deleted instead (`is_legacy_child`).
function profiles.migration_for(device_profile_name)
  if type(device_profile_name) ~= "string" or device_profile_name == "" then
    return nil
  end
  local target = profiles.current()
  if device_profile_name == target then
    return nil
  end
  for _, known in ipairs(profiles.known()) do
    if device_profile_name == known then
      return target
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
    -- is already on the current profile.
    if name == profiles.current() then
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
end

return profiles
