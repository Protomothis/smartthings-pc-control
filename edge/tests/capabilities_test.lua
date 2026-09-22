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

function T.test_every_pc_profile_uses_the_same_ids()
  -- #79 keeps one file per version, so the check runs over all of them: an
  -- id that is only in the old file would ship a half-broken new profile.
  -- #81 removed the child profiles, so every file left here is a PC profile.
  local checked = 0
  for name, text in pairs(profile_files) do
    checked = checked + 1
    for _, id in pairs(caps.ids) do
      h.assert_contains(text, id, "profiles/" .. name .. " is missing ")
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
  -- §4.1: `noSecret` is deliberately not a connection value; the warning goes
  -- into pcHealth.message instead.
  local connection = definition("status").attributes.connection.schema.properties.value
  h.assert_deep_equal(connection.enum, { "ok", "unauthorized", "unreachable", "incompatible" })
end

--------------------------------------------------------------------------------
-- commands
--------------------------------------------------------------------------------

-- The commands init.lua registers handlers for (§5.1). The eight no-argument
-- ones are the remote-control buttons of the detail view (#78); `execute` stays
-- for automations, where the mode and the delay are worth asking about.
local REMOTE_BUTTONS = {
  "wake", "suspend", "hibernate", "restart", "shutdown", "lock",
  "screenOff", "screenOn",
}

local EXPECTED_COMMANDS = {
  power_state = {},
  command = { execute = { "command", "mode", "minutes" } },
  schedule = { cancel = {}, schedule = { "minutes", "command" } },
  status = {},
  session = {},
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
  -- §4.3: the command names the service accepts. `ping` is the driver's own
  -- reachability probe and is not offered in the app.
  local execute = definition("command").commands.execute.arguments[1].schema.enum
  local seen = {}
  for _, name in ipairs(execute) do
    seen[name] = true
  end
  for _, name in ipairs({ "shutdown", "forceshutdown", "restart", "hibernate",
      "suspend", "lock", "turnscreenoff", "turnscreenon" }) do
    h.assert_true(seen[name] == true, "pcControl.execute is missing " .. name)
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

--------------------------------------------------------------------------------
-- the remote-control detail view (#78)
--------------------------------------------------------------------------------

function T.test_the_command_detail_view_is_one_button_per_command()
  -- §5.3: a vertical list of pushButtons, in the order of the issue, with
  -- forceshutdown deliberately left off the screen (automation only).
  local detail = presentation("command").detailView
  local buttons = {}
  for _, item in ipairs(detail) do
    if item.displayType == "pushButton" then
      buttons[#buttons + 1] = item.pushButton.command
    end
  end
  h.assert_deep_equal(buttons, REMOTE_BUTTONS)
  for _, item in ipairs(detail) do
    h.assert_true(item.displayType ~= "multiArgCommand",
      "multiArgCommand is rejected in a detailView (§14)")
  end
  local _, commands = references(detail, {}, {})
  h.assert_true(commands.forceshutdown == nil, "forceshutdown must not be on screen")
end

function T.test_no_automation_action_uses_a_push_button()
  -- §14: the presentation API rejects pushButton in automation.actions.
  for key, id in pairs(caps.ids) do
    for _, action in ipairs((presentation(key).automation or {}).actions or {}) do
      h.assert_true(action.displayType ~= "pushButton",
        id .. " has a pushButton in automation.actions, which the API rejects")
    end
  end
end

-- Attributes that are still defined and emitted but no longer have a row of
-- their own in the detail view (#78): the summaries replaced them.
local RAW_ROWS_REMOVED = {
  schedule = { "remainingSeconds", "executeAt", "origin", "command", "active" },
  status = { "serviceVersion", "updateAvailable", "wolReady", "lastSeen", "connection" },
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

function T.test_the_conditional_rows_are_guarded_by_a_visible_condition()
  -- #78. If the API rejects `visibleCondition` the fallback is to drop these
  -- objects; README and the design doc §14 record that.
  local function condition_of(key, needle)
    for _, item in ipairs(presentation(key).detailView or {}) do
      local label = ((item.state or {}).label) or ((item.pushButton or {}).command) or ""
      if label:find(needle, 1, true) then
        return item.visibleCondition
      end
    end
    return nil
  end

  local function check(condition, capability, value, operator, where)
    h.assert_true(type(condition) == "table", where .. " has no visibleCondition")
    h.assert_equal(condition.capability, capability, where .. " capability")
    h.assert_equal(condition.component, "main", where .. " component")
    h.assert_equal(condition.version, 1, where .. " version")
    h.assert_equal(condition.value, value, where .. " value")
    h.assert_equal(condition.operator, operator, where .. " operator")
  end

  check(condition_of("status", "message.value"), caps.STATUS,
    "message.value", "NOT_EQUALS", "pcHealth message row")
  check(condition_of("schedule", "summary.value"), caps.SCHEDULE,
    "active.value", "EQUALS", "pcTimer summary row")
  check(condition_of("schedule", "cancel"), caps.SCHEDULE,
    "active.value", "EQUALS", "pcTimer cancel button")
  check(condition_of("session", "summary.value"), caps.SESSION,
    "exposed.value", "EQUALS", "pcUser summary row")
  h.assert_true(condition_of("schedule", "summary.value").operand == true)
  h.assert_equal(condition_of("status", "message.value").operand, "")
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
          -- command arguments in every shape we tried (design §14.1), so
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
