-- A tiny pure-Lua JSON codec standing in for the hub's `st.json`.
--
-- Scope is deliberately small: what `/st/v1` bodies need (objects, arrays,
-- strings, numbers, booleans). Deviations from st.json worth knowing:
--   * `null` decodes to nil, so a null inside an array leaves a hole;
--   * an empty table encodes as `[]`, since Lua cannot tell {} apart from an
--     empty object;
--   * object keys are sorted on encode, which makes encoded bodies comparable
--     in assertions.

local json = {}

--------------------------------------------------------------------------------
-- encode
--------------------------------------------------------------------------------

local ESCAPES = {
  ['"'] = '\\"',
  ["\\"] = "\\\\",
  ["\b"] = "\\b",
  ["\f"] = "\\f",
  ["\n"] = "\\n",
  ["\r"] = "\\r",
  ["\t"] = "\\t",
}

local function escape_char(c)
  return ESCAPES[c] or string.format("\\u%04x", string.byte(c))
end

local function encode_string(s)
  return '"' .. s:gsub('[%z\1-\31\\"]', escape_char) .. '"'
end

local function is_array(t)
  local count = 0
  for k in pairs(t) do
    if type(k) ~= "number" then
      return false
    end
    count = count + 1
  end
  return count == #t
end

local encode_value

encode_value = function(v)
  local t = type(v)
  if v == nil then
    return "null"
  elseif t == "boolean" then
    return tostring(v)
  elseif t == "number" then
    if v % 1 == 0 then
      return string.format("%d", v)
    end
    return string.format("%.14g", v)
  elseif t == "string" then
    return encode_string(v)
  elseif t == "table" then
    if is_array(v) then
      local parts = {}
      for i = 1, #v do
        parts[i] = encode_value(v[i])
      end
      return "[" .. table.concat(parts, ",") .. "]"
    end
    local keys = {}
    for k in pairs(v) do
      keys[#keys + 1] = k
    end
    table.sort(keys, function(a, b) return tostring(a) < tostring(b) end)
    local parts = {}
    for _, k in ipairs(keys) do
      parts[#parts + 1] = encode_string(tostring(k)) .. ":" .. encode_value(v[k])
    end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  error("json: cannot encode " .. t, 0)
end

function json.encode(value)
  return encode_value(value)
end

--------------------------------------------------------------------------------
-- decode
--------------------------------------------------------------------------------

local function fail(pos, msg)
  error(string.format("json: %s at position %d", msg, pos), 0)
end

local function skip_ws(s, i)
  local j = s:find("[^ \t\r\n]", i)
  return j or (#s + 1)
end

local STRING_ESCAPES = {
  ['"'] = '"', ["\\"] = "\\", ["/"] = "/",
  b = "\b", f = "\f", n = "\n", r = "\r", t = "\t",
}

local function parse_string(s, i)
  local out = {}
  i = i + 1 -- skip the opening quote
  while true do
    local c = s:sub(i, i)
    if c == "" then
      fail(i, "unterminated string")
    elseif c == '"' then
      return table.concat(out), i + 1
    elseif c == "\\" then
      local e = s:sub(i + 1, i + 1)
      if STRING_ESCAPES[e] then
        out[#out + 1] = STRING_ESCAPES[e]
        i = i + 2
      elseif e == "u" then
        local code = tonumber(s:sub(i + 2, i + 5), 16)
        if not code then
          fail(i, "invalid \\u escape")
        end
        out[#out + 1] = utf8.char(code)
        i = i + 6
      else
        fail(i, "invalid escape")
      end
    else
      out[#out + 1] = c
      i = i + 1
    end
  end
end

local parse_value

local function parse_array(s, i)
  local arr = {}
  i = skip_ws(s, i + 1)
  if s:sub(i, i) == "]" then
    return arr, i + 1
  end
  while true do
    local value
    value, i = parse_value(s, i)
    arr[#arr + 1] = value
    i = skip_ws(s, i)
    local c = s:sub(i, i)
    if c == "," then
      i = skip_ws(s, i + 1)
    elseif c == "]" then
      return arr, i + 1
    else
      fail(i, "expected ',' or ']'")
    end
  end
end

local function parse_object(s, i)
  local obj = {}
  i = skip_ws(s, i + 1)
  if s:sub(i, i) == "}" then
    return obj, i + 1
  end
  while true do
    if s:sub(i, i) ~= '"' then
      fail(i, "expected object key")
    end
    local key
    key, i = parse_string(s, i)
    i = skip_ws(s, i)
    if s:sub(i, i) ~= ":" then
      fail(i, "expected ':'")
    end
    i = skip_ws(s, i + 1)
    local value
    value, i = parse_value(s, i)
    obj[key] = value
    i = skip_ws(s, i)
    local c = s:sub(i, i)
    if c == "," then
      i = skip_ws(s, i + 1)
    elseif c == "}" then
      return obj, i + 1
    else
      fail(i, "expected ',' or '}'")
    end
  end
end

parse_value = function(s, i)
  i = skip_ws(s, i)
  local c = s:sub(i, i)
  if c == "{" then
    return parse_object(s, i)
  elseif c == "[" then
    return parse_array(s, i)
  elseif c == '"' then
    return parse_string(s, i)
  elseif s:sub(i, i + 3) == "true" then
    return true, i + 4
  elseif s:sub(i, i + 4) == "false" then
    return false, i + 5
  elseif s:sub(i, i + 3) == "null" then
    return nil, i + 4
  end
  local j = i
  while s:sub(j, j):match("[%-%+%.%deE]") do
    j = j + 1
  end
  local num = tonumber(s:sub(i, j - 1))
  if num == nil then
    fail(i, "unexpected character '" .. c .. "'")
  end
  return num, j
end

function json.decode(text)
  if type(text) ~= "string" then
    error("json: decode expects a string", 0)
  end
  local value, i = parse_value(text, 1)
  i = skip_ws(text, i)
  if i <= #text then
    fail(i, "trailing garbage")
  end
  return value
end

return json
