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
  function device:online()
    self.health = "online"
  end
  function device:offline()
    self.health = "offline"
  end
  return device
end

return h
