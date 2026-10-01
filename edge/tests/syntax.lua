-- Compile every driver module without running it.
--
-- The hub libraries (st.*, cosock, ltn12) do not exist locally, so this only
-- parses: it catches syntax errors in files no test requires. `loadfile` never
-- executes a chunk, so the top-level `require` calls are not a problem.
--
--   bun tools/lua.js tests/syntax.lua
--   lua5.3 tests/syntax.lua --src build/edge/src     (the stripped package)

local function dirname(path)
  return (path or ""):match("^(.*)[/\\][^/\\]*$")
end

local tests_dir = SCRIPT_DIR or dirname(arg and arg[0]) or "tests"
local edge_dir = tests_dir .. "/.."
local src_dir = edge_dir .. "/src"
for i = 1, #(arg or {}) do
  if arg[i] == "--src" and arg[i + 1] then
    src_dir = arg[i + 1]
  end
end

local DIRS = { src_dir, tests_dir, edge_dir .. "/tools" }

local function list(dir)
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

-- `.lua` files under `dir`, relative to it (src/ has subdirectories). A name
-- without a dot is taken for a directory.
local function lua_files(dir, prefix, out)
  out = out or {}
  for _, name in ipairs(list(dir)) do
    local rel = (prefix and prefix .. "/" or "") .. name
    if name:match("%.lua$") then
      out[#out + 1] = rel
    elseif not name:find(".", 1, true) then
      lua_files(dir .. "/" .. name, rel, out)
    end
  end
  return out
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
