-- The handful of `st.utils` helpers a driver typically reaches for.
local utils = {}

function utils.deep_copy(value)
  if type(value) ~= "table" then
    return value
  end
  local copy = {}
  for k, v in pairs(value) do
    copy[k] = utils.deep_copy(v)
  end
  return copy
end

function utils.stringify_table(value, name, multi_line)
  local function render(v)
    if type(v) ~= "table" then
      if type(v) == "string" then
        return string.format("%q", v)
      end
      return tostring(v)
    end
    local keys = {}
    for k in pairs(v) do
      keys[#keys + 1] = k
    end
    table.sort(keys, function(a, b) return tostring(a) < tostring(b) end)
    local parts = {}
    for _, k in ipairs(keys) do
      parts[#parts + 1] = tostring(k) .. "=" .. render(v[k])
    end
    return "{" .. table.concat(parts, multi_line and ",\n" or ", ") .. "}"
  end
  if name then
    return tostring(name) .. ": " .. render(value)
  end
  return render(value)
end

function utils.merge(target, source)
  for k, v in pairs(source or {}) do
    if target[k] == nil then
      target[k] = v
    end
  end
  return target
end

return utils
