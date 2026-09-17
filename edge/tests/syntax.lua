-- Compile every driver module without running it.
--
-- The hub libraries (st.*, cosock, ltn12) do not exist locally, so this only
-- parses: it catches syntax errors in files no test requires (init.lua,
-- poll.lua, discovery.lua). `loadfile` never executes a chunk, so the top-level
-- `require` calls in those files are not a problem.
--
--   bun tools/lua.js tests/syntax.lua

local function dirname(path)
  return (path or ""):match("^(.*)[/\\][^/\\]*$")
end

local tests_dir = SCRIPT_DIR or dirname(arg and arg[0]) or "tests"
local edge_dir = tests_dir .. "/.."

local DIRS = { edge_dir .. "/src", tests_dir, edge_dir .. "/tools" }

local function lua_files(dir)
  local names = {}
  if host and host.listdir then
    for _, name in ipairs(host.listdir(dir)) do
      if name:match("%.lua$") then
        names[#names + 1] = name
      end
    end
  elseif io.popen then
    local pipe = io.popen('ls "' .. dir .. '"')
    if pipe then
      for name in pipe:lines() do
        if name:match("%.lua$") then
          names[#names + 1] = name
        end
      end
      pipe:close()
    end
  end
  table.sort(names)
  return names
end

local checked, failed = 0, 0

for _, dir in ipairs(DIRS) do
  for _, name in ipairs(lua_files(dir)) do
    local path = dir .. "/" .. name
    local chunk, err = loadfile(path)
    checked = checked + 1
    if chunk then
      io.write("OK   " .. name .. "\n")
    else
      failed = failed + 1
      io.write("BAD  " .. name .. ": " .. tostring(err) .. "\n")
    end
  end
end

io.write(string.format("\n%d files compiled, %d failed\n", checked, failed))
os.exit(failed > 0 and 1 or 0)
