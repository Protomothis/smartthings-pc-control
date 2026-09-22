-- The display child device (design doc §5.2, §13.1).
--
-- One `switch` whose on/off runs `turnscreenon` / `turnscreenoff` on the parent
-- and whose state follows `status.display`. Created while the parent's
-- `createDisplayDevice` preference is on and the parent has a machine_id;
-- removed when the preference goes off or the parent goes away.
--
-- Every decision is a pure function (`wanted`, `plan`, `switch_for`,
-- `command_for`); the driver-facing half only carries them out.

local client = require "client"
local discovery = require "discovery"
local i18n = require "i18n"
local profiles = require "profiles"
local state = require "state"

local display = {}

-- §14.3: the version lives in src/profiles.lua, not here.
display.PROFILE = profiles.DISPLAY
-- The child's DNI is the parent's plus this suffix, so it is derivable from
-- the machine_id alone (§13.1) and survives a driver restart.
display.SUFFIX = "-display"
display.CHILD_KEY = "display"
display.STATE_FIELD = "display_state"

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

--- The child's device network id for a parent machine_id.
function display.dni(machine_id)
  if type(machine_id) ~= "string" or machine_id == "" then
    return nil
  end
  return discovery.DNI_PREFIX .. machine_id .. display.SUFFIX
end

--- True when `device` is a display child rather than a PC.
function display.is_child(device)
  local dni = (device or {}).device_network_id
  if type(dni) == "string" and dni:sub(-#display.SUFFIX) == display.SUFFIX then
    return true
  end
  local key = (device or {}).parent_assigned_child_key
  return key == display.CHILD_KEY
end

--- §13.1: "<hostname> 모니터" / "<hostname> Monitor".
function display.label(hostname, lang)
  if type(hostname) ~= "string" or hostname == "" then
    hostname = "PC"
  end
  return i18n.t(lang, "display_label", hostname)
end

--- §5.2: the child exists while the preference is on and the parent has an
--- identity to hang the child's DNI on.
function display.wanted(prefs, machine_id)
  if (prefs or {}).createDisplayDevice == false then
    return false
  end
  return type(machine_id) == "string" and machine_id ~= ""
end

--- What to do about the child.
--
-- @param opts `wanted` (display.wanted), `exists`, `machine_id`, `hostname`,
--   `lang`, `current_label` and `previous_hostname` — the last two decide
--   whether a rename is ours to make: §13.1 says a label the user changed is
--   never overwritten, so only a label that still matches the old hostname is.
function display.plan(opts)
  opts = opts or {}
  local label = display.label(opts.hostname, opts.lang)

  if opts.wanted and not opts.exists then
    return {
      action = "create",
      device_network_id = display.dni(opts.machine_id),
      label = label,
    }
  end
  if opts.exists and not opts.wanted then
    return { action = "delete" }
  end
  if opts.exists and opts.wanted
      and opts.hostname and opts.hostname ~= ""
      and opts.current_label and opts.current_label ~= label
      and opts.previous_hostname
      and opts.current_label == display.label(opts.previous_hostname, opts.lang) then
    return { action = "relabel", label = label }
  end
  return { action = "none" }
end

--- `status.display` -> the child's `switch` value, or nil for `unknown`
--- (§5.2: an unknown display leaves the switch as it was).
function display.switch_for(display_state)
  if display_state == "on" or display_state == "off" then
    return display_state
  end
  return nil
end

--- The service command for a switch value (§5.2).
function display.command_for(switch_value)
  if switch_value == "on" then
    return "turnscreenon"
  end
  if switch_value == "off" then
    return "turnscreenoff"
  end
  return nil
end

--------------------------------------------------------------------------------
-- driver glue
--------------------------------------------------------------------------------

local function devices_of(driver)
  local ok, devices = pcall(function() return driver:get_devices() end)
  if ok and type(devices) == "table" then
    return devices
  end
  return {}
end

--- The existing child of `parent`, or nil.
function display.child_of(driver, parent)
  if not driver or not parent then
    return nil
  end
  local ok, children = pcall(function() return parent:get_child_list() end)
  if ok and type(children) == "table" then
    for _, child in ipairs(children) do
      if display.is_child(child) then
        return child
      end
    end
  end
  local dni = display.dni(discovery.machine_id_of(parent))
  if not dni then
    return nil
  end
  for _, device in ipairs(devices_of(driver)) do
    if device.device_network_id == dni then
      return device
    end
  end
  return nil
end

--- The PC a child belongs to, or nil.
function display.parent_of(driver, child)
  local ok, parent = pcall(function() return child:get_parent_device() end)
  if ok and parent then
    return parent
  end
  local dni = (child or {}).device_network_id
  if type(dni) ~= "string" then
    return nil
  end
  local parent_dni = dni:sub(1, #dni - #display.SUFFIX)
  for _, device in ipairs(devices_of(driver)) do
    if device.device_network_id == parent_dni then
      return device
    end
  end
  return nil
end

--- Create or delete the child so it matches the preference (§5.2).
function display.ensure(driver, parent)
  if not driver or not parent or display.is_child(parent) then
    return { action = "none" }
  end
  local machine_id = discovery.machine_id_of(parent)
  local child = display.child_of(driver, parent)
  local plan = display.plan({
    wanted = display.wanted(parent.preferences, machine_id),
    exists = child ~= nil,
    machine_id = machine_id,
    hostname = display.hostname_of(parent),
    lang = ((parent.preferences or {}).language),
    current_label = child and child.label or nil,
  })

  local log = logger()
  if plan.action == "create" then
    log.info("creating the display child for " .. tostring(parent.id))
    pcall(function()
      driver:try_create_device({
        type = "EDGE_CHILD",
        -- EDGE_CHILD may not set device_network_id (hub warning); the
        -- parent_assigned_child_key identifies the child instead.
        label = plan.label,
        profile = display.PROFILE,
        parent_device_id = parent.id,
        parent_assigned_child_key = display.CHILD_KEY,
        manufacturer = discovery.MANUFACTURER,
        model = discovery.MODEL,
        vendor_provided_label = discovery.MODEL,
      })
    end)
  elseif plan.action == "delete" then
    log.info("removing the display child of " .. tostring(parent.id))
    display.delete(driver, child)
  end
  return plan
end

--- Delete a child device, whichever of the two APIs the hub offers.
function display.delete(driver, child)
  if not child then
    return false
  end
  local ok = pcall(function() return child:try_delete_device() end)
  if ok then
    return true
  end
  return (pcall(function() return driver:try_delete_device(child.id) end))
end

--- The hostname SSDP or a poll learned for this PC.
function display.hostname_of(device)
  local ok, hostname = pcall(function() return device:get_field(discovery.HOSTNAME_FIELD) end)
  if ok and type(hostname) == "string" and hostname ~= "" then
    return hostname
  end
  return nil
end

--- Emit the child's `switch` from a status body (§5.2).
function display.sync(driver, parent, status)
  if type(status) ~= "table" then
    return false
  end
  local value = display.switch_for(status.display)
  if not value then
    -- "unknown": leave the switch showing what it last was.
    return false
  end
  local child = display.child_of(driver, parent)
  if not child then
    return false
  end
  if child:get_field(display.STATE_FIELD) == value then
    return false
  end
  child:set_field(display.STATE_FIELD, value)
  local poll = require "poll"
  poll.emit(child, { { cap = state.CAP_SWITCH, attr = "switch", value = value } })
  return true
end

--- switch on/off on the child: run the screen command on the parent (§5.2).
function display.handle_switch(driver, child, value, deps)
  local command = display.command_for(value)
  if not command then
    return false, "bad value"
  end
  local parent = display.parent_of(driver, child)
  if not parent then
    return false, "no parent"
  end
  local ok, _, kind = client.command(parent, command, "immediate", 0, deps)
  if not ok then
    return false, kind
  end
  -- Paint the child straight away; the next status confirms it.
  child:set_field(display.STATE_FIELD, value)
  local poll = require "poll"
  poll.emit(child, { { cap = state.CAP_SWITCH, attr = "switch", value = value } })
  -- The parent's `lastCommand` changed too.
  pcall(function() poll.once(driver, parent, { deps = deps }) end)
  return true
end

return display
