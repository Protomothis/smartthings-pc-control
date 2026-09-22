-- The capability JSON in `capabilities/` is not Lua, but it is part of the
-- driver's contract: the account owner feeds it to
-- `smartthings capabilities:create` and the ids, attributes and commands it
-- declares have to be exactly the ones src/ emits and handles. Nothing checks
-- that for us until the capabilities exist on a real account, so it is checked
-- here.
--
-- What this cannot check is whether SmartThings accepts the *shape* of these
-- files; see ../docs/design/edge-platform-notes.md for the platform rules they encode.

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

-- short key (caps.ids) -> definition file name, e.g. power_state -> pcPower.json.
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

--------------------------------------------------------------------------------
-- the profiles (#79: there is one file per profile version)
--------------------------------------------------------------------------------

local profiles_dir = tests_dir .. "/../profiles"

local function list_yml()
  local names = {}
  if host and host.listdir then
    for _, name in ipairs(host.listdir(profiles_dir)) do
      names[#names + 1] = name
    end
  elseif io.popen then
    local pipe = io.popen('ls "' .. profiles_dir .. '"')
    if pipe then
      for name in pipe:lines() do
        names[#names + 1] = name
      end
      pipe:close()
    end
  end
  local out = {}
  for _, name in ipairs(names) do
    if name:match("%.yml$") then
      out[#out + 1] = name
    end
  end
  table.sort(out)
  return out
end

-- file name -> text, for every profiles/*.yml.
local profile_files = {}
for _, name in ipairs(list_yml()) do
  local path = profiles_dir .. "/" .. name
  if host and host.readfile then
    profile_files[name] = host.readfile(path)
  else
    local file = assert(io.open(path, "r"))
    profile_files[name] = file:read("a")
    file:close()
  end
end

--- The `name:` a profile file declares.
local function profile_name(text)
  return (text or ""):match("\nname:%s*([%w%.%-_]+)") or (text or ""):match("^name:%s*([%w%.%-_]+)")
end

--- The file that declares `name`, or nil.
local function profile_file_for(wanted)
  for name, text in pairs(profile_files) do
    if profile_name(text) == wanted then
      return name, text
    end
  end
  return nil
end

function T.test_the_current_profile_lists_every_capability()
  -- A device is created on (and migrated to) this one, so it is the only file
  -- that has to carry the whole set. #86 added `pcversion`; the older files are
  -- left exactly as they shipped - a device still on one of them keeps the
  -- screen it was created with until the migration moves it (platform notes "프로필과 화면 생성").
  local profiles = require "profiles"
  local name, text = profile_file_for(profiles.current())
  h.assert_true(text ~= nil, "no profile file declares " .. profiles.current())
  for _, id in pairs(caps.ids) do
    h.assert_contains(text, id, "profiles/" .. tostring(name) .. " is missing ")
  end
end

function T.test_no_profile_references_a_capability_this_driver_dropped()
  -- The other direction, over every file: an id that is in a profile but not in
  -- caps.lua is a capability the driver no longer emits anything for, so every
  -- row of that card would read "-" on a device still sitting there.
  -- #81 removed the child profiles, so every file left here is a PC profile.
  local known = {}
  for _, id in pairs(caps.ids) do
    known[(id:gsub("^.*%.", ""))] = true
  end
  local checked = 0
  for name, text in pairs(profile_files) do
    checked = checked + 1
    for id in (text or ""):gmatch("numbersystem53811%.(%a+)") do
      h.assert_true(known[id] == true,
        "profiles/" .. name .. " references " .. id .. ", which caps.lua does not know")
    end
  end
  h.assert_true(checked > 0, "no main profile found in " .. profiles_dir)
end

function T.test_no_display_child_profile_is_shipped()
  -- #81: the child device is gone; a leftover pc-display*.yml would let the
  -- hub keep rendering children this driver no longer manages.
  for name in pairs(profile_files) do
    h.assert_true(name:match("^pc%-display") == nil,
      "profiles/" .. name .. " is a display child profile (#81 removed them)")
  end
end

function T.test_every_profile_file_declares_a_name_profiles_lua_knows()
  -- The package has to carry a file for every name the driver may leave a
  -- device on, or a device on an older profile breaks at install time.
  local profiles = require "profiles"
  local declared = {}
  for name, text in pairs(profile_files) do
    local declared_name = profile_name(text)
    h.assert_true(type(declared_name) == "string" and declared_name ~= "",
      "profiles/" .. name .. " declares no name")
    declared[declared_name] = name
  end
  for _, known in ipairs(profiles.KNOWN) do
    h.assert_true(declared[known] ~= nil,
      "src/profiles.lua knows " .. known .. ", but no profile file declares it")
  end
  h.assert_true(declared[profiles.PC] ~= nil, "no file declares " .. profiles.PC)
end

--- The custom capability names a profile lists, in order.
local function capability_order(text)
  local order = {}
  for id in (text or ""):gmatch("numbersystem53811%.(%a+)") do
    order[#order + 1] = id
  end
  return order
end

function T.test_the_version_capability_is_last_in_the_current_profile()
  -- #85: the app draws the cards in the order the profile lists them, so the
  -- version row sits at the bottom of the screen only if its capability is the
  -- last entry. #86 made that capability `pcVersion` (two state rows of one
  -- capability are drawn as two narrow columns), with `pcInfo` right above it.
  local profiles = require "profiles"
  local name, text = profile_file_for(profiles.current())
  h.assert_true(text ~= nil, "no profile file declares " .. profiles.current())
  local order = capability_order(text)
  h.assert_true(#order > 1, "profiles/" .. tostring(name) .. " lists no custom capability")
  h.assert_equal(order[#order], (caps.VERSION:gsub("^.*%.", "")),
    "profiles/" .. tostring(name) .. " must list the version capability last (#86)")
  h.assert_equal(order[#order - 1], (caps.STATUS:gsub("^.*%.", "")),
    "the info card belongs directly above the version card (#86)")
end

function T.test_every_older_profile_still_ends_with_the_card_it_shipped_with()
  -- The older files are frozen (#79): a device that has not been migrated yet
  -- still renders them, so they must keep the reading order they were packaged
  -- with. That is the info card last for everything up to #85, and the version
  -- card last from #86 on - which is when the version row got a capability of
  -- its own. A file is read for which of the two it carries rather than by its
  -- number, so a rename like #88's (pc.v13 becoming an older profile) does not
  -- have to be spelled out here.
  local profiles = require "profiles"
  local version = (caps.VERSION:gsub("^.*%.", ""))
  local info = (caps.STATUS:gsub("^.*%.", ""))
  for name, text in pairs(profile_files) do
    if profile_name(text) ~= profiles.current() then
      local order = capability_order(text)
      h.assert_true(#order > 0, "profiles/" .. name .. " lists no custom capability")
      local carries_version = false
      for _, id in ipairs(order) do
        carries_version = carries_version or id == version
      end
      local last = carries_version and version or info
      h.assert_equal(order[#order], last,
        "profiles/" .. name .. " must keep the card it shipped with last (#85, #86)")
    end
  end
end

function T.test_no_two_profile_files_share_a_name()
  -- Two files with the same `name:` is what a copied-but-not-renamed version
  -- bump looks like, and the hub would take whichever it read last.
  local seen = {}
  for name, text in pairs(profile_files) do
    local declared_name = profile_name(text)
    h.assert_nil(seen[declared_name],
      "profiles/" .. name .. " and profiles/" .. tostring(seen[declared_name])
      .. " both declare " .. tostring(declared_name))
    seen[declared_name] = name
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
  -- §3.1: `noSecret` is deliberately not a connection value; the warning goes
  -- into pcInfo.message instead.
  local connection = definition("status").attributes.connection.schema.properties.value
  h.assert_deep_equal(connection.enum, { "ok", "unauthorized", "unreachable", "incompatible" })
end

--------------------------------------------------------------------------------
-- commands
--------------------------------------------------------------------------------

-- The commands init.lua registers handlers for (§4). The eight no-argument
-- ones were the detail view's push buttons until #82 replaced them with one
-- `execute` list; they stay in the definition (older profiles still show them,
-- and a scene can call them) and `execute` carries the screen and automations.
local REMOTE_BUTTONS = {
  "wake", "suspend", "hibernate", "restart", "shutdown", "lock",
  "screenOff", "screenOn",
}

-- #82: what the detail-view list offers, top to bottom. Service command names
-- (§3.3), because that is what `execute(command)` takes; `wake` is the WoL
-- sequence and `forceshutdown` is deliberately absent — an irreversible
-- command stays in automations only. #84 keeps the menu as it was: `none` is
-- in the enum so a dismissed picker is valid, not so it can be picked.
local ACTION_LIST = {
  "wake", "suspend", "hibernate", "restart", "shutdown", "lock",
  "turnscreenoff", "turnscreenon",
}

-- #84: what the "command to schedule" row offers, and the `planCommand` enum.
local PLAN_LIST = { "shutdown", "restart", "suspend", "hibernate" }

local EXPECTED_COMMANDS = {
  power_state = {},
  command = {
    execute = { "command", "mode", "minutes" },
  },
  -- #85: `setPlanCommand` lives here, with the row it drives.
  schedule = {
    cancel = {},
    schedule = { "minutes", "command" },
    setPlanCommand = { "command" },
  },
  status = {},
  session = {},
  -- #86: a read-only card; the version row has nothing to command.
  version = {},
}

for _, name in ipairs(REMOTE_BUTTONS) do
  EXPECTED_COMMANDS.command[name] = {}
end

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
  -- §3.3: the command names the service accepts. `ping` is the driver's own
  -- reachability probe and is not offered in the app. #82 added `wake`, which
  -- is not a service command at all — the driver turns it into the WoL
  -- sequence — so that the detail-view list can offer it like the rest.
  -- #84 added `none`, the no-op a dismissed picker sends (platform notes "상세 화면(detailView) 위젯").
  local execute = definition("command").commands.execute.arguments[1].schema.enum
  local seen = {}
  for _, name in ipairs(execute) do
    seen[name] = true
  end
  for _, name in ipairs({ "none", "wake", "shutdown", "forceshutdown", "restart",
      "hibernate", "suspend", "lock", "turnscreenoff", "turnscreenon" }) do
    h.assert_true(seen[name] == true, "pcExec.execute is missing " .. name)
  end
  h.assert_equal(#execute, 10)
  for _, name in ipairs(ACTION_LIST) do
    h.assert_true(seen[name] == true,
      "the detail-view list offers " .. name .. ", which execute does not accept")
  end

  -- #84: `lastAction` holds exactly what `execute` accepts, because the phone
  -- sends the row's current value when the list is closed without a pick.
  h.assert_deep_equal(definition("command").attributes.lastAction.schema.properties.value.enum,
    execute, "lastAction and execute.command must be the same set, in the same order")
  h.assert_deep_equal(state.ACTIONS, execute, "state.ACTIONS is the lastAction enum")

  -- #84/#85: the schedulable commands, shared by the attribute, its setter and
  -- the optional argument of `schedule` - all three on the schedule capability.
  h.assert_deep_equal(definition("schedule").attributes.planCommand.schema.properties.value.enum,
    PLAN_LIST)
  h.assert_deep_equal(definition("schedule").commands.setPlanCommand.arguments[1].schema.enum,
    PLAN_LIST)
  h.assert_deep_equal(definition("schedule").commands.schedule.arguments[2].schema.enum,
    PLAN_LIST)
  h.assert_deep_equal(state.PLAN_COMMANDS, PLAN_LIST)

  local mode = definition("command").commands.execute.arguments[2].schema.enum
  h.assert_deep_equal(mode, { "default", "immediate", "grace" })

  -- §3.3: the service caps a schedule at 1440 minutes.
  -- schedule(minutes, command?): minutes first so a one-argument list works.
  local minutes = definition("schedule").commands.schedule.arguments[1].schema
  h.assert_equal(minutes.type, "integer")
  -- #85, measured on the phone: the cloud validates command arguments against
  -- the definition and never forwards a rejected one, so the list's `취소`
  -- entry (minutes = 0) failed with "system error" while the minimum was 1.
  -- #88: and it starts at -1, the no-op a dismissed picker sends. The row rests
  -- on `minutesPick` = "-1", and a value the row can show has to be a value the
  -- command accepts (platform notes "상세 화면(detailView) 위젯").
  h.assert_equal(minutes.minimum, -1, "minutes = -1 is what a dismissed picker sends (#88)")
  h.assert_equal(minutes.maximum, 1440)
  h.assert_equal(state.MINUTES_NONE, -1, "state.MINUTES_NONE is the no-op argument")

  -- #88: the attribute the 예약 시간 row rests on holds exactly that one value.
  h.assert_deep_equal(definition("schedule").attributes.minutesPick.schema.properties.value.enum,
    { state.MINUTES_PICK }, "minutesPick is the one-value enum the row rests on")
  h.assert_equal(tonumber(state.MINUTES_PICK), state.MINUTES_NONE,
    "the value the row shows and the argument it sends have to be the same number")
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

function T.test_plan_command_belongs_to_the_schedule_capability()
  -- #85: the app groups detail rows by the capability that owns them, so the
  -- row that picks WHAT a schedule runs has to be part of the schedule card;
  -- with it on the command capability the schedule card read "minutes only".
  h.assert_true(definition("schedule").attributes.planCommand ~= nil,
    "planCommand belongs to " .. caps.ids.schedule)
  h.assert_true(definition("schedule").commands.setPlanCommand ~= nil,
    "setPlanCommand belongs to " .. caps.ids.schedule)
  h.assert_nil(definition("command").attributes.planCommand,
    caps.ids.command .. " must not define planCommand any more")
  h.assert_nil(definition("command").commands.setPlanCommand,
    caps.ids.command .. " must not define setPlanCommand any more")

  -- ... and the command presentation must not mention either of them.
  local attributes, commands = references(presentation("command"), {}, {})
  h.assert_nil(attributes.planCommand, "the command presentation still reads planCommand")
  h.assert_nil(commands.setPlanCommand, "the command presentation still runs setPlanCommand")

  -- state.lua emits it under the schedule capability, not the command one.
  local used = state.attributes_used()
  h.assert_true(used[caps.ids.schedule].planCommand == true)
  h.assert_nil(used[caps.ids.command].planCommand)
end

function T.test_the_dashboard_state_is_the_power_state()
  -- §5: powerState is the one thing the dashboard tile shows; the action on
  -- it comes from the standard `switch` capability, not from ours.
  local dashboard = presentation("power_state").dashboard
  h.assert_equal(#dashboard.states, 1)
  h.assert_contains(dashboard.states[1].label, "powerState.value")
  h.assert_equal(#dashboard.actions, 0, "the switch capability supplies the action")

  for _, key in ipairs({ "command", "schedule", "status", "session", "version" }) do
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
  -- §5: conditions on powerState / active / connection / locked, actions on
  -- execute / cancel / schedule.
  local function condition_attributes(key)
    local attributes = references((presentation(key).automation or {}).conditions or {}, {}, {})
    return attributes
  end
  h.assert_true(condition_attributes("power_state").powerState == true)
  h.assert_true(condition_attributes("schedule").active == true)
  -- #83: `status` says the same thing as an enum, which is what the detail
  -- view's list needs and what reads better in a routine.
  h.assert_true(condition_attributes("schedule").status == true)
  h.assert_true(condition_attributes("status").connection == true)
  h.assert_true(condition_attributes("session").locked == true)

  local function action_commands(key)
    local _, commands = references((presentation(key).automation or {}).actions or {}, {}, {})
    return commands
  end
  h.assert_true(action_commands("command").execute == true)
  -- cancel has no automation action: `automation.actions` may not carry a
  -- pushButton (SmartThings presentation rules), and #82 removed the detail
  -- view's one too. An automation cancels with `schedule(0)`.
  h.assert_true(action_commands("schedule").schedule == true)
  -- #82: `lastAction` is a condition as well, so an automation can react to
  -- what was last asked of the PC.
  h.assert_true(condition_attributes("command").lastAction == true)
  -- #84: and to the command a schedule would run - on the schedule capability
  -- since #85.
  h.assert_true(condition_attributes("schedule").planCommand == true)
  h.assert_true(action_commands("schedule").setPlanCommand == true)
end

function T.test_an_automation_can_cancel_a_schedule_with_zero_minutes()
  -- platform notes "상세 화면(detailView) 위젯": `automation.actions` may not carry a pushButton, so `cancel()` has
  -- no action of its own and a routine cancels with `schedule(minutes: 0)`.
  -- #85 made 0 a valid argument, so the picker can offer it.
  local keys = {}
  for _, action in ipairs((presentation("schedule").automation or {}).actions or {}) do
    if (action.multiArgCommand or {}).command == "schedule" then
      for _, argument in ipairs(action.multiArgCommand.arguments or {}) do
        if argument.name == "minutes" then
          for _, alternative in ipairs((argument.list or {}).alternatives or {}) do
            keys[alternative.key] = true
          end
        end
      end
    end
  end
  h.assert_true(keys["0"] == true, "the automation's minutes picker cannot cancel")
end

--------------------------------------------------------------------------------
-- the detail view (#78 remote control, #82 final layout)
--------------------------------------------------------------------------------

--- The alternative keys of a detail-view `list`, command side and state side.
local function list_keys(item)
  local commands, states = {}, {}
  for _, alternative in ipairs(((item.list or {}).command or {}).alternatives or {}) do
    commands[#commands + 1] = alternative.key
  end
  for _, alternative in ipairs(((item.list or {}).state or {}).alternatives or {}) do
    states[#states + 1] = alternative.key
  end
  return commands, states
end

function T.test_the_action_detail_view_is_the_list_and_the_last_run()
  -- platform notes "상세 화면(detailView) 위젯" (#82): a pushButton has no value, so the phone drew "-" beside each
  -- of the eight. One list replaces them: the commands are the menu, and
  -- `lastAction` is the value it shows.
  -- #84: the list rests on `none`, so what actually ran is read off the
  -- `lastCommand` row below it.
  -- #85: the "command to schedule" row moved to the schedule card, where the
  -- app draws the rows of the capability that owns them.
  local detail = presentation("command").detailView
  h.assert_equal(#detail, 2, "command list, last run")
  local item = detail[1]
  h.assert_equal(item.displayType, "list")
  h.assert_equal(item.label, "{{i18n.attributes.lastAction.label}}")
  h.assert_equal(item.list.command.name, "execute")

  local commands, states = list_keys(item)
  h.assert_deep_equal(commands, ACTION_LIST)
  h.assert_equal(item.list.state.value, "lastAction.value")
  h.assert_deep_equal(states,
    definition("command").attributes.lastAction.schema.properties.value.enum,
    "the state alternatives must cover the whole enum, `none` included")

  for _, key in ipairs(commands) do
    h.assert_true(key ~= "forceshutdown", "forceshutdown must not be on screen")
    h.assert_true(key ~= "none", "`none` is what the row rests on, not a menu entry")
  end
  for _, alternative in ipairs(item.list.command.alternatives) do
    h.assert_true(type(alternative.value) == "string" and alternative.value ~= "",
      "a command alternative needs a literal label (platform notes '번역': arguments cannot be translated)")
  end

  -- #84: the feedback row the 5 s flash was replaced with.
  h.assert_equal(detail[2].displayType, "state")
  h.assert_equal(detail[2].label, "{{i18n.attributes.lastCommand.label}}")
  h.assert_contains(detail[2].state.label, "lastCommand.value")
end

-- #84, measured on the phone (2026-09-22): closing a detailView `list` without
-- picking anything sends the row's CURRENT state value as the command
-- argument. `lastAction` was `none`, which `execute` did not accept, and the
-- cloud answered "network or server error" without ever reaching the hub. So
-- every value a list's state can hold has to be an argument the command takes.
--- Is `key` something `schema` accepts as an argument value? Returns a reason
--- when it is not. Enums are matched by membership, numbers by their range -
--- the two shapes a capability argument can have here.
local function argument_error(schema, key)
  schema = schema or {}
  if schema.enum then
    for _, value in ipairs(schema.enum) do
      if tostring(value) == tostring(key) then
        return nil
      end
    end
    return "not one of " .. table.concat(schema.enum, "/")
  end
  if schema.type == "integer" or schema.type == "number" then
    local number = tonumber(key)
    if not number then
      return "not a number"
    end
    if schema.type == "integer" and number ~= math.floor(number) then
      return "not an integer"
    end
    if schema.minimum and number < schema.minimum then
      return "below the minimum (" .. tostring(schema.minimum) .. ")"
    end
    if schema.maximum and number > schema.maximum then
      return "above the maximum (" .. tostring(schema.maximum) .. ")"
    end
    return nil
  end
  return nil
end

-- #85, measured on the phone: the cloud validates every command argument
-- against the capability definition and answers "system error" without ever
-- forwarding the command, so a list key outside the definition's schema is
-- dead on arrival. That is what the schedule row's `취소` entry (minutes = 0,
-- `minimum: 1`) was. #84's rule is the same defect one layer up - a dismissed
-- list sends the row's CURRENT value as the argument - so both are checked
-- here: every key a list can send, command side and state side, detail view and
-- automation action, has to be a valid argument.
function T.test_every_list_key_is_a_valid_command_argument()
  local checked = 0

  --- Check one set of keys against the schema of `argument_name` of `command`.
  local function check(key, command_name, argument_name, keys, where)
    local command = (definition(key).commands or {})[command_name]
    h.assert_true(command ~= nil,
      string.format("%s runs %s(), which is not defined", where, tostring(command_name)))
    local schema
    for i, argument in ipairs(command.arguments or {}) do
      if argument.name == argument_name or (argument_name == nil and i == 1) then
        schema = argument.schema
        break
      end
    end
    h.assert_true(schema ~= nil,
      string.format("%s sends %s(%s), which the definition has no argument for",
        where, tostring(command_name), tostring(argument_name)))
    for _, value in ipairs(keys) do
      local err = argument_error(schema, value)
      h.assert_nil(err, string.format("%s: %s() would reject %s - %s (#85: the cloud "
        .. "validates arguments against the definition)",
        where, command_name, tostring(value), tostring(err)))
      checked = checked + 1
    end
  end

  for key, id in pairs(caps.ids) do
    for i, item in ipairs(presentation(key).detailView or {}) do
      if item.displayType == "list" then
        local where = string.format("%s detailView[%d]", id, i)
        local command_name = item.list.command.name
        local commands, states = list_keys(item)
        -- The menu itself: every entry the user can pick.
        check(key, command_name, nil, commands, where)
        -- #84: and whatever the row can be left showing, because closing the
        -- list without a pick sends that value as the argument. Both the
        -- declared alternatives and the bound attribute's own enum count.
        --
        -- #88: for EVERY list, not only the ones whose argument is an enum.
        -- The carve-out used to say that a row bound to an integer argument
        -- shows a word the app cannot send as a number and that the phone
        -- therefore leaves such a picker alone. It does not: the 예약 시간 row
        -- rested on `status` and a dismissed picker sent
        -- `schedule(minutes: "idle")`, which the cloud rejected with a network
        -- error before the hub ever saw it. So an integer argument's row has to
        -- rest on an integer inside [minimum, maximum] just as an enum
        -- argument's row has to rest on a member of the enum.
        local attr = (item.list.state.value or ""):match("^([%a][%w_]*)%.value$")
        local spec = ((((definition(key).attributes or {})[attr] or {}).schema or {})
          .properties or {}).value or {}
        check(key, command_name, nil, states, where .. " state")
        check(key, command_name, nil, spec.enum or {}, where .. " " .. tostring(attr))
      end
    end

    -- platform notes "automation 프레젠테이션": an automation action is a multiArgCommand whose argument widgets
    -- carry the argument name, so each list is checked against its own
    -- argument rather than the first one.
    for i, action in ipairs((presentation(key).automation or {}).actions or {}) do
      local multi = action.multiArgCommand
      if multi then
        for _, argument in ipairs(multi.arguments or {}) do
          local keys = {}
          for _, alternative in ipairs((argument.list or {}).alternatives or {}) do
            keys[#keys + 1] = alternative.key
          end
          if #keys > 0 then
            check(key, multi.command, argument.name,
              keys, string.format("%s automation.actions[%d]", id, i))
          end
        end
      end
    end
  end

  h.assert_true(checked >= 20, "far too few list keys were checked: " .. checked)
end

-- #88, measured on the phone (2026-09-22): the same defect as #84, one type
-- further. The 예약 시간 row's command takes an integer and the row was bound to
-- `status` ("idle" / "scheduled"), so dismissing the list without a choice sent
-- `schedule(minutes: "idle")` and the cloud answered "네트워크 오류" without ever
-- forwarding it. The rule below is what the old shape broke: a list bound to an
-- integer argument may only rest on integers inside the argument's range, and
-- the resting value has to be a no-op (-1, by the convention this driver keeps
-- for the same reason `execute` rests on `none`).
function T.test_an_integer_argument_list_rests_on_a_number_in_its_range()
  local checked = 0
  for key, id in pairs(caps.ids) do
    for i, item in ipairs(presentation(key).detailView or {}) do
      if item.displayType == "list" then
        local command = (definition(key).commands or {})[item.list.command.name] or {}
        local schema = ((command.arguments or {})[1] or {}).schema or {}
        if schema.type == "integer" or schema.type == "number" then
          local where = string.format("%s detailView[%d] (%s)", id, i, item.list.command.name)
          -- Every value the row can be left showing: the declared alternatives
          -- and, because the app reads the attribute rather than the
          -- presentation, the whole enum of the attribute it is bound to.
          local attr = (item.list.state.value or ""):match("^([%a][%w_]*)%.value$")
          local spec = ((((definition(key).attributes or {})[attr] or {}).schema or {})
            .properties or {}).value or {}
          local values = {}
          for _, alternative in ipairs(((item.list or {}).state or {}).alternatives or {}) do
            values[#values + 1] = alternative.key
          end
          for _, value in ipairs(spec.enum or {}) do
            values[#values + 1] = value
          end
          h.assert_true(#values > 0, where .. " state can hold nothing at all")
          for _, value in ipairs(values) do
            local number = tonumber(value)
            h.assert_true(number ~= nil, string.format(
              "%s rests on %s, which %s() cannot take as a number - a dismissed "
              .. "list sends the row's current value (#88)",
              where, tostring(value), item.list.command.name))
            h.assert_true(number == math.floor(number),
              where .. " rests on " .. tostring(value) .. ", which is not an integer")
            h.assert_true(number >= (schema.minimum or -math.huge), string.format(
              "%s rests on %s, below the argument's minimum (%s)",
              where, tostring(value), tostring(schema.minimum)))
            h.assert_true(number <= (schema.maximum or math.huge), string.format(
              "%s rests on %s, above the argument's maximum (%s)",
              where, tostring(value), tostring(schema.maximum)))
            -- ... and inside the range is not enough: the value the row rests
            -- on is sent for real, so it has to do nothing. 0 already means
            -- "cancel the schedule", hence the negative no-op.
            h.assert_true(number < 0, string.format(
              "%s rests on %s, which would %s() something the user never asked for "
              .. "(the no-op is -1)", where, tostring(value), item.list.command.name))
            checked = checked + 1
          end
        end
      end
    end
  end
  h.assert_true(checked > 0, "the 예약 시간 row is a list of integers; none was checked")
end

function T.test_the_driver_never_leaves_a_flash_timer_behind()
  -- #84: `lastAction` used to show the command that ran and reset itself five
  -- seconds later. The row must now rest on `none`, so neither the timer nor
  -- the function that scheduled it may come back.
  local poll = require "poll"
  h.assert_nil(poll.flash_action, "poll.flash_action is gone (#84)")
  h.assert_nil(poll.ACTION_RESET_SECONDS)
  h.assert_nil(poll.ACTION_RESET_TIMER_FIELD)
end

function T.test_the_schedule_detail_view_is_the_plan_the_presets_and_the_summary()
  -- #82: the cancel pushButton became the `0` entry of the preset list.
  -- #83: the list's value is the `status` enum, not the boolean `active` - a
  -- list bound to a boolean drew "-" with no chevron and never opened.
  -- #85: "what to schedule" comes first, because the card was read as "pick a
  -- time" while that row sat in the command card two cards further down.
  local detail = presentation("schedule").detailView
  h.assert_equal(#detail, 3, "command to schedule, presets, summary")

  local plan = detail[1]
  h.assert_equal(plan.displayType, "list")
  h.assert_equal(plan.label, "{{i18n.attributes.planCommand.label}}")
  h.assert_equal(plan.list.command.name, "setPlanCommand")
  local plan_commands, plan_states = list_keys(plan)
  h.assert_deep_equal(plan_commands, PLAN_LIST)
  h.assert_equal(plan.list.state.value, "planCommand.value")
  h.assert_deep_equal(plan_states, PLAN_LIST)

  local item = detail[2]
  h.assert_equal(item.displayType, "list")
  h.assert_equal(item.list.command.name, "schedule")
  local minutes, states = list_keys(item)
  h.assert_deep_equal(minutes, { "5", "15", "30", "60", "120", "0" })
  h.assert_equal(item.list.command.argumentType, "integer",
    "a list of integer arguments needs argumentType (platform notes '상세 화면(detailView) 위젯')")
  -- #88: the row rests on `minutesPick`, not on `status`. A dismissed picker
  -- sends the row's current value as the argument, and "idle"/"scheduled" is
  -- not a number the cloud will forward - that was the network-error popup.
  h.assert_equal(item.list.state.value, "minutesPick.value")
  h.assert_deep_equal(states,
    definition("schedule").attributes.minutesPick.schema.properties.value.enum,
    "the state alternatives must cover the whole minutesPick enum")
  h.assert_deep_equal(states, { "-1" }, "the row rests on the no-op delay")

  h.assert_equal(detail[3].displayType, "state")
  h.assert_contains(detail[3].state.label, "summary.value")
end

function T.test_every_schedule_preset_is_inside_the_definitions_range()
  -- #85, measured on the phone: the cloud checks a command's arguments against
  -- the definition and answers "system error" without ever reaching the hub.
  -- The `취소` entry sent `minutes = 0` against `minimum: 1` and died there.
  local schema = definition("schedule").commands.schedule.arguments[1].schema
  local presets = select(1, list_keys(presentation("schedule").detailView[2]))
  h.assert_true(#presets > 0, "the schedule row has no presets")
  local zero = false
  for _, key in ipairs(presets) do
    local minutes = tonumber(key)
    h.assert_true(minutes ~= nil, "the preset " .. tostring(key) .. " is not a number")
    h.assert_true(minutes >= schema.minimum,
      string.format("the preset %s is below minutes' minimum (%s)", key, tostring(schema.minimum)))
    h.assert_true(minutes <= schema.maximum,
      string.format("the preset %s is above minutes' maximum (%s)", key, tostring(schema.maximum)))
    zero = zero or minutes == 0
  end
  h.assert_true(zero, "the schedule row has no Cancel entry (minutes = 0)")
end

function T.test_the_info_detail_view_is_only_the_summary()
  -- #86, measured on the phone: two `state` rows of the SAME capability are
  -- laid out side by side in two narrow columns and both texts are cut off with
  -- "…". That is what "상태" and "버전" looked like while they shared pcInfo, so
  -- the version row moved to `pcVersion` and this card is one row again.
  local detail = presentation("status").detailView
  h.assert_equal(#detail, 1, "the status summary, and nothing else (#86)")
  h.assert_equal(detail[1].displayType, "state")
  h.assert_contains(detail[1].state.label, "summary.value")

  -- The attribute stays defined and emitted: changing a definition means a new
  -- capability id (platform notes "허브의 정의 캐시"), and an attribute that is never emitted keeps the app
  -- saying the device has not reported all of its state.
  h.assert_true(definition("status").attributes.versions ~= nil,
    caps.ids.status .. " must keep defining versions (#86)")
  h.assert_true(state.attributes_used()[caps.ids.status].versions == true,
    "the driver must keep emitting " .. caps.ids.status .. ".versions (#86)")
end

function T.test_the_version_detail_view_is_the_one_versions_row()
  -- #85's row, on its own capability since #86 and therefore full width.
  local detail = presentation("version").detailView
  h.assert_equal(#detail, 1, "one state row, so the app draws it full width")
  h.assert_equal(detail[1].displayType, "state")
  h.assert_equal(detail[1].label, "{{i18n.attributes.versions.label}}")
  h.assert_contains(detail[1].state.label, "versions.value")
  h.assert_true(definition("version").attributes.versions ~= nil,
    caps.ids.version .. " does not define versions")
  h.assert_nil(next(definition("version").commands or {}),
    "the version card is read-only")
  h.assert_equal(#((presentation("version").automation or {}).conditions or {}), 0,
    "a version string is not a useful automation condition")
end

-- #83, measured on the phone: a detailView `list` whose `state.value` points at
-- a boolean attribute is not drawn as a picker at all. Only a string/enum
-- attribute works, so every list state is checked against the definition.
function T.test_no_detail_list_binds_its_state_to_a_boolean()
  local checked = 0
  for key, id in pairs(caps.ids) do
    for i, item in ipairs(presentation(key).detailView or {}) do
      if item.displayType == "list" then
        local value = ((item.list or {}).state or {}).value or ""
        local attr = value:match("^([%a][%w_]*)%.value$")
        h.assert_true(attr ~= nil,
          string.format("%s detailView[%d] state.value is not an attribute reference", id, i))
        local spec = (definition(key).attributes or {})[attr]
        h.assert_true(spec ~= nil,
          string.format("%s detailView[%d] reads %s, which is not defined", id, i, attr))
        local schema = ((spec.schema or {}).properties or {}).value or {}
        h.assert_equal(schema.type, "string",
          string.format("%s detailView[%d] binds its list to %s (%s); a boolean list "
            .. "does not render (#83)", id, i, attr, tostring(schema.type)))
        h.assert_true(#(schema.enum or {}) > 0,
          string.format("%s detailView[%d] binds its list to %s, which is not an enum",
            id, i, attr))
        checked = checked + 1
      end
    end
  end
  h.assert_true(checked >= 2, "the action and schedule rows are both lists")
end

-- #83, measured on the phone: the translation files are NOT applied to
-- attribute *values* - the app showed "On" and "None" with a Korean locale.
-- The only text the user reads is the one in the presentation, so every value
-- label of the three enum-valued capabilities is written "한국어 (English)",
-- the convention the command menus already used.
local BILINGUAL_VALUE_CAPABILITIES = { "power_state", "command", "schedule" }

local function assert_bilingual(alternatives, where)
  h.assert_true(#(alternatives or {}) > 0, where .. " has no alternatives")
  for _, alternative in ipairs(alternatives) do
    local value = alternative.value
    h.assert_true(type(value) == "string" and value ~= "", where .. " has an empty value")
    h.assert_true(value:find("(", 1, true) ~= nil,
      string.format("%s: %s is not bilingual (\"한국어 (English)\", platform notes '번역')",
        where, tostring(value)))
  end
end

function T.test_every_state_value_label_is_bilingual()
  for _, key in ipairs(BILINGUAL_VALUE_CAPABILITIES) do
    local id = caps.ids[key]
    local doc = presentation(key)
    for i, item in ipairs(doc.dashboard.states or {}) do
      assert_bilingual(item.alternatives, string.format("%s dashboard.states[%d]", id, i))
    end
    for i, item in ipairs(doc.detailView or {}) do
      local alternatives
      if item.displayType == "list" then
        alternatives = ((item.list or {}).state or {}).alternatives
      elseif item.displayType == "state" then
        alternatives = (item.state or {}).alternatives
      end
      if alternatives then
        assert_bilingual(alternatives, string.format("%s detailView[%d]", id, i))
      end
    end
    for i, condition in ipairs((doc.automation or {}).conditions or {}) do
      assert_bilingual((condition.list or {}).alternatives,
        string.format("%s automation.conditions[%d]", id, i))
    end
  end
end

function T.test_no_detail_row_is_a_push_button()
  -- #82, measured on the hub: a pushButton row has no value, so the phone
  -- renders "-" next to its label. Every row is a value now.
  for key, id in pairs(caps.ids) do
    for i, item in ipairs(presentation(key).detailView or {}) do
      h.assert_true(item.displayType ~= "pushButton",
        string.format("%s detailView[%d] is a pushButton, which renders as \"-\"", id, i))
      h.assert_true(item.displayType ~= "multiArgCommand",
        "multiArgCommand is rejected in a detailView (platform notes 'automation 프레젠테이션')")
    end
  end
end

function T.test_every_detail_list_carries_state_alternatives()
  -- platform notes "상세 화면(detailView) 위젯": the presentation API requires `state.alternatives` whenever a
  -- detailView list has a `state`, and a list without one shows nothing.
  local lists = 0
  for key, id in pairs(caps.ids) do
    for i, item in ipairs(presentation(key).detailView or {}) do
      if item.displayType == "list" then
        lists = lists + 1
        local where = string.format("%s detailView[%d]", id, i)
        h.assert_true(type((item.list or {}).state) == "table", where .. " has no state")
        h.assert_true(type(item.list.state.value) == "string", where .. " state has no value")
        h.assert_true(#(item.list.state.alternatives or {}) > 0,
          where .. " state has no alternatives (the API rejects that)")
        h.assert_true(type((item.list or {}).command) == "table",
          where .. " list has no command object")
        h.assert_true(type(item.list.command.name) == "string",
          where .. " command needs a `name` (platform notes 'automation 프레젠테이션')")
      end
    end
  end
  h.assert_true(lists >= 2, "the action and schedule rows are both lists")
end

function T.test_no_automation_action_uses_a_push_button()
  -- platform notes "automation 프레젠테이션": the presentation API rejects pushButton in automation.actions.
  for key, id in pairs(caps.ids) do
    for _, action in ipairs((presentation(key).automation or {}).actions or {}) do
      h.assert_true(action.displayType ~= "pushButton",
        id .. " has a pushButton in automation.actions, which the API rejects")
    end
  end
end

-- Attributes that are still defined and emitted but no longer have a row of
-- their own in the detail view (#78): the summaries replaced them.
-- #82 put `active` back on screen as the value of the schedule list; #83 moved
-- that job to the `status` enum (a list cannot read a boolean), so `active` is
-- a condition-only attribute again.
-- #86 added `versions` here: pcInfo still defines and emits it, but the row on
-- screen belongs to `pcVersion` - two state rows of one capability are drawn as
-- two narrow, truncated columns.
local RAW_ROWS_REMOVED = {
  schedule = { "remainingSeconds", "executeAt", "origin", "command", "active" },
  status = {
    "serviceVersion", "updateAvailable", "wolReady", "lastSeen", "connection",
    "versions",
  },
  session = { "idleMinutes", "locked", "user" },
}

function T.test_the_detail_views_show_summaries_instead_of_raw_attributes()
  for key, removed in pairs(RAW_ROWS_REMOVED) do
    local defined = definition(key).attributes
    for _, attr in ipairs(removed) do
      h.assert_true(defined[attr] ~= nil,
        attr .. " must stay defined even though the detail view dropped it")
    end
    -- `state` rows are what the user reads; a visibleCondition may still name
    -- the attribute it keys on, so only the row labels are inspected here.
    for _, item in ipairs(presentation(key).detailView or {}) do
      if item.displayType == "state" then
        local label = (item.state or {}).label or ""
        for _, attr in ipairs(removed) do
          h.assert_equal(label:find(attr .. ".value", 1, true), nil,
            caps.ids[key] .. " detail view still shows the raw " .. attr)
        end
      end
    end
  end
  for _, key in ipairs({ "schedule", "status", "session" }) do
    local seen = false
    for _, item in ipairs(presentation(key).detailView or {}) do
      if item.displayType == "state" and ((item.state or {}).label or ""):find("summary.value", 1, true) then
        seen = true
      end
    end
    h.assert_true(seen, caps.ids[key] .. " has no summary row")
  end
end

--------------------------------------------------------------------------------
-- #86: no detail row may ever read "-"
--------------------------------------------------------------------------------

--- The attribute a `state` row shows, from its `{{<attr>.value}}` label.
local function state_row_attribute(item)
  return (((item.state or {}).label) or ""):match("{{([%a][%w_]*)%.value}}")
end

--- Every detailView `state` row of every capability.
local function detail_state_rows()
  local rows = {}
  for key, id in pairs(caps.ids) do
    for i, item in ipairs(presentation(key).detailView or {}) do
      if item.displayType == "state" then
        local attr = state_row_attribute(item)
        h.assert_true(attr ~= nil, string.format(
          "%s detailView[%d] is a state row whose label reads no attribute", id, i))
        rows[#rows + 1] = { key = key, id = id, attr = attr,
          where = string.format("%s detailView[%d] (%s)", id, i, attr) }
      end
    end
  end
  return rows
end

function T.test_no_detail_state_row_is_ever_emitted_empty()
  -- Measured on the phone 2026-09-22 (#86): a row whose value is the empty
  -- string is drawn as "-", exactly like one that was never emitted, and the
  -- topmost such row showed a "-" with no visible label at all. So every row
  -- the user reads has to carry a sentence in every state the driver can be in:
  -- freshly added (`initial_rows`), unreachable-but-answered, and a full status
  -- body. That is what turned `lastCommand`'s "" into "없음 (None)".
  local full = {
    service_version = "v1.1.0",
    secret_set = true,
    wol = { ready = true },
    update = { available = false },
    last_command = { command = "lock", origin = "smartthings", at = "2026-09-22T16:16:00+09:00" },
    schedule = { active = true, command = "shutdown", remaining_seconds = 240,
      execute_at = "2026-09-22T23:10:00+09:00", origin = "smartthings" },
    session = { exposed = true, locked = true, idle_seconds = 1200, user = "kim" },
  }
  local samples = {}
  for _, lang in ipairs({ "ko", "en" }) do
    samples[#samples + 1] = state.initial_rows(lang)
    -- An empty body: everything the row says has to come from the driver.
    samples[#samples + 1] = state.apply_status(state.new(), {}, { lang = lang })
    samples[#samples + 1] = state.apply_status(state.new("on"), full,
      { lang = lang, now = "23:05:00" })
  end

  local rows = detail_state_rows()
  h.assert_true(#rows >= 5, "far too few state rows were checked: " .. #rows)
  for _, row in ipairs(rows) do
    local seen = false
    for _, events in ipairs(samples) do
      for _, e in ipairs(events) do
        if e.cap == row.id and e.attr == row.attr then
          seen = true
          h.assert_true(type(e.value) == "string" and e.value ~= "",
            row.where .. ' would read "-": the driver emits ' .. h.render(e.value))
        end
      end
    end
    h.assert_true(seen, row.where .. " is a row the driver never emits a value for")
  end
end

function T.test_no_detail_row_relies_on_a_visible_condition()
  -- The phone ignored visibleCondition on capability presentations (2026-09-22),
  -- so every row must read sensibly on its own (platform notes "상세 화면(detailView) 위젯").
  for key in pairs(caps.ids) do
    for _, item in ipairs(presentation(key).detailView or {}) do
      h.assert_nil(item.visibleCondition, caps.ids[key] .. " detailView still uses visibleCondition")
    end
  end
end

--------------------------------------------------------------------------------
-- translations (#78)
--------------------------------------------------------------------------------

local TAGS = { "ko", "en" }
local translations_dir = caps_dir .. "/translations"

local function read_in(dir, name)
  local path = dir .. "/" .. name
  if host and host.readfile then
    local text, err = host.readfile(path)
    if not text then
      return nil, tostring(err)
    end
    return text
  end
  local file, err = io.open(path, "r")
  if not file then
    return nil, tostring(err)
  end
  local text = file:read("a")
  file:close()
  return text
end

-- "<key>.<tag>" -> decoded table or { __error = ... }.
local translations = {}

for key in pairs(caps.ids) do
  local base = file_for(key):gsub("%.json$", "")
  for _, tag in ipairs(TAGS) do
    local name = base .. "." .. tag .. ".json"
    local text, err = read_in(translations_dir, name)
    if not text then
      translations[key .. "." .. tag] = { __error = "cannot read " .. name .. ": " .. tostring(err) }
    else
      local ok, decoded = pcall(json.decode, text)
      translations[key .. "." .. tag] = ok and decoded or { __error = name .. " is not valid JSON: " .. tostring(decoded) }
    end
  end
end

local function translation(key, tag)
  return translations[key .. "." .. tag]
end

function T.test_every_capability_has_a_korean_and_an_english_translation()
  for key, id in pairs(caps.ids) do
    for _, tag in ipairs(TAGS) do
      local doc = translation(key, tag)
      h.assert_true(doc.__error == nil, tostring(doc.__error))
      h.assert_equal(doc.tag, tag, id .. " " .. tag .. " tag")
      h.assert_true(type(doc.label) == "string" and doc.label ~= "",
        id .. " " .. tag .. " has no capability label")
    end
  end
end

function T.test_translations_cover_every_attribute_and_enum_value()
  for key, id in pairs(caps.ids) do
    for _, tag in ipairs(TAGS) do
      local doc = translation(key, tag)
      local translated = doc.attributes or {}
      for attr, spec in pairs(definition(key).attributes or {}) do
        local where = string.format("%s %s %s", id, tag, attr)
        local entry = translated[attr]
        h.assert_true(type(entry) == "table", where .. " is not translated")
        h.assert_true(type(entry.label) == "string" and entry.label ~= "",
          where .. " has no label")
        local enum = ((spec.schema or {}).properties or {}).value or {}
        for _, value in ipairs(enum.enum or {}) do
          local localised = (((entry.i18n or {}).value or {})[value] or {}).label
          h.assert_true(type(localised) == "string" and localised ~= "",
            where .. "." .. value .. " has no label")
        end
      end
      for attr in pairs(translated) do
        h.assert_true((definition(key).attributes or {})[attr] ~= nil,
          string.format("%s %s translates %s, which is not defined", id, tag, attr))
      end
    end
  end
end

function T.test_translations_cover_every_command_and_argument()
  for key, id in pairs(caps.ids) do
    for _, tag in ipairs(TAGS) do
      local doc = translation(key, tag)
      local translated = doc.commands or {}
      for name, command in pairs(definition(key).commands or {}) do
        local where = string.format("%s %s %s()", id, tag, name)
        local entry = translated[name]
        h.assert_true(type(entry) == "table", where .. " is not translated")
        h.assert_true(type(entry.label) == "string" and entry.label ~= "",
          where .. " has no label")
        for _, argument in ipairs(command.arguments or {}) do
          -- Argument translations are keyed by argument name and carry a
          -- label only: the translations API rejects per-value i18n for
          -- command arguments in every shape we tried (platform notes "번역"), so
          -- enum values of arguments stay untranslated in the Routine picker.
          local argument_entry = (entry.arguments or {})[argument.name]
          h.assert_true(type(argument_entry) == "table",
            where .. " argument " .. tostring(argument.name) .. " is not translated")
          h.assert_true(type(argument_entry.label) == "string" and argument_entry.label ~= "",
            where .. " argument " .. tostring(argument.name) .. " has no label")
          h.assert_true(argument_entry.i18n == nil,
            where .. " argument " .. tostring(argument.name) .. " must not carry i18n (API rejects it)")
        end
      end
      for name in pairs(translated) do
        h.assert_true((definition(key).commands or {})[name] ~= nil,
          string.format("%s %s translates %s(), which is not defined", id, tag, name))
      end
    end
  end
end

function T.test_every_last_action_value_is_translated()
  -- #82: the list row reads its text from these, `none` included — an
  -- untranslated value shows the raw enum key next to eight Korean ones.
  for _, tag in ipairs(TAGS) do
    local values = ((translation("command", tag).attributes or {}).lastAction or {}).i18n or {}
    for _, value in ipairs(definition("command").attributes.lastAction.schema.properties.value.enum) do
      local label = ((values.value or {})[value] or {}).label
      h.assert_true(type(label) == "string" and label ~= "",
        "lastAction." .. value .. " has no " .. tag .. " label")
    end
  end
end

function T.test_the_versions_row_has_a_label_in_both_languages()
  -- #85: a row label is one of the few things translations really do reach
  -- (platform notes "번역"), so this is where the user's "버전" / "Versions" comes from.
  -- #86: the row is on `pcVersion` now; pcInfo keeps the attribute, and its
  -- label with it, because the definition cannot change without a rename.
  for _, key in ipairs({ "version", "status" }) do
    h.assert_equal(((translation(key, "ko").attributes or {}).versions or {}).label, "버전")
    h.assert_equal(((translation(key, "en").attributes or {}).versions or {}).label, "Versions")
  end
  h.assert_equal(translation("version", "ko").label, "PC 버전")
  h.assert_equal(translation("version", "en").label, "PC version")
end

function T.test_the_korean_translation_is_actually_korean()
  -- A copy-pasted English file would pass every structural check above.
  for key, id in pairs(caps.ids) do
    local doc = translation(key, "ko")
    local hangul = false
    local function walk(node)
      if type(node) == "string" then
        -- Hangul syllables are U+AC00..U+D7A3, i.e. lead bytes 0xEA..0xED.
        if node:find("[\234-\237][\128-\191][\128-\191]") then
          hangul = true
        end
      elseif type(node) == "table" then
        for _, value in pairs(node) do
          walk(value)
        end
      end
    end
    walk(doc)
    h.assert_true(hangul, id .. " ko translation has no Hangul in it")
  end
end

return T
