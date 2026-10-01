#!/usr/bin/env node
/*
 * Build the driver package tree in build/edge/ (#129).
 *
 *   cd edge && node tools/build.js          # (re)write build/edge/
 *   smartthings edge:drivers:package build/edge
 *
 * The hub limits a driver package to 655360 bytes, and about half of src/ is
 * comments. So the package is made from a copy where every Lua comment is
 * replaced by blank lines: line numbers stay exactly what they are in src/, so
 * a line number in a hub log still points at the right line of the source.
 * Full-line YAML comments are dropped from config.yml and profiles/ as well.
 *
 * The stripped tree is a build product (gitignored). CI runs the whole Lua
 * suite against it (`tests/run.lua --src build/edge/src`) and packages it.
 *
 * Options:
 *   --out <dir>   write somewhere else (default: build/edge)
 *   --quiet       no size report
 *   --self-test   check the comment stripper against its cases and exit
 */
'use strict';

const fs = require('fs');
const path = require('path');

const EDGE = path.resolve(__dirname, '..');

/**
 * Replace the comments of a Lua 5.3 chunk with whitespace, keeping every
 * newline. Strings ('…', "…", [[…]], [==[…]==]) are copied untouched, so a
 * `--` inside one is not a comment. A long comment that spans lines leaves its
 * newlines; one on a single line leaves one space, so `a--[[x]]b` cannot
 * become `ab`. Whitespace before a removed comment goes too.
 */
function stripLua(source) {
  const out = [];
  const n = source.length;
  let i = 0;

  // `[` `=`* `[` at `at`: the level (number of `=`), else -1.
  function longOpen(at) {
    if (source[at] !== '[') return -1;
    let j = at + 1;
    while (source[j] === '=') j++;
    return source[j] === '[' ? j - at - 1 : -1;
  }

  // The index just past the `]` `=`*level `]` that closes a long bracket
  // opened at `at`, or n when it is never closed (left to the compiler).
  function longClose(at, level) {
    const close = ']' + '='.repeat(level) + ']';
    const end = source.indexOf(close, at + level + 2);
    return end < 0 ? n : end + close.length;
  }

  function dropTrailingBlanks() {
    while (out.length > 0) {
      const last = out[out.length - 1];
      const trimmed = last.replace(/[ \t]+$/, '');
      if (trimmed === last) return;
      if (trimmed === '') {
        out.pop();
      } else {
        out[out.length - 1] = trimmed;
        return;
      }
    }
  }

  while (i < n) {
    const c = source[i];
    if (c === '-' && source[i + 1] === '-') {
      dropTrailingBlanks();
      const level = longOpen(i + 2);
      if (level >= 0) {
        const end = longClose(i + 2, level);
        const newlines = (source.slice(i, end).match(/\n/g) || []).length;
        out.push(newlines > 0 ? '\n'.repeat(newlines) : ' ');
        i = end;
      } else {
        let end = source.indexOf('\n', i);
        if (end < 0) end = n;
        // Keep a CR of a CRLF line ending with its LF.
        if (end > i && source[end - 1] === '\r') end--;
        i = end;
      }
      continue;
    }
    if (c === '"' || c === "'") {
      let j = i + 1;
      while (j < n && source[j] !== c && source[j] !== '\n') {
        if (source[j] === '\\' && source[j + 1] === 'z') {
          // `\z` skips the whitespace after it, newlines included.
          j += 2;
          while (j < n && /\s/.test(source[j])) j++;
        } else {
          j += source[j] === '\\' ? 2 : 1;
        }
      }
      j = Math.min(j + 1, n);
      out.push(source.slice(i, j));
      i = j;
      continue;
    }
    if (c === '[') {
      const level = longOpen(i);
      if (level >= 0) {
        const end = longClose(i, level);
        out.push(source.slice(i, end));
        i = end;
        continue;
      }
    }
    // Plain code up to the next character that may start one of the above.
    let j = i + 1;
    while (j < n && !'-"\'['.includes(source[j])) j++;
    out.push(source.slice(i, j));
    i = j;
  }
  return out.join('');
}

/**
 * Drop full-line `#` comments from a YAML file. Refuses a file with a block
 * scalar (`key: |` / `key: >`), where a `#` line can be content.
 */
function stripYaml(text, name) {
  if (/:\s*[|>][-+0-9]*\s*$/m.test(text)) {
    throw new Error(name + ': block scalars are not supported by the comment stripper');
  }
  return text
    .split('\n')
    .filter((line) => !/^\s*#/.test(line))
    .join('\n');
}

function listLua(dir, rel, files) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const relPath = rel ? rel + '/' + entry.name : entry.name;
    if (entry.isDirectory()) {
      listLua(path.join(dir, entry.name), relPath, files);
    } else if (entry.name.endsWith('.lua')) {
      files.push(relPath);
    }
  }
  return files;
}

function lines(text) {
  return (text.match(/\n/g) || []).length;
}

function build(outDir) {
  fs.rmSync(outDir, { recursive: true, force: true });
  fs.mkdirSync(path.join(outDir, 'profiles'), { recursive: true });
  const sizes = { src: [0, 0], profiles: [0, 0], config: [0, 0] };

  const config = fs.readFileSync(path.join(EDGE, 'config.yml'), 'utf8');
  const configOut = stripYaml(config, 'config.yml');
  fs.writeFileSync(path.join(outDir, 'config.yml'), configOut);
  sizes.config = [Buffer.byteLength(config), Buffer.byteLength(configOut)];

  for (const name of fs.readdirSync(path.join(EDGE, 'profiles')).sort()) {
    if (!name.endsWith('.yml')) continue;
    const text = fs.readFileSync(path.join(EDGE, 'profiles', name), 'utf8');
    const stripped = stripYaml(text, 'profiles/' + name);
    fs.writeFileSync(path.join(outDir, 'profiles', name), stripped);
    sizes.profiles[0] += Buffer.byteLength(text);
    sizes.profiles[1] += Buffer.byteLength(stripped);
  }

  for (const rel of listLua(path.join(EDGE, 'src'), '', []).sort()) {
    const text = fs.readFileSync(path.join(EDGE, 'src', rel), 'utf8');
    const stripped = stripLua(text);
    if (lines(stripped) !== lines(text)) {
      throw new Error('src/' + rel + ': stripping changed the line count');
    }
    const target = path.join(outDir, 'src', rel);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, stripped);
    sizes.src[0] += Buffer.byteLength(text);
    sizes.src[1] += Buffer.byteLength(stripped);
  }
  return sizes;
}

// Cases the stripper must get right: [source, expected]. `--self-test` runs
// them (CI does, before the build).
const CASES = [
  ['local a = 1 -- note\n', 'local a = 1\n'],
  ['-- whole line\nx()\n', '\nx()\n'],
  ['  --- doc\n', '\n'],
  ['s = "a -- b" -- c\n', 's = "a -- b"\n'],
  ["s = 'it''s' -- c\n", "s = 'it''s'\n"],
  ['s = "q\\"--" -- c\n', 's = "q\\"--"\n'],
  ['s = [[x -- y]] -- c\n', 's = [[x -- y]]\n'],
  ['s = [==[a ]] -- b]==] --c\n', 's = [==[a ]] -- b]==]\n'],
  ['a --[[ one ]] b\n', 'a  b\n'],
  ['a--[[x]]b\n', 'a b\n'],
  ['--[[\nline\n]]\ny = 2\n', '\n\n\ny = 2\n'],
  ['--[==[\n]]\n]==] z()\n', '\n\n z()\n'],
  ['x = a - -b --c\n', 'x = a - -b\n'],
  ['t[ [[k]] ] = 1\n', 't[ [[k]] ] = 1\n'],
  ['s = "a\\z\n   b" -- c\n', 's = "a\\z\n   b"\n'],
  ['x = 1 -- crlf\r\ny = 2\r\n', 'x = 1\r\ny = 2\r\n'],
];

function selfTest() {
  let failed = 0;
  for (const [source, expected] of CASES) {
    const got = stripLua(source);
    if (got !== expected || lines(got) !== lines(source)) {
      failed++;
      process.stderr.write('stripLua(' + JSON.stringify(source) + ') = ' + JSON.stringify(got)
        + ', want ' + JSON.stringify(expected) + '\n');
    }
  }
  if (failed > 0) process.exit(1);
  process.stdout.write('stripLua: ' + CASES.length + ' cases ok\n');
}

function main() {
  const argv = process.argv.slice(2);
  if (argv.includes('--self-test')) {
    selfTest();
    return;
  }
  const at = argv.indexOf('--out');
  const outDir = at >= 0 ? path.resolve(argv[at + 1]) : path.join(EDGE, 'build', 'edge');
  const sizes = build(outDir);
  if (argv.includes('--quiet')) return;
  const total = (k) => sizes.src[k] + sizes.profiles[k] + sizes.config[k];
  const row = (label, pair) => '  ' + label.padEnd(10) + String(pair[0]).padStart(8)
    + ' -> ' + String(pair[1]).padStart(8) + ' bytes';
  process.stdout.write('wrote ' + path.relative(process.cwd(), outDir) + '\n'
    + row('src', sizes.src) + '\n'
    + row('profiles', sizes.profiles) + '\n'
    + row('config', sizes.config) + '\n'
    + row('package', [total(0), total(1)]) + ' (limit 655360)\n');
}

if (require.main === module) {
  main();
}

module.exports = { stripLua, stripYaml };
