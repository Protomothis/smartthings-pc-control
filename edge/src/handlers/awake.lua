-- The `awake` component's standard switch: keep the PC from idle sleep
-- (#115). The hub hands both components' `switch` to one handler, with
-- `command.component` saying which (init.lua dispatches).

local common = require "handlers.common"
local emit = require "device.emit"
local features = require "features"
local fields = require "device.fields"

local awake = {}

local AWAKE_ROWS = {
  [features.AWAKE_COMPONENT .. "/" .. features.CAP_SWITCH .. ".switch"] = true,
}

-- Spring the toggle back to what the PC last said.
local function answer_awake(device)
  local on = (fields.extras(device) or {}).awake_on == true
  emit.rows(device, { {
    cap = features.CAP_SWITCH, attr = "switch", value = on and "on" or "off",
    component = features.AWAKE_COMPONENT,
  } }, { reason = "answer" })
end

--- True when a `switch` command is for the `awake` component.
function awake.is_awake(cmd)
  return type(cmd) == "table" and cmd.component == features.AWAKE_COMPONENT
end

--- switch.on: no idle sleep for `awakeMinutes` (0 = until switched off);
--- again while on, a new period from now. Not held back by a transition - it
--- does not move the PC's power, and on a PC that is going away it fails.
function awake.on(driver, device)
  return common.run_feature(driver, device, "awake", features.awake_minutes(device.preferences),
    answer_awake, AWAKE_ROWS)
end

function awake.off(driver, device)
  return common.run_feature(driver, device, "awakeoff", nil, answer_awake, AWAKE_ROWS)
end

return awake
