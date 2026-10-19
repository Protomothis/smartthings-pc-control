#!/usr/bin/env node
/*
 * Generate the current device profiles from tools/profile-template.yml (#107).
 *
 *   cd edge && bun tools/gen-profiles.js           # (re)write profiles/*
 *   cd edge && bun tools/gen-profiles.js --check   # exit 1 if any file is stale
 *
 * `node` works the same. One template, eight files (#158): with and without
 * each of three cards a PC may not need - the monitor card (#153), the watch
 * list (#123) and the battery (#116, a desktop must not carry an empty
 * battery card) - all in the Switch category (#136). Up to v12 there were
 * twenty, one per `iconStyle` value (#100); v13 and v14 had two.
 *
 *   pc.v15                         profiles/pc-v15.yml                        none
 *   pc-watch.v15                   profiles/pc-watch-v15.yml                  watch list
 *   pc-monitor-watch-battery.v15   profiles/pc-monitor-watch-battery-v15.yml  all three
 *   …
 *
 * The rules are deliberately small; `--check` (CI: npm run check-profiles)
 * fails when a file on disk is not what they produce:
 *
 *   1. Normalise line endings to LF and drop the template's header - every
 *      line before the first one that starts with `name:`.
 *   2. Replace `__NAME__` and `__CATEGORY__` (each exactly once; the
 *      category is `CATEGORY`).
 *   3. Keep the lines between `# @<card>-begin` and `# @<card>-end` (card:
 *      monitor, watch, battery) only for a profile with that card; drop the
 *      marker lines always.
 *   3c. (#129) Drop every other comment line (`#` after optional spaces): the
 *      template explains itself, the copies do not need to.
 *   4. Prepend the generated header below.
 *
 * `VERSION` and `CATEGORY` are also in src/profiles.lua; the Lua test reads
 * them back out of this file, so the two cannot drift apart without the suite
 * failing.
 */
'use strict';

const fs = require('fs');
const path = require('path');

const EDGE = path.resolve(__dirname, '..');
const TEMPLATE = path.join(EDGE, 'tools', 'profile-template.yml');
const OUT_DIR = path.join(EDGE, 'profiles');

// The profile generation. A screen change bumps this AND the name in
// src/profiles.lua (`profiles.VERSION`); the files of the old generation then
// stay in profiles/ untouched, for the devices that have not moved yet -
// unless that generation was never published (`profiles.UNSHIPPED_VERSIONS`),
// in which case its files are deleted instead.
// 3: pcMessage replaced the standard notification/speechSynthesis pair.
// 4: pcNotify (send only) replaced pcMessage; v2 and v3 were never published.
// 5: pcToast (send + lastMessage, the row's value) replaced pcNotify; v4 was
//    never published either.
// 6: pcApps (a summary row; one child device per watched app, #123) replaced
//    the kind-based pcActivity; v5 was never published either.
// 7: the watch card - component `apps` with pcWatch (summary, names, five
//    slots a routine reads) replaced pcApps and the per-app child devices;
//    v6 was never published either.
// 8: the watch card's preview - the main view shows a component card's first
//    three state rows side by side, so the slots come before `names` and
//    their values are short ("실행 중", not "실행 중 (Running)"); v7 was
//    never published either.
// 9: no screen change - the capability presentation of pcWatch was updated
//    seconds before v8 was packaged, and v8's device presentation was built
//    from the old one; a fresh name regenerates it. v8 was never published
//    either.
// 10: the watch card moved to a new capability id, pcWatchList
//    (`numbersystem53811.pcwatchlist`): v9's device presentation was still
//    built from pcWatch's FIRST presentation, ten minutes after the update -
//    a capability presentation is frozen at first use, like its definition.
//    v9 was never published either.
// 11: #136 - the default style is monitor (SmartMonitor), not others: the
//    Android app offers no "change icon" for an Others device. The bare name
//    now stands for monitor and others gets `pc-others.v11`; up to v10 the
//    bare name was others (profiles.PAST_DEFAULTS).
// 12: #136 again - SmartMonitor has no "change icon" option in the app
//    either; the default is switch (Switch, a category with an icon set).
//    `pc-monitor.v12` and `pc-others.v12` are the two named ones. v11 was
//    never published.
// 13: #136 - the switch default gave the app's own "change icon" option, so
//    the `iconStyle` preference and its eighteen variants are gone: two
//    profiles, Switch. v12 was never published either.
// 14: the PC app v1.3.0 cards - components `presence` (pcPresence,
//    pcPrivacy: #151/#152), `audio` (pcAudioOut, pcAudioIn: #154) and
//    `monitor` (pcMonitor, switch, switchLevel: #153). v13 was published
//    (edge 1.2.x), so its two files stay.
// 15: #158 - main down to seven capabilities; cards `media` (the media
//    group and the audio device slots), `presence` (+ pcUser), `actions`
//    (pcPreset, pcToast); the monitor and watch cards only on the variants
//    that have them: eight files. v14 was never published.
const VERSION = 15;

const CATEGORY = 'Switch';

// #158: the optional cards, in name order. Each has its `# @<card>-begin`
// and `# @<card>-end` markers in the template.
const CARDS = ['monitor', 'watch', 'battery'];

// The eight card sets, `pc.v15` first.
function variants() {
  const out = [];
  for (let bits = 0; bits < 1 << CARDS.length; bits++) {
    const has = {};
    CARDS.forEach((card, i) => { has[card] = (bits & (1 << (CARDS.length - 1 - i))) !== 0; });
    out.push(has);
  }
  return out;
}

function profileName(has) {
  return ['pc', ...CARDS.filter((card) => has[card])].join('-') + '.v' + VERSION;
}

// pc.v3 -> pc-v3.yml, pc-tv-battery.v3 -> pc-tv-battery-v3.yml
function fileName(name) {
  return name.replace(/\.v(\d+)$/, '-v$1') + '.yml';
}

function header(has) {
  return [
    '# GENERATED by tools/gen-profiles.js from tools/profile-template.yml - do not edit.',
    '# cards: ' + CARDS.map((card) => card + ' ' + (has[card] ? 'yes' : 'no')).join(' · '),
  ].join('\n') + '\n';
}

function replaceOnce(text, placeholder, value) {
  const parts = text.split(placeholder);
  if (parts.length !== 2) {
    throw new Error('the template must contain ' + placeholder + ' exactly once, found ' + (parts.length - 1));
  }
  return parts.join(value);
}

function render(template, has) {
  const lines = template.replace(/\r\n/g, '\n').split('\n');
  const first = lines.findIndex((line) => line.startsWith('name:'));
  if (first < 0) throw new Error('the template has no `name:` line');

  const out = [];
  const inside = {};
  for (const line of lines.slice(first)) {
    const marker = /^# @(\w+)-(begin|end)$/.exec(line);
    if (marker) {
      if (!CARDS.includes(marker[1])) throw new Error('unknown marker ' + line);
      inside[marker[1]] = marker[2] === 'begin';
    } else if (/^\s*#/.test(line)) {
      // a template comment
    } else if (!CARDS.some((card) => inside[card] && !has[card])) {
      out.push(line);
    }
  }
  for (const card of CARDS) {
    if (inside[card]) throw new Error('# @' + card + '-begin without # @' + card + '-end');
  }

  let body = out.join('\n');
  body = replaceOnce(body, '__NAME__', profileName(has));
  body = replaceOnce(body, '__CATEGORY__', CATEGORY);
  return header(has) + body;
}

function main() {
  const check = process.argv.includes('--check');
  const template = fs.readFileSync(TEMPLATE, 'utf8');
  const stale = [];
  let written = 0;
  const all = variants();

  for (const has of all) {
    const file = path.join(OUT_DIR, fileName(profileName(has)));
    const text = render(template, has);
    let current = null;
    try {
      current = fs.readFileSync(file, 'utf8').replace(/\r\n/g, '\n');
    } catch (e) {
      current = null;
    }
    if (current === text) continue;
    if (check) {
      stale.push(path.relative(EDGE, file));
    } else {
      fs.writeFileSync(file, text);
      written++;
    }
  }

  if (check) {
    if (stale.length > 0) {
      process.stderr.write('stale generated profiles (run `bun tools/gen-profiles.js`):\n  ' + stale.join('\n  ') + '\n');
      process.exit(1);
    }
    process.stdout.write('profiles are in sync with tools/profile-template.yml\n');
    return;
  }
  process.stdout.write('wrote ' + written + ' of ' + all.length + ' profiles\n');
}

main();