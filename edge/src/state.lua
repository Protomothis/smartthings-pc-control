-- The pure state layer, as one table (#129: the parts live in model/).
--
--   model/power.lua     the power state machine (§6.2) and transitions (§6.9)
--   model/commands.lua  the values the command and schedule lists rest on
--   model/text.lua      the sentences of the status rows
--   model/wol.lua       what a status body says about Wake-on-LAN
--   model/status.lua    status JSON -> event records (§3.2 -> §4)
--
-- Nothing here touches st.* or a device: the records go to device/emit.lua.

local state = {}

for _, name in ipairs({ "model.power", "model.commands", "model.text", "model.wol", "model.status" }) do
  for key, value in pairs(require(name)) do
    assert(state[key] == nil, "state." .. key .. " is defined twice")
    state[key] = value
  end
end

return state
