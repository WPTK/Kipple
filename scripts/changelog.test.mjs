// node --test scripts/changelog.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import {
  readFragments, renderSections, checkUnreleased, release, notes, parseReleaseArgs, localDate, POINTER,
  isVersion, topVersion, pinExamples, checkExamples,
} from './changelog.mjs';

function dirWith(files) {
  const dir = mkdtempSync(join(tmpdir(), 'changes-'));
  for (const [name, body] of Object.entries(files)) writeFileSync(join(dir, name), body);
  return dir;
}

const CHANGELOG = `# Changelog

## [Unreleased]

${POINTER}

## [0.3.0-beta.1] - 2026-09-27

Intro.

### Fixed

- Old thing.

[Unreleased]: https://github.com/WPTK/Kipple/compare/v0.3.0-beta.1...HEAD
[0.3.0-beta.1]: https://github.com/WPTK/Kipple/compare/v0.3.0-alpha.7...v0.3.0-beta.1
`;

test('readFragments accepts good files and ignores README, _intro and .gitkeep', () => {
  const dir = dirWith({ 'a.fixed.md': 'A fix. (#1)\n', 'b.added.md': 'Line one\nline two\n', 'README.md': '# x', '_intro.md': 'i', '.gitkeep': '' });
  try {
    const { fragments, errors } = readFragments(dir);
    assert.deepEqual(errors, []);
    assert.deepEqual(fragments.map((f) => [f.id, f.kind]), [['a', 'fixed'], ['b', 'added']]);
  } finally {
    rmSync(dir, { recursive: true });
  }
});

test('readFragments reports bad names and bodies', () => {
  const dir = dirWith({
    'Bad Name.fixed.md': 'x',
    'a.oops.md': 'x',
    'empty.fixed.md': '  \n',
    'bullet.fixed.md': '- already a bullet',
    'heading.fixed.md': '### Fixed',
    'two.fixed.md': 'one\n\ntwo',
    'notes.txt': 'x',
  });
  try {
    const { fragments, errors } = readFragments(dir);
    assert.equal(fragments.length, 0);
    assert.equal(errors.length, 7);
  } finally {
    rmSync(dir, { recursive: true });
  }
});

test('renderSections orders kinds like Keep a Changelog, merges same kinds, sorts ids naturally, indents wraps', () => {
  const out = renderSections([
    { id: 'pr10-x', kind: 'fixed', text: 'ten' },
    { id: 'pr9-x', kind: 'fixed', text: 'nine' },
    { id: 'a', kind: 'security', text: 'sec' },
    { id: 'b', kind: 'added', text: 'add\nwrapped' },
    { id: 'c', kind: 'changed', text: 'chg' },
  ]);
  assert.equal(out, '### Added\n\n- add\n  wrapped\n\n### Changed\n\n- chg\n\n### Fixed\n\n- nine\n- ten\n\n### Security\n\n- sec\n');
  assert.equal(out.match(/### Changed/g).length, 1);
});

test('checkUnreleased passes on the pointer and fails on hand-written entries', () => {
  assert.deepEqual(checkUnreleased(CHANGELOG), []);
  assert.equal(checkUnreleased(CHANGELOG.replace(POINTER, `${POINTER}\n\n### Fixed\n\n- oops`)).length, 1);
  assert.equal(checkUnreleased(CHANGELOG.replace(POINTER, '')).length, 1);
});

test('release inserts the section, keeps the pointer, and updates compare links', () => {
  const out = release(CHANGELOG, {
    version: '0.3.0-beta.2',
    date: '2026-10-01',
    intro: 'Beta two.',
    fragments: [{ id: 'a', kind: 'fixed', text: 'Fixed it.' }],
  });
  assert.match(out, /## \[Unreleased\]\n\nChanges not yet in a release[^\n]*\n\n## \[0\.3\.0-beta\.2\] - 2026-10-01\n\nBeta two\.\n\n### Fixed\n\n- Fixed it\.\n\n## \[0\.3\.0-beta\.1\]/);
  assert.match(out, /\[Unreleased\]: \S+\/compare\/v0\.3\.0-beta\.2\.\.\.HEAD\n\[0\.3\.0-beta\.2\]: \S+\/compare\/v0\.3\.0-beta\.1\.\.\.v0\.3\.0-beta\.2\n\[0\.3\.0-beta\.1\]/);
  assert.deepEqual(checkUnreleased(out), []);
  assert.equal(notes(out, '0.3.0-beta.2'), 'Beta two.\n\n### Fixed\n\n- Fixed it.\n');
});

test('release without an intro has no stray blank paragraph', () => {
  const out = release(CHANGELOG, { version: '0.3.0', date: '2026-10-01', fragments: [{ id: 'a', kind: 'added', text: 'New.' }] });
  assert.match(out, /## \[0\.3\.0\] - 2026-10-01\n\n### Added\n\n- New\.\n\n## \[0\.3\.0-beta\.1\]/);
});

test('release refuses a bad version, a duplicate version, no fragments, and a hand-edited Unreleased', () => {
  const frag = [{ id: 'a', kind: 'fixed', text: 'x' }];
  assert.throws(() => release(CHANGELOG, { version: 'v1', date: '2026-10-01', fragments: frag }), /not X\.Y\.Z/);
  assert.throws(() => release(CHANGELOG, { version: '0.3.0-beta.1', date: '2026-10-01', fragments: frag }), /already has/);
  assert.throws(() => release(CHANGELOG, { version: '0.3.0', date: 'today', fragments: frag }), /YYYY-MM-DD/);
  assert.throws(() => release(CHANGELOG, { version: '0.3.0', date: '2026-10-01', fragments: [] }), /nothing to release/);
  assert.throws(() => release(CHANGELOG.replace(POINTER, 'hand written'), { version: '0.3.0', date: '2026-10-01', fragments: frag }), /pointer/);
});

test('notes returns one section and rejects a missing version', () => {
  assert.equal(notes(CHANGELOG, '0.3.0-beta.1'), 'Intro.\n\n### Fixed\n\n- Old thing.\n');
  assert.throws(() => notes(CHANGELOG, '9.9.9'), /no section/);
});

test('parseReleaseArgs takes the version and options in any order', () => {
  assert.deepEqual(parseReleaseArgs(['0.3.0', '--date', '2026-10-01']), { version: '0.3.0', date: '2026-10-01', dryRun: false });
  assert.deepEqual(parseReleaseArgs(['--dry-run', '--date', '2026-10-01', '0.3.0']), { version: '0.3.0', date: '2026-10-01', dryRun: true });
  assert.equal(parseReleaseArgs(['0.3.0']).date, localDate());
  assert.throws(() => parseReleaseArgs(['--dry-run']), /needs a version/);
  assert.throws(() => parseReleaseArgs(['0.3.0', '0.4.0']), /unexpected/);
  assert.throws(() => parseReleaseArgs(['0.3.0', '--bogus']), /unknown option/);
});

test('localDate uses local calendar fields', () => {
  assert.equal(localDate(new Date(2026, 9, 1, 23, 30)), '2026-10-01');
});

// One list of vectors for both grammars: changelog.mjs (what `release` accepts) and release-tags.sh (what the release
// gate accepts). A version `release` would write must be one the gate would let through, and the reverse.
const GOOD = ['0.0.0', '1.2.3', '10.20.30', '1.2.3-alpha.1', '1.2.3-beta.12', '1.2.3-rc.3'];
const BAD = ['01.2.3', '1.02.3', '1.2.03', '1.2.3-alpha.0', '1.2.3-alpha.01', '1.2.3-gamma.1', '1.2', '1.2.3.4', '1.2.3-rc', '1.2.3-rc.1-x', ' 1.2.3', '1.2.3\n9.9.9', '', 'v1.2.3'];
// The first bash that can run the gate script wins. On Windows `bash` on the PATH can be the WSL launcher, which cannot
// open a C:\ script path, so Git's own bash is tried next.
const GATE_SCRIPT = fileURLToPath(new URL('./release-tags.sh', import.meta.url));
const run = (shell, tag) => spawnSync(shell, [GATE_SCRIPT, 'check-tag', tag], { encoding: 'utf8' });
const SHELLS = ['bash', ...(process.platform === 'win32' ? [join(process.env.ProgramFiles ?? 'C:/Program Files', 'Git', 'bin', 'bash.exe')] : [])];
const shell = SHELLS.find((s) => run(s, 'v1.2.3').status === 0);
const gate = (tag) => run(shell, tag);

test('isVersion: no leading zeros, prerelease numbers start at 1', () => {
  for (const v of GOOD) assert.equal(isVersion(v), true, v);
  for (const v of BAD) assert.equal(isVersion(v), false, JSON.stringify(v));
  assert.throws(() => release(CHANGELOG, { version: '01.0.0', date: '2026-10-01', fragments: [{ id: 'a', kind: 'fixed', text: 'x' }] }), /not X\.Y\.Z/);
});

test('isVersion agrees with the release gate (release-tags.sh check-tag)', (t) => {
  if (!shell) {
    // A machine without a usable bash may skip; CI must not, or a broken gate script would pass unnoticed.
    if (process.env.CI) assert.fail('no bash can run release-tags.sh');
    return t.skip('no bash can run release-tags.sh');
  }
  for (const v of GOOD) assert.equal(gate(`v${v}`).status, 0, `gate refuses v${v}`);
  // A newline in argv does not survive spawnSync into Git Bash on Windows; release-tags.test.sh covers that vector itself.
  for (const v of BAD.filter((x) => x !== 'v1.2.3' && !x.includes('\n'))) assert.notEqual(gate(`v${v}`).status, 0, `gate accepts v${JSON.stringify(v)}`);
});

const DOC = (v) => `Run ghcr.io/wptk/kipple:${v} and\n    image: ghcr.io/wptk/kipple:${v}\nverify ghcr.io/wptk/kipple:${v} \\nsee ghcr.io/wptk/kipple:<version>\n`;

test('topVersion is the first released heading under [Unreleased]', () => {
  assert.equal(topVersion(CHANGELOG), '0.3.0-beta.1');
  assert.throws(() => topVersion(`# C\n\n## [Unreleased]\n\n${POINTER}\n`), /no released version/);
});

test('the top subcommand prints the version the release gate compares the tag with', () => {
  const root = fileURLToPath(new URL('..', import.meta.url));
  const out = spawnSync(process.execPath, [fileURLToPath(new URL('./changelog.mjs', import.meta.url)), 'top'], { encoding: 'utf8' });
  assert.equal(out.status, 0, out.stderr);
  assert.equal(out.stdout, `${topVersion(readFileSync(join(root, 'CHANGELOG.md'), 'utf8'))}\n`);
  assert.equal(isVersion(out.stdout.trim()), true);
});

test('pinExamples rewrites every image tag, prerelease or not, and nothing else', () => {
  assert.equal(pinExamples(DOC('0.3.0-beta.1'), '0.3.0'), DOC('0.3.0'));
  assert.equal(pinExamples(DOC('0.3.0'), '0.4.0-rc.2'), DOC('0.4.0-rc.2'));
  const other = 'first published with release 0.3.0-beta.1; ghcr.io/other/kipple:0.1.0\n';
  assert.equal(pinExamples(other, '9.9.9'), other);
});

test('checkExamples flags a stale tag and a required file with none', () => {
  const file = { path: 'README.md', required: true };
  assert.deepEqual(checkExamples(DOC('0.3.0'), '0.3.0', file), []);
  const stale = checkExamples(DOC('0.2.0'), '0.3.0', file);
  assert.equal(stale.length, 3);
  assert.match(stale[0], /README\.md: example image tag is 0\.2\.0, the newest CHANGELOG version is 0\.3\.0/);
  assert.match(checkExamples('nothing here', '0.3.0', file)[0], /no example image tag/);
  assert.deepEqual(checkExamples('nothing here', '0.3.0', { path: 'docs/deploy.md', required: false }), []);
});

test('release then check: the example files end up consistent with the new top version', () => {
  const out = release(CHANGELOG, { version: '0.4.0', date: '2026-10-01', fragments: [{ id: 'a', kind: 'fixed', text: 'A fix.' }] });
  const v = topVersion(out);
  assert.equal(v, '0.4.0');
  assert.equal(checkExamples(pinExamples(DOC('0.3.0-beta.1'), v), v, { path: 'README.md', required: true }).length, 0);
});
