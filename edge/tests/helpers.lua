-- Assertion helpers for the test suite. Every failure raises with a message
-- that says what was expected, so run.lua only has to print it.

local h = {}

local function render(value)
  if type(value) == "string" then
    return string.format("%q", value)
  end
  if type(value) ~= "table" then
    return tostring(value)
  end
  local keys = {}
  for k in pairs(value) do
    keys[#keys + 1] = k
  end
  table.sort(keys, function(a, b) return tostring(a) < tostring(b) end)
  local parts = {}
  for _, k in ipairs(keys) do
    parts[#parts + 1] = tostring(k) .. " = " .. render(value[k])
  end
  return "{ " .. table.concat(parts, ", ") .. " }"
end

h.render = render

function h.fail(message)
  error(message, 2)
end

function h.assert_equal(actual, expected, context)
  if actual ~= expected then
    error(string.format("%sexpected %s, got %s",
      context and (context .. ": ") or "", render(expected), render(actual)), 2)
  end
end

function h.assert_true(value, context)
  if value ~= true then
    error(string.format("%sexpected true, got %s",
      context and (context .. ": ") or "", render(value)), 2)
  end
end

function h.assert_false(value, context)
  if value ~= false then
    error(string.format("%sexpected false, got %s",
      context and (context .. ": ") or "", render(value)), 2)
  end
end

function h.assert_nil(value, context)
  if value ~= nil then
    error(string.format("%sexpected nil, got %s",
      context and (context .. ": ") or "", render(value)), 2)
  end
end

local function deep_equal(a, b)
  if a == b then
    return true
  end
  if type(a) ~= "table" or type(b) ~= "table" then
    return false
  end
  for k, v in pairs(a) do
    if not deep_equal(v, b[k]) then
      return false
    end
  end
  for k in pairs(b) do
    if a[k] == nil then
      return false
    end
  end
  return true
end

h.deep_equal = deep_equal

function h.assert_deep_equal(actual, expected, context)
  if not deep_equal(actual, expected) then
    error(string.format("%sexpected %s, got %s",
      context and (context .. ": ") or "", render(expected), render(actual)), 2)
  end
end

--- Assert that `fn` raises. Returns the error message.
function h.assert_error(fn, context)
  local ok, err = pcall(fn)
  if ok then
    error(string.format("%sexpected an error, but the call succeeded",
      context and (context .. ": ") or ""), 2)
  end
  return err
end

function h.assert_contains(haystack, needle, context)
  if type(haystack) ~= "string" or not haystack:find(needle, 1, true) then
    error(string.format("%sexpected %s to contain %s",
      context and (context .. ": ") or "", render(haystack), render(needle)), 2)
  end
end

--- Find the value of one `{ cap, attr, value }` record in an event list.
--- Returns nil when the attribute was not emitted. The first match wins, and
--- only main-component records count (#107: the `awake` component has a
--- `switch` of its own).
function h.event_value(events, cap, attr)
  return h.component_value(events, nil, cap, attr)
end

--- #107: the same for a component (`nil` or "main" = the main component).
function h.component_value(events, component, cap, attr)
  if component == "main" then
    component = nil
  end
  for _, e in ipairs(events or {}) do
    local c = e.component
    if c == "main" then
      c = nil
    end
    if e.cap == cap and e.attr == attr and c == component then
      return e.value
    end
  end
  return nil
end

--- #107: the value of the LAST matching record - what the app ends up showing
--- after a command answered one row more than once.
function h.last_value(events, component, cap, attr)
  if component == "main" then
    component = nil
  end
  local found
  for _, e in ipairs(events or {}) do
    local c = e.component
    if c == "main" then
      c = nil
    end
    if e.cap == cap and e.attr == attr and c == component then
      found = e.value
    end
  end
  return found
end

--- What a fake device actually emitted, as the same `{ cap, attr, value }`
--- records `state.apply_status` produces, so `event_value` works on both. The
--- st.capabilities mock builds `{ capability, attribute, value }`.
function h.emitted(device)
  local out = {}
  for i, e in ipairs((device or {}).emitted or {}) do
    out[i] = {
      cap = e.capability, attr = e.attribute, value = e.value,
      -- #86: the emit options, so a test can tell a forced event (the answer to
      -- an app command) from an ordinary poll update.
      options = e.options,
      -- #107: nil for the main component (`emit_event`), the component id for
      -- `emit_component_event`.
      component = e.component,
    }
  end
  return out
end

--- True when `cap.attr` was emitted with `{ state_change = true }` (#86), false
--- when it was emitted plainly, nil when it was not emitted at all.
-- The last emit wins, which is the one the app sees last.
-- #107: main-component records only, like `event_value`; `component_forced`
-- asks about another component.
function h.event_forced(events, cap, attr)
  return h.component_forced(events, nil, cap, attr)
end

function h.component_forced(events, component, cap, attr)
  if component == "main" then
    component = nil
  end
  local forced
  for _, e in ipairs(events or {}) do
    local c = e.component
    if c == "main" then
      c = nil
    end
    if e.cap == cap and e.attr == attr and c == component then
      forced = (e.options or {}).state_change == true
    end
  end
  return forced
end

--- True when the event list contains any record for `cap`.
function h.has_capability(events, cap)
  for _, e in ipairs(events or {}) do
    if e.cap == cap then
      return true
    end
  end
  return false
end

--- #107: the component table of a profile, keyed by id like
--- `device.profile.components` on the hub: `main` always, `awake` on every v2
--- profile, `battery` on the `-battery` ones.
function h.components_for(profile_name)
  local components = { main = { id = "main" }, awake = { id = "awake" } }
  if type(profile_name) == "string" and profile_name:find("%-battery%.v%d+$") then
    components.battery = { id = "battery" }
  end
  -- #123: the watch card's component, from v7 on (no name = a current device).
  local version = type(profile_name) == "string" and tonumber(profile_name:match("%.v(%d+)$"))
  if not version or version >= 7 then
    components.apps = { id = "apps" }
  end
  return components
end

--- Run the most recent live timer named `name` on a st.driver mock, once: it
--- is marked cancelled first, so firing again runs the next one (or none).
-- Returns true when a timer ran. The suites that load init.lua share one
-- driver, so older tests' timers are still on it; the newest is this test's.
function h.fire_last(driver, name)
  local found
  for _, timer in ipairs((driver or {}).timers or {}) do
    if timer.name == name and not timer.cancelled then
      found = timer
    end
  end
  if not found then
    return false
  end
  found.cancelled = true
  found.fn()
  return true
end

--- Run live timers named `name` until none is left - a timer may set the
--- next one (the batches of a spread repaint, emit.paint). Newest first,
--- like `fire_last`. Returns how many ran.
function h.fire_all(driver, name)
  local n = 0
  while n < 1000 and h.fire_last(driver, name) do
    n = n + 1
  end
  return n
end

--- A device stand-in: preferences plus the get_field/set_field pair.
function h.fake_device(preferences)
  -- `parent_assigned_child_key` is deliberately absent: the hub only sets it on
  -- a child device, and #81 uses its absence to tell a PC from a leftover
  -- display child.
  local device = { id = "test-device", preferences = preferences or {}, fields = {}, emitted = {} }
  function device:get_field(name)
    return self.fields[name]
  end
  function device:set_field(name, value)
    self.fields[name] = value
  end
  function device:emit_event(event)
    self.emitted[#self.emitted + 1] = event
  end
  -- #107: what `emit.rows` uses for the `awake` and `battery` components.
  function device:emit_component_event(component, event)
    local copy = {}
    for k, v in pairs(event) do
      copy[k] = v
    end
    copy.component = component.id
    self.emitted[#self.emitted + 1] = copy
  end
  -- #107: the components a v2 profile has, keyed by id as on the hub. No
  -- `name`, so `profiles.name_of` still falls back to the field as before.
  device.profile = { components = h.components_for(nil) }
  -- #79: what the driver asked the hub to change about the device itself
  -- (`profile`, and `model` since #94). The hub swaps the value out, so the
  -- mock does too - `profiles.name_of` and `discovery.ensure_model` read it
  -- back from there.
  device.metadata_updates = {}
  function device:try_update_metadata(update)
    self.metadata_updates[#self.metadata_updates + 1] = update
    if type(update) == "table" and type(update.profile) == "string" then
      self.profile = { id = update.profile, name = update.profile,
        components = h.components_for(update.profile) }
    end
    if type(update) == "table" and type(update.model) == "string" then
      self.model = update.model
    end
    return true
  end
  function device:online()
    self.health = "online"
  end
  function device:offline()
    self.health = "offline"
  end
  return device
end

--- #129: give a fake device the hub's persistent state cache - what
--- `emit_event`/`emit_component_event` last sent per component, capability
--- and attribute, read back with `get_latest_state` (lua_libs st/device.lua).
--- `h.restart(device)` then forgets everything a driver run keeps in memory
--- and keeps the cache, as a driver restart does.
function h.with_state_cache(device)
  device.state_cache = {}
  local function record(component_id, event)
    local by_cap = device.state_cache[component_id] or {}
    device.state_cache[component_id] = by_cap
    local by_attr = by_cap[event.capability] or {}
    by_cap[event.capability] = by_attr
    by_attr[event.attribute] = { value = event.value }
  end
  local emit_event, emit_component_event = device.emit_event, device.emit_component_event
  function device:emit_event(event)
    record("main", event)
    return emit_event(self, event)
  end
  function device:emit_component_event(component, event)
    record(component.id, event)
    return emit_component_event(self, component, event)
  end
  function device:get_latest_state(component_id, capability_id, attribute)
    local entry = (((self.state_cache[component_id] or {})[capability_id]) or {})[attribute]
    if not entry then
      return nil, nil
    end
    return entry.value, entry
  end
  return device
end

--- #129: a driver restart for a fake device: every field that is not
--- persisted on the hub is gone (the ones `set_field` was given
--- `{ persist = true }` for are listed in `persisted`), the state cache stays.
function h.restart(device, persisted)
  local kept = {}
  for _, name in ipairs(persisted or {}) do
    kept[name] = device.fields[name]
  end
  device.fields = kept
  return device
end

--------------------------------------------------------------------------------
-- #125: the shared contract fixtures in testdata/st-v1 at the repository root
--------------------------------------------------------------------------------

local function dirname(path)
  return (path or ""):match("^(.*)[/\\][^/\\]*$")
end

-- SCRIPT_DIR is set by tools/lua.js; arg[0] covers a real lua interpreter
-- (`lua tests/run.lua` from edge/), exactly as run.lua finds tests/.
local tests_dir = SCRIPT_DIR or dirname(arg and arg[0]) or "tests"

--- testdata/st-v1, from edge/tests.
h.FIXTURE_DIR = tests_dir .. "/../../testdata/st-v1"

--- The contents of a file, or raises. fengari has no `io.open`, so tools/lua.js
--- hands one in as `host.readfile`.
function h.read_file(path)
  if host and host.readfile then
    local text, err = host.readfile(path)
    if not text then
      error("cannot read " .. path .. ": " .. tostring(err), 2)
    end
    return text
  end
  local file, err = io.open(path, "rb")
  if not file then
    error("cannot read " .. path .. ": " .. tostring(err), 2)
  end
  local text = file:read("a")
  file:close()
  return text
end

--- File names in a directory (host.listdir under fengari, `ls` otherwise).
function h.list_dir(dir)
  local names = {}
  if host and host.listdir then
    for _, name in ipairs(host.listdir(dir)) do
      names[#names + 1] = name
    end
  elseif io.popen then
    local pipe = io.popen('ls "' .. dir .. '"')
    if pipe then
      for name in pipe:lines() do
        names[#names + 1] = name
      end
      pipe:close()
    end
  end
  table.sort(names)
  return names
end

--- The driver tree the suite runs against: edge/src, or the stripped copy
--- `run.lua --src` points at (tools/build.js). Set by run.lua.
h.SRC_DIR = tests_dir .. "/../src"

--- Every `.lua` file under `dir`, as paths relative to it ("device/emit.lua"),
--- sorted. A name without a dot is taken for a subdirectory.
function h.lua_tree(dir, prefix, out)
  out = out or {}
  for _, name in ipairs(h.list_dir(dir)) do
    local rel = (prefix and prefix .. "/" or "") .. name
    if name:match("%.lua$") then
      out[#out + 1] = rel
    elseif not name:find(".", 1, true) then
      h.lua_tree(dir .. "/" .. name, rel, out)
    end
  end
  table.sort(out)
  return out
end

--- One fixture of testdata/st-v1, decoded with the st.json mock (the same
--- decoder the driver's bodies go through in every other test). `null`
--- decodes to nil, as on the hub.
function h.fixture(name)
  local json = require "st.json"
  return json.decode(h.read_file(h.FIXTURE_DIR .. "/" .. name))
end

--- The fixture names matching a Lua pattern ("^push%..*%.json$").
function h.fixture_names(pattern)
  local out = {}
  for _, name in ipairs(h.list_dir(h.FIXTURE_DIR)) do
    if name:match(pattern) then
      out[#out + 1] = name
    end
  end
  return out
end

return h
