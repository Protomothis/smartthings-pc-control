#!/usr/bin/env node
/*
 * Rewrite the custom capability namespace across the driver.
 *
 *   node tools/apply-namespace.js <namespace>      (bun works too)
 *   node tools/apply-namespace.js --dry-run <namespace>
 *
 * SmartThings assigns a namespace to the account the first time
 * `smartthings capabilities:create` runs, and every custom capability id is
 * `<namespace>.pcPower` and friends. The repository ships the placeholder
 * `pccontrol00000` (design doc §11.1) because that value is not knowable until
 * the owner creates the capabilities; this script puts the real one in.
 *
 * The current namespace is read from `src/caps.lua` rather than assumed to be
 * the placeholder, so the script is idempotent (running it twice changes
 * nothing the second time) and can also move an already-applied driver to a
 * different namespace - handy when testing against a second account.
 *
 * Only `fs`/`path` are used, so this runs under node (CI) and bun (local)
 * unchanged.
 */
'use strict';

const fs = require('fs');
const path = require('path');

// tools/ -> edge/
const ROOT = path.resolve(__dirname, '..');
const CAPS_LUA = path.join(ROOT, 'src', 'caps.lua');

// Lowercase alphanumeric, 8-20 characters: what SmartThings hands out, and
// what the capability id grammar accepts. Rejecting early beats discovering it
// when the CLI refuses the profile.
const NAMESPACE_RE = /^[a-z0-9]{8,20}$/;

function fail(msg) {
  process.stderr.write('apply-namespace: ' + msg + '\n');
  process.exit(1);
}

function usage() {
  process.stderr.write(
    'usage: node tools/apply-namespace.js [--dry-run] <namespace>\n' +
      '\n' +
      '  <namespace>  lowercase letters and digits, 8-20 characters.\n' +
      '               Printed by `smartthings capabilities:create`, and by\n' +
      '               `smartthings capabilities` as the prefix of each id.\n'
  );
  process.exit(1);
}

const args = process.argv.slice(2);
let dryRun = false;
const positional = [];
for (const arg of args) {
  if (arg === '--dry-run' || arg === '-n') {
    dryRun = true;
  } else if (arg === '--help' || arg === '-h') {
    usage();
  } else if (arg.startsWith('-')) {
    fail('unknown option ' + arg);
  } else {
    positional.push(arg);
  }
}

if (positional.length !== 1) {
  usage();
}

const next = positional[0];
if (!NAMESPACE_RE.test(next)) {
  fail(
    'invalid namespace "' +
      next +
      '": expected 8-20 lowercase letters or digits (got ' +
      next.length +
      ' character(s))'
  );
}

/** Read the namespace the driver currently uses out of src/caps.lua. */
function currentNamespace() {
  let lua;
  try {
    lua = fs.readFileSync(CAPS_LUA, 'utf8');
  } catch (e) {
    fail('cannot read ' + CAPS_LUA + ': ' + ((e && e.message) || e));
  }
  const m = lua.match(/^local\s+NAMESPACE\s*=\s*"([^"]+)"/m);
  if (!m) {
    fail('no `local NAMESPACE = "..."` line in ' + CAPS_LUA);
  }
  return m[1];
}

/** Every file that may mention the namespace: sources, profiles, capability JSON, docs. */
function targetFiles() {
  const files = [CAPS_LUA, path.join(ROOT, 'README.md')];
  for (const dir of ['profiles', 'capabilities']) {
    const abs = path.join(ROOT, dir);
    let names;
    try {
      names = fs.readdirSync(abs);
    } catch (e) {
      fail('cannot list ' + abs + ': ' + ((e && e.message) || e));
    }
    for (const name of names.sort()) {
      if (dir === 'profiles' ? /\.ya?ml$/.test(name) : /\.json$/.test(name)) {
        files.push(path.join(abs, name));
      }
    }
  }
  return files.filter((f) => fs.existsSync(f));
}

const current = currentNamespace();

if (current === next) {
  process.stdout.write('namespace is already "' + next + '" - nothing to do\n');
  process.exit(0);
}

// Plain global replacement of the current namespace token. The value is
// validated alphanumeric on both sides, so there is nothing to escape and no
// way for it to match a partial word in a way that matters.
const changed = [];
let occurrences = 0;

for (const file of targetFiles()) {
  const before = fs.readFileSync(file, 'utf8');
  const parts = before.split(current);
  if (parts.length === 1) continue;
  occurrences += parts.length - 1;
  const after = parts.join(next);
  if (!dryRun) {
    fs.writeFileSync(file, after);
  }
  changed.push({
    file: path.relative(ROOT, file).replace(/\\/g, '/'),
    hits: parts.length - 1,
  });
}

if (changed.length === 0) {
  process.stdout.write(
    'no file mentions "' + current + '" - nothing to do\n'
  );
  process.exit(0);
}

process.stdout.write(
  (dryRun ? 'would rewrite ' : 'rewrote ') +
    '"' +
    current +
    '" -> "' +
    next +
    '" (' +
    occurrences +
    ' occurrence(s) in ' +
    changed.length +
    ' file(s))\n'
);
for (const c of changed) {
  process.stdout.write('  ' + c.file + '  (' + c.hits + ')\n');
}

if (!dryRun) {
  process.stdout.write(
    '\nNext: `npm test` (capabilities_test.lua checks the ids still line up),\n' +
      'then commit. Comments that call the old value a PLACEHOLDER were\n' +
      'rewritten with the rest - tidy them up if they now read oddly.\n'
  );
}
