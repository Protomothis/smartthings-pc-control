-- Profile names and the migration between their versions (design doc §14.3).
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
-- Everything here is pure except `remember` and `ensure`, which touch the
-- device, and both are guarded: a hub that refuses `try_update_metadata` must
-- not take the lifecycle handler down with it.

local profiles = {}

-- What new devices are created with.
profiles.PC = "pc.v3"
profiles.DISPLAY = "pc-display.v3"

-- Every profile name this driver has ever shipped, oldest first. A name that
-- is not in here belongs to another driver, or to a version newer than this
-- one, and is left alone.
profiles.KNOWN = { "pc.v1", "pc.v2", "pc.v3" }
profiles.KNOWN_DISPLAY = { "pc-display.v1", "pc-display.v2", "pc-display.v3" }

-- The profile name is written here at creation time and after a migration,
-- because `device.profile` does not always carry a name (see `name_of`).
profiles.FIELD = "profile_name"

-- What a device with neither a name nor the field is assumed to be on: every
-- device that existed before #79 was created with the v1 profiles.
profiles.LEGACY = "pc.v1"
profiles.LEGACY_DISPLAY = "pc-display.v1"

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

--- The profile a device of this kind is created with now.
function profiles.current(is_child)
  return is_child and profiles.DISPLAY or profiles.PC
end

--- Every profile name this driver has shipped for this kind of device.
function profiles.known(is_child)
  return is_child and profiles.KNOWN_DISPLAY or profiles.KNOWN
end

--- The profile `device_profile_name` has to move to, or nil.
--
-- nil covers all three "do nothing" cases: already current, not a name this
-- driver ever used (foreign device, or a version from the future), and no name
-- at all. The child profiles are a separate series, so `pc.v1` on a child is
-- foreign just as `pc-display.v1` on a PC is.
function profiles.migration_for(device_profile_name, is_child)
  if type(device_profile_name) ~= "string" or device_profile_name == "" then
    return nil
  end
  local target = profiles.current(is_child)
  if device_profile_name == target then
    return nil
  end
  for _, known in ipairs(profiles.known(is_child)) do
    if device_profile_name == known then
      return target
    end
  end
  return nil
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
function profiles.name_of(device, is_child)
  local profile = (device or {}).profile
  if type(profile) == "table" and type(profile.name) == "string" and profile.name ~= "" then
    return profile.name
  end
  if type(profile) == "string" and profile ~= "" then
    return profile
  end
  return stored_name(device)
    or (is_child and profiles.LEGACY_DISPLAY or profiles.LEGACY)
end

--------------------------------------------------------------------------------
-- driver glue
--------------------------------------------------------------------------------

--- Record that `device` is on the current profile, so the next driver version
--- can tell which version it has to move it from.
function profiles.remember(device, is_child, name)
  if type(device) ~= "table" or type(device.set_field) ~= "function" then
    return nil
  end
  name = name or profiles.current(is_child)
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

--- Move `device` onto the current profile if it is due (§14.3).
--
-- Returns the new profile name, or nil when nothing was done. Called from
-- `init` and `added`; only the first call per device does anything.
function profiles.ensure(device, is_child)
  if type(device) ~= "table" then
    return nil
  end
  local key = attempt_key(device)
  if attempted[key] then
    return nil
  end
  attempted[key] = true

  local name = profiles.name_of(device, is_child)
  local target = profiles.migration_for(name, is_child)
  if not target then
    -- Nothing to do, but keep the field in step with reality when the device
    -- is already on the current profile.
    if name == profiles.current(is_child) then
      profiles.remember(device, is_child, name)
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
  profiles.remember(device, is_child, target)
  logger().info(string.format("migrated %s to %s", tostring(device.id), target))
  return target
end

--- Forget the one-attempt-per-device guard (tests only).
function profiles.reset()
  attempted = {}
end

return profiles
