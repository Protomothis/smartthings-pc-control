-- Device and driver lifecycle (design doc §6.1, §6.3, §6.6). This driver
-- creates no child device; one an older build left on a development hub (#81's
-- display child, #123's app children) is deleted and otherwise ignored.

local client = require "client"
local discovery = require "discovery"
local emit = require "device.emit"
local fields = require "device.fields"
local i18n = require "i18n"
local log = require "log"
local poll = require "poll"
local profiles = require "profiles"
local push = require "push"
local rows = require "device.rows"
local state = require "state"
local version = require "driver_version"
local wol = require "wol"

local lifecycle = {}

--- A leftover child (`profiles.is_child`): asked to be deleted once per run,
--- and no PC path at all - no migration, poll, push or emit. True for one.
function lifecycle.leftover_child(driver, device)
  if not profiles.is_child(device) then
    return false
  end
  profiles.remove_child(driver, device)
  return true
end

function lifecycle.init(driver, device)
  log.info(string.format("init %s (driver %s)", device.id, version))
  if lifecycle.leftover_child(driver, device) then
    return
  end
  -- And the leftovers whose own `init` has not come (yet).
  profiles.remove_children(driver)
  -- A device keeps the screen it was created with (platform notes "프로필과
  -- 화면 생성"): one on an older profile moves to the current one, once. An
  -- `iconStyle` change the restart interrupted is applied now.
  local migrated = profiles.ensure(device)
  local switched = profiles.apply_style(device)
  -- A new generation of rows (new capability ids start unset) is painted once.
  local fresh_rows = poll.ensure_rows(device, driver)
  if fresh_rows or switched or migrated then
    -- `ensure_rows` has just repainted when it says so - not a second time.
    poll.repaint_soon(driver, device, { painted = fresh_rows })
  end
  -- §6.3: one listener per driver, opened on the first device that needs it.
  push.start(driver)
  poll.start(driver, device)
end

function lifecycle.added(driver, device)
  log.info("added " .. device.id)
  if lifecycle.leftover_child(driver, device) then
    return
  end
  profiles.remove_children(driver)
  -- Created by this run, so on the current profile: record the name (the hub
  -- does not always expose it) and let `ensure` confirm there is nothing to do.
  profiles.remember(device)
  profiles.ensure(device)
  -- §6.5: a device SSDP just created arrives with the address it was found at.
  discovery.adopt(device)
  -- Paint the tiles now; the first poll fills in the real values.
  local initial = state.new()
  fields.set_state(device, initial)
  rows.emit_power(device, initial)
  -- Every row once: one that was never sent reads "-".
  poll.ensure_rows(device, driver)
  if not client.device_base_url(device) then
    rows.emit_connection(device, "unreachable", i18n.t(fields.lang(device), "no_ip"))
  end
  -- The repaint is queued (emit.paint): its first batch now, the rest later.
  emit.paint_start(driver, device)
end

function lifecycle.removed(driver, device)
  log.info("removed " .. device.id)
  if profiles.is_child(device) then
    return
  end
  poll.stop(driver, device)
  wol.cancel_wake(driver, device)
  push.stop(driver, device)
end

--- Did this `infoChanged` move the device onto another profile? The hub
--- passes the record from before the change as `args.old_st_store` (lua_libs
--- st/driver.lua); when its profile is the one the device is on now, only
--- preferences or the label changed. Without it to compare, assume a move.
function lifecycle.profile_changed(device, args)
  local old = (((args or {}).old_st_store) or {}).profile
  if type(old) ~= "table" then
    return true
  end
  local now = device.profile
  if type(now) ~= "table" then
    return true
  end
  if old.id ~= nil and now.id ~= nil then
    return old.id ~= now.id
  end
  if old.name ~= nil and now.name ~= nil then
    return old.name ~= now.name
  end
  return true
end

function lifecycle.info_changed(driver, device, _event, args)
  log.info("preferences changed for " .. device.id)
  -- A leftover child has no preferences: this is the user renaming it.
  if profiles.is_child(device) then
    return
  end
  -- A new `iconStyle` moves the device onto the profile with that category.
  -- The move fires infoChanged again; `apply_style` then does nothing.
  local switched = profiles.apply_style(device)
  -- The restarted timer picks up a new interval, the poll a new address or
  -- secret. Only a profile that changed (a migration or a switch landing) is
  -- repainted: its cloud record is empty.
  poll.start(driver, device)
  if switched or lifecycle.profile_changed(device, args) then
    poll.repaint_soon(driver, device)
  end
end

function lifecycle.do_configure(driver, device)
  if profiles.is_child(device) then
    return
  end
  poll.start(driver, device)
end

--- Driver shutdown (hub restart, driver update): drop every device's push
--- subscription and close the listener, so the service does not post to a
--- port nobody listens on until the TTL runs out.
function lifecycle.driver(driver, event)
  if event ~= "shutdown" then
    return
  end
  local ok, devices = pcall(function() return driver:get_devices() end)
  for _, device in ipairs(ok and devices or {}) do
    if not profiles.is_child(device) then
      pcall(function() push.stop(driver, device) end)
    end
  end
  pcall(function() push.shutdown(driver) end)
  log.info("driver shutting down: push subscriptions released")
end

return lifecycle
