// node --test scripts/changelog.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { readFragments, renderSections, checkUnreleased, release, notes, parseReleaseArgs, localDate, POINTER } from './changelog.mjs';

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
