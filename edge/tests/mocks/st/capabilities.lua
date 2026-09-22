-- Minimal `st.capabilities` stand-in.
--
-- Indexing with any capability id yields a stub whose attributes are event
-- constructors: `capabilities.switch.switch("on")` returns
-- `{ capability = "switch", attribute = "switch", value = "on" }`, which is
-- enough for asserting what a driver would emit.

-- Fields that are not attributes, so the __index below must not turn them into
-- constructors.
local RESERVED = { ID = true, NAME = true, commands = true, attributes = true, id = true }

-- `capabilities.switch.commands.on.NAME` is how init.lua names a command, so
-- indexing `commands` yields a stub carrying the name it was asked for.
local function make_commands()
  return setmetatable({}, {
    __index = function(t, name)
      if type(name) ~= "string" then
        return nil
      end
      local command = { NAME = name, ID = name }
      rawset(t, name, command)
      return command
    end,
  })
end

local function make_capability(id)
  local cap = { ID = id, NAME = id, commands = make_commands(), attributes = {} }
  setmetatable(cap, {
    __index = function(t, key)
      if type(key) ~= "string" or RESERVED[key] then
        return nil
      end
      local constructor = function(value)
        return { capability = id, attribute = key, value = value }
      end
      rawset(t, key, constructor)
      return constructor
    end,
  })
  return cap
end

local capabilities = setmetatable({}, {
  __index = function(t, id)
    if type(id) ~= "string" then
      return nil
    end
    local cap = make_capability(id)
    rawset(t, id, cap)
    return cap
  end,
})

return capabilities
