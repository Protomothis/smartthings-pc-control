#!/usr/bin/env node
/*
 * Minimal `lua <script.lua> [args...]` runner backed by fengari (Lua 5.3 in JS).
 *
 * There is no native Lua interpreter in this project's dev environment, and the
 * SmartThings Edge runtime is Lua 5.3, so fengari runs the same dialect the hub
 * does. Used by `npm test` (CI) and locally via `bun tools/lua.js tests/run.lua`.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const fengari = require('fengari');

const { lua, lauxlib, lualib, to_luastring, to_jsstring } = fengari;

function fail(msg) {
  process.stderr.write(String(msg) + '\n');
  process.exit(1);
}

const argv = process.argv.slice(2);
if (argv.length === 0) {
  fail('usage: lua.js <script.lua> [args...]');
}

const scriptPath = argv[0];
if (!fs.existsSync(scriptPath)) {
  fail('lua.js: cannot open ' + scriptPath);
}

const L = lauxlib.luaL_newstate();
lualib.luaL_openlibs(L);

// Standard Lua `arg` table: arg[0] = script, arg[1..] = script arguments.
lua.lua_createtable(L, argv.length - 1, 1);
lua.lua_pushstring(L, to_luastring(scriptPath));
lua.lua_rawseti(L, -2, 0);
for (let i = 1; i < argv.length; i++) {
  lua.lua_pushstring(L, to_luastring(argv[i]));
  lua.lua_rawseti(L, -2, i);
}
lua.lua_setglobal(L, to_luastring('arg'));

// Make relative requires resolve from the script's directory as well as cwd.
const scriptDir = path.dirname(path.resolve(scriptPath));

const source = fs.readFileSync(scriptPath);
let status = lauxlib.luaL_loadbuffer(
  L,
  fengari.to_luastring(source.toString('utf8')),
  null,
  to_luastring('@' + scriptPath)
);

function luaError() {
  const msg = lua.lua_tostring(L, -1);
  return msg ? to_jsstring(msg) : '(no error message)';
}

if (status !== lua.LUA_OK) {
  fail('lua.js: ' + luaError());
}

// Expose the script directory so run.lua can build package.path portably.
lua.lua_pushstring(L, to_luastring(scriptDir.replace(/\\/g, '/')));
lua.lua_setglobal(L, to_luastring('SCRIPT_DIR'));

// `host` bridges the few things fengari's stripped-down io library cannot do.
// Under a real Lua interpreter this global is absent and run.lua falls back.
lua.lua_createtable(L, 0, 2);
lua.lua_pushstring(L, to_luastring(scriptDir.replace(/\\/g, '/')));
lua.lua_setfield(L, -2, to_luastring('script_dir'));
lua.lua_pushcfunction(L, function (S) {
  const dir = to_jsstring(lauxlib.luaL_checkstring(S, 1));
  let names;
  try {
    names = fs.readdirSync(dir);
  } catch (e) {
    names = [];
  }
  lua.lua_createtable(S, names.length, 0);
  names.forEach(function (name, i) {
    lua.lua_pushstring(S, to_luastring(name));
    lua.lua_rawseti(S, -2, i + 1);
  });
  return 1;
});
lua.lua_setfield(L, -2, to_luastring('listdir'));
lua.lua_setglobal(L, to_luastring('host'));

// Push script args as varargs for the chunk.
for (let i = 1; i < argv.length; i++) {
  lua.lua_pushstring(L, to_luastring(argv[i]));
}

status = lua.lua_pcall(L, argv.length - 1, lua.LUA_MULTRET, 0);
if (status !== lua.LUA_OK) {
  fail('lua.js: ' + luaError());
}

process.exit(0);
