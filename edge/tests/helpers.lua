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
--- Returns nil when the attribute was not emitted.
function h.event_value(events, cap, attr)
  for _, e in ipairs(events or {}) do
    if e.cap == cap and e.attr == attr then
      return e.value
    end
  end
  return nil
end

--- What a fake device actually emitted, as the same `{ cap, attr, value }`
--- records `state.apply_status` produces, so `event_value` works on both. The
--- st.capabilities mock builds `{ capability, attribute, value }`.
function h.emitted(device)
  local out = {}
  for i, e in ipairs((device or {}).emitted or {}) do
    out[i] = { cap = e.capability, attr = e.attribute, value = e.value }
  end
  return out
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
  -- #79: what the driver asked the hub to change about the device itself
  -- (currently only `profile`). The hub swaps the profile out, so the mock
  -- does too - `profiles.name_of` reads it back from there.
  device.metadata_updates = {}
  function device:try_update_metadata(update)
    self.metadata_updates[#self.metadata_updates + 1] = update
    if type(update) == "table" and type(update.profile) == "string" then
      self.profile = { id = update.profile, name = update.profile, components = {} }
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

return h
