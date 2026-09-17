-- Test runner: loads every tests/*_test.lua, calls the functions named test_*
-- in the table it returns, prints one line per test and exits non-zero on any
-- failure.
--
--   bun tools/lua.js tests/run.lua     (local)
--   npm test                           (CI, same runner through node)
--
-- No test framework: the hub has none, and the point is that these modules run
-- under plain Lua 5.3.

local function dirname(path)
  return (path or ""):match("^(.*)[/\\][^/\\]*$")
end

-- SCRIPT_DIR is set by tools/lua.js; arg[0] covers a real lua interpreter.
local tests_dir = SCRIPT_DIR or dirname(arg and arg[0]) or "tests"
local edge_dir = tests_dir .. "/.."
local src_dir = edge_dir .. "/src"
local mocks_dir = tests_dir .. "/mocks"

-- Driver modules require each other flat ("state", "caps", ...), exactly as on
-- the hub where src/ is the package root. Only plain names are used, so the
-- platform's directory separator never enters into it.
package.path = table.concat({
  src_dir .. "/?.lua",
  tests_dir .. "/?.lua",
  package.path,
}, ";")

-- Hub-provided modules are preloaded from tests/mocks by explicit path, rather
-- than via package.path, so that dotted names like "st.json" do not depend on
-- how the interpreter spells the directory separator.
local MOCKS = {
  ["log"] = "log.lua",
  ["ltn12"] = "ltn12.lua",
  ["cosock"] = "cosock.lua",
  ["st.json"] = "st/json.lua",
  ["st.utils"] = "st/utils.lua",
  ["st.driver"] = "st/driver.lua",
  ["st.capabilities"] = "st/capabilities.lua",
}

for name, file in pairs(MOCKS) do
  local path = mocks_dir .. "/" .. file
  local chunk, err = loadfile(path)
  if not chunk then
    io.write("cannot load mock " .. name .. " (" .. path .. "): " .. tostring(err) .. "\n")
    os.exit(1)
  end
  package.preload[name] = chunk
end

-- Discover tests/*_test.lua.
local function list_test_files()
  local names = {}
  if host and host.listdir then
    for _, name in ipairs(host.listdir(tests_dir)) do
      if name:match("_test%.lua$") then
        names[#names + 1] = name
      end
    end
  elseif io.popen then
    local pipe = io.popen('ls "' .. tests_dir .. '"')
    if pipe then
      for name in pipe:lines() do
        if name:match("_test%.lua$") then
          names[#names + 1] = name
        end
      end
      pipe:close()
    end
  else
    io.write("no way to list " .. tests_dir .. ": need host.listdir or io.popen\n")
    os.exit(1)
  end
  table.sort(names)
  return names
end

local files = list_test_files()
if #files == 0 then
  io.write("no *_test.lua files found in " .. tests_dir .. "\n")
  os.exit(1)
end

local passed, failed = 0, 0
local failures = {}

for _, file in ipairs(files) do
  local suite = file:gsub("%.lua$", "")
  local chunk, load_err = loadfile(tests_dir .. "/" .. file)
  if not chunk then
    failed = failed + 1
    failures[#failures + 1] = suite .. ": " .. tostring(load_err)
    io.write("FAIL " .. suite .. " (could not load): " .. tostring(load_err) .. "\n")
  else
    local ok, module = pcall(chunk)
    if not ok or type(module) ~= "table" then
      failed = failed + 1
      local why = ok and "did not return a table of tests" or tostring(module)
      failures[#failures + 1] = suite .. ": " .. why
      io.write("FAIL " .. suite .. " (could not run): " .. why .. "\n")
    else
      local names = {}
      for name in pairs(module) do
        if type(name) == "string" and name:match("^test_") then
          names[#names + 1] = name
        end
      end
      table.sort(names)
      for _, name in ipairs(names) do
        local test_ok, test_err = pcall(module[name])
        if test_ok then
          passed = passed + 1
          io.write("PASS " .. suite .. "." .. name .. "\n")
        else
          failed = failed + 1
          failures[#failures + 1] = suite .. "." .. name .. ": " .. tostring(test_err)
          io.write("FAIL " .. suite .. "." .. name .. ": " .. tostring(test_err) .. "\n")
        end
      end
    end
  end
end

io.write(string.format("\n%d passed, %d failed (%d files)\n", passed, failed, #files))

if failed > 0 then
  io.write("\nfailures:\n")
  for _, f in ipairs(failures) do
    io.write("  " .. f .. "\n")
  end
  os.exit(1)
end

os.exit(0)
