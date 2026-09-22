-- The capability JSON in `capabilities/` is not Lua, but it is part of the
-- driver's contract: the account owner feeds it to
-- `smartthings capabilities:create` and the ids, attributes and commands it
-- declares have to be exactly the ones src/ emits and handles. Nothing checks
-- that for us until the capabilities exist on a real account, so it is checked
-- here.
--
-- What this cannot check is whether SmartThings accepts the *shape* of these
-- files; see edge/README.md for the assumptions the owner has to confirm.

local h = require "helpers"
local caps = require "caps"
local json = require "st.json"
local state = require "state"

local T = {}

local function dirname(path)
  return (path or ""):match("^(.*)[/\\][^/\\]*$")
end

local tests_dir = SCRIPT_DIR or dirname(arg and arg[0]) or "tests"
local caps_dir = tests_dir .. "/../capabilities"

--------------------------------------------------------------------------------
-- reading the directory (fengari has host.*, a real interpreter has io.*)
--------------------------------------------------------------------------------

local function list_json()
  local names = {}
  if host and host.listdir then
    for _, name in ipairs(host.listdir(caps_dir)) do
      names[#names + 1] = name
    end
  elseif io.popen then
    local pipe = io.popen('ls "' .. caps_dir .. '"')
    if pipe then
      for name in pipe:lines() do
        names[#names + 1] = name
      end
      pipe:close()
    end
  end
  local out = {}
  for _, name in ipairs(names) do
    if name:match("%.json$") then
      out[#out + 1] = name
    end
  end
  table.sort(out)
  return out
end

local function read(name)
  local path = caps_dir .. "/" .. name
  if host and host.readfile then
    local text, err = host.readfile(path)
    if not text then
      error("cannot read " .. path .. ": " .. tostring(err), 0)
    end
    return text
  end
  local file = assert(io.open(path, "r"))
  local text = file:read("a")
  file:close()
  return text
end

-- name -> decoded table, parsed once for the whole suite.
local documents = {}
local definition_files, presentation_files = {}, {}

for _, name in ipairs(list_json()) do
  local ok, decoded = pcall(json.decode, read(name))
  if not ok then
    -- Recorded rather than raised: test_every_file_is_valid_json reports it
    -- with the file name, and the rest of the suite skips it.
    documents[name] = { __error = tostring(decoded) }
  else
    documents[name] = decoded
  end
  if name:match("%.presentation%.json$") then
    presentation_files[#presentation_files + 1] = name
  else
    definition_files[#definition_files + 1] = name
  end
end

-- short key (caps.ids) -> definition file name, e.g. power_state -> pcPowerState.json.
-- SmartThings lower-cases the name part of a capability id, so the match
-- against the camelCase file names is case-insensitive.
local function file_for(key)
  local id = caps.ids[key]
  local suffix = (id:gsub("^.*%.", "")):lower() .. ".json"
  for _, name in ipairs(definition_files) do
    if name:lower() == suffix then
      return name
    end
  end
  return suffix
end

local function definition(key)
  return documents[file_for(key)]
end

local function presentation(key)
  return documents[(file_for(key):gsub("%.json$", ".presentation.json"))]
end

--------------------------------------------------------------------------------
-- the files themselves
--------------------------------------------------------------------------------

function T.test_every_file_is_valid_json()
  h.assert_true(#definition_files > 0, "no capability JSON found in " .. caps_dir)
  for name, doc in pairs(documents) do
    if doc.__error then
      h.fail(name .. " is not valid JSON: " .. doc.__error)
    end
  end
end

function T.test_there_is_one_definition_and_one_presentation_per_capability()
  local expected = {}
  for key in pairs(caps.ids) do
    expected[#expected + 1] = file_for(key)
  end
  table.sort(expected)
  h.assert_deep_equal(definition_files, expected)
  h.assert_equal(#presentation_files, #expected,
    "every capability needs a presentation as well as a definition")
  for key in pairs(caps.ids) do
    h.assert_true(presentation(key) ~= nil, "no presentation for " .. caps.ids[key])
  end
end

function T.test_ids_match_caps_lua()
  -- The placeholder namespace lives in three places (caps.lua, pc.yml and
  -- these files) until #74's script rewrites them together; they must agree.
  for key, id in pairs(caps.ids) do
    h.assert_equal(definition(key).id, id, file_for(key) .. " id")
    h.assert_equal(presentation(key).id, id, file_for(key) .. " presentation id")
    -- SmartThings lower-cases the id but keeps the camelCase name.
    h.assert_equal(definition(key).name:lower(), (id:gsub("^.*%.", "")), file_for(key) .. " name")
  end
end

function T.test_the_profile_uses_the_same_ids()
  local path = tests_dir .. "/../profiles/pc.yml"
  local text
  if host and host.readfile then
    text = host.readfile(path)
  else
    local file = assert(io.open(path, "r"))
    text = file:read("a")
    file:close()
  end
  for _, id in pairs(caps.ids) do
    h.assert_contains(text, id, "profiles/pc.yml is missing ")
  end
end

--------------------------------------------------------------------------------
-- attributes
--------------------------------------------------------------------------------

function T.test_every_attribute_has_the_documented_schema()
  for key in pairs(caps.ids) do
    for attr, spec in pairs(definition(key).attributes or {}) do
      local where = string.format("%s.%s", file_for(key), attr)
      local schema = spec.schema or {}
      h.assert_equal(schema.type, "object", where .. " schema type")
      h.assert_false(schema.additionalProperties, where .. " additionalProperties")
      h.assert_deep_equal(schema.required, { "value" }, where .. " required")
      h.assert_true((schema.properties or {}).value ~= nil,
        where .. " has no `value` property")
    end
  end
end

function T.test_every_attribute_state_lua_emits_is_defined()
  local used = state.attributes_used()
  for key, id in pairs(caps.ids) do
    local defined = definition(key).attributes or {}
    for attr in pairs(used[id] or {}) do
      h.assert_true(defined[attr] ~= nil,
        string.format("state.lua emits %s.%s, but %s does not define it",
          id, attr, file_for(key)))
    end
  end
end

function T.test_no_attribute_is_defined_that_nothing_emits()
  -- The other direction: an attribute the app shows but the driver never sets
  -- is a tile stuck on "unknown" forever.
  local used = state.attributes_used()
  for key, id in pairs(caps.ids) do
    for attr in pairs(definition(key).attributes or {}) do
      h.assert_true((used[id] or {})[attr] == true,
        string.format("%s defines %s, which state.lua never emits", file_for(key), attr))
    end
  end
end

function T.test_the_enums_match_the_lua_constants()
  local power = definition("power_state").attributes.powerState.schema.properties.value
  h.assert_deep_equal(power.enum, {
    state.ON, state.SLEEPING, state.HIBERNATED, state.OFF,
    state.WAKING, state.SHUTTING_DOWN, state.UNKNOWN,
  })
  -- §4.1: `noSecret` is deliberately not a connection value; the warning goes
  -- into pcStatus.message instead.
  local connection = definition("status").attributes.connection.schema.properties.value
  h.assert_deep_equal(connection.enum, { "ok", "unauthorized", "unreachable", "incompatible" })
end

--------------------------------------------------------------------------------
-- commands
--------------------------------------------------------------------------------

-- The commands init.lua registers handlers for (§5.1).
local EXPECTED_COMMANDS = {
  power_state = {},
  command = { execute = { "command", "mode", "minutes" } },
  schedule = { cancel = {}, schedule = { "minutes", "command" } },
  status = {},
  session = {},
}

function T.test_commands_and_their_arguments_are_the_handled_ones()
  for key, expected in pairs(EXPECTED_COMMANDS) do
    local commands = definition(key).commands or {}
    local names = {}
    for name in pairs(commands) do
      names[#names + 1] = name
    end
    table.sort(names)

    local wanted = {}
    for name in pairs(expected) do
      wanted[#wanted + 1] = name
    end
    table.sort(wanted)
    h.assert_deep_equal(names, wanted, file_for(key) .. " commands")

    for name, arguments in pairs(expected) do
      local actual = {}
      for i, argument in ipairs(commands[name].arguments or {}) do
        actual[i] = argument.name
        h.assert_true((argument.schema or {}).type ~= nil,
          string.format("%s.%s(%s) has no schema type", name, argument.name or "?", i))
      end
      h.assert_deep_equal(actual, arguments, file_for(key) .. " " .. name .. " arguments")
    end
  end
end

function T.test_command_enums_match_the_service()
  -- §4.3: the command names the service accepts. `ping` is the driver's own
  -- reachability probe and is not offered in the app.
  local execute = definition("command").commands.execute.arguments[1].schema.enum
  local seen = {}
  for _, name in ipairs(execute) do
    seen[name] = true
  end
  for _, name in ipairs({ "shutdown", "forceshutdown", "restart", "hibernate",
      "suspend", "lock", "turnscreenoff", "turnscreenon" }) do
    h.assert_true(seen[name] == true, "pcCommand.execute is missing " .. name)
  end
  h.assert_equal(#execute, 8)

  local mode = definition("command").commands.execute.arguments[2].schema.enum
  h.assert_deep_equal(mode, { "default", "immediate", "grace" })

  -- §4.3: the service caps a schedule at 1440 minutes.
  -- schedule(minutes, command?): minutes first so a one-argument list works.
  local minutes = definition("schedule").commands.schedule.arguments[1].schema
  h.assert_equal(minutes.type, "integer")
  h.assert_equal(minutes.minimum, 1)
  h.assert_equal(minutes.maximum, 1440)
end

--------------------------------------------------------------------------------
-- presentations
--------------------------------------------------------------------------------

-- Collect `<attribute>.value` references and `"command": "<name>"` values from
-- anywhere in a presentation, including inside `{{ }}` templates.
local function references(node, attributes, commands)
  if type(node) == "string" then
    for name in node:gmatch("([%a][%w_]*)%.value") do
      attributes[name] = true
    end
    return attributes, commands
  end
  if type(node) ~= "table" then
    return attributes, commands
  end
  for key, value in pairs(node) do
    if key == "command" and type(value) == "string" then
      commands[value] = true
    else
      references(value, attributes, commands)
    end
  end
  return attributes, commands
end

function T.test_presentations_only_reference_defined_attributes_and_commands()
  for key, id in pairs(caps.ids) do
    local definitions = definition(key)
    local attributes, commands = references(presentation(key), {}, {})
    for name in pairs(attributes) do
      h.assert_true((definitions.attributes or {})[name] ~= nil,
        string.format("%s presentation uses %s.%s, which is not defined",
          id, id, name))
    end
    for name in pairs(commands) do
      h.assert_true((definitions.commands or {})[name] ~= nil,
        string.format("%s presentation runs %s(), which is not defined", id, name))
    end
  end
end

function T.test_the_dashboard_state_is_the_power_state()
  -- §5.3: powerState is the one thing the dashboard tile shows; the action on
  -- it comes from the standard `switch` capability, not from ours.
  local dashboard = presentation("power_state").dashboard
  h.assert_equal(#dashboard.states, 1)
  h.assert_contains(dashboard.states[1].label, "powerState.value")
  h.assert_equal(#dashboard.actions, 0, "the switch capability supplies the action")

  for _, key in ipairs({ "command", "schedule", "status", "session" }) do
    h.assert_equal(#presentation(key).dashboard.states, 0,
      caps.ids[key] .. " must not compete for the dashboard tile")
  end
end

function T.test_every_presentation_has_a_detail_view()
  for key, id in pairs(caps.ids) do
    local detail = presentation(key).detailView or {}
    h.assert_true(#detail > 0, id .. " has an empty detail view")
    for i, item in ipairs(detail) do
      h.assert_true(type(item.displayType) == "string",
        string.format("%s detailView[%d] has no displayType", id, i))
      h.assert_true(type(item.label) == "string",
        string.format("%s detailView[%d] has no label", id, i))
    end
  end
end

function T.test_the_documented_automation_conditions_and_actions_exist()
  -- §5.3: conditions on powerState / active / connection / locked, actions on
  -- execute / cancel / schedule.
  local function condition_attributes(key)
    local attributes = references((presentation(key).automation or {}).conditions or {}, {}, {})
    return attributes
  end
  h.assert_true(condition_attributes("power_state").powerState == true)
  h.assert_true(condition_attributes("schedule").active == true)
  h.assert_true(condition_attributes("status").connection == true)
  h.assert_true(condition_attributes("session").locked == true)

  local function action_commands(key)
    local _, commands = references((presentation(key).automation or {}).actions or {}, {}, {})
    return commands
  end
  h.assert_true(action_commands("command").execute == true)
  -- cancel is a detail-view pushButton; automation.actions may not carry
  -- pushButton (SmartThings presentation rules), so it is not expected there.
  h.assert_true(action_commands("schedule").schedule == true)
end

return T
