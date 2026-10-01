-- luacheck configuration for the Edge driver. Run from edge/:
--
--   luacheck src tests
--
-- CI (.github/workflows/edge.yml) installs Ubuntu's lua-check package.

-- The hub runs Lua 5.3; flag anything that only exists in other versions
-- (unpack, setfenv, loadstring, bit32, ...).
std = "lua53"

-- Long string tables (i18n, capability presentations) and comments quoting
-- the platform docs are easier to read unwrapped.
max_line_length = false

-- Hub callbacks have fixed signatures (driver, device, command); an argument
-- a handler does not need is named with a leading underscore.
ignore = { "212/_.*" }

-- src/ needs no globals beyond the Lua 5.3 standard library: everything the
-- hub provides (st.*, cosock, log, ltn12) is `require`d.

files["tests"] = {
  -- tools/lua.js (fengari) sets SCRIPT_DIR and the host.readfile/listdir
  -- helpers; under a real interpreter both are nil and the tests fall back
  -- to arg[0] and io.* instead.
  read_globals = { "SCRIPT_DIR", "host" },
  -- Stubs and mocks mirror hub methods (device:try_update_metadata,
  -- Driver:cancel_timer), so they keep the method form whether or not they
  -- use self.
  self = false,
}
