-- The two ltn12 helpers client.lua uses, reimplemented so tests do not need
-- luasocket. Behaviour matches ltn12 closely enough for a single-chunk body.
local ltn12 = { source = {}, sink = {} }

--- A source that yields `s` once and then nil.
function ltn12.source.string(s)
  local done = false
  return function()
    if done then
      return nil
    end
    done = true
    return s
  end
end

--- A sink that appends every chunk to `t`.
function ltn12.sink.table(t)
  t = t or {}
  return function(chunk, err)
    if chunk then
      t[#t + 1] = chunk
    elseif err then
      return nil, err
    end
    return 1
  end, t
end

return ltn12
