// node --test scripts/uat-labels.test.mjs
// Tests for scripts/uat-labels.mjs. Each test names the behavior it protects. No git, no browser: inputs are inline.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { check, extractFragments, findUsages, goneFragments, parseDiff, staticParts } from './uat-labels.mjs';

const uat = (text) => [{ name: 'web/uat/run.mjs', text }];

test('staticParts: drops the ${...} holes of a template literal', () => {
  assert.deepEqual(staticParts('Select ${f.title}, now'), ['Select ', ', now']);
  assert.deepEqual(staticParts('${x}'), []);
});

test('extractFragments: reads double-quoted, single-quoted and braced string values', () => {
  assert.deepEqual(extractFragments('<button aria-label="More actions">'), ['More actions']);
  assert.deepEqual(extractFragments("<button aria-label='Search tips'>"), ['Search tips']);
  assert.deepEqual(extractFragments('<b aria-label={selecting ? "Done" : "Select"}>'), ['Done', 'Select']);
});

test('extractFragments: keeps the static text of a template literal, including its trailing space', () => {
  assert.deepEqual(extractFragments('<b aria-label={`Layout: ${name}`}>'), ['Layout: ']);
});

test('extractFragments: reads the ariaLabel prop spelling and ignores other attributes', () => {
  assert.deepEqual(extractFragments('<Icon ariaLabel="Close" title="Not this">'), ['Close']);
  assert.deepEqual(extractFragments('<p title="Nope">'), []);
});

test('extractFragments: a value that is a variable has no static text', () => {
  assert.deepEqual(extractFragments('<b aria-label={label}>'), []);
});

test('parseDiff: collects removed and added fragments per file', () => {
  const diff = [
    'diff --git a/web/src/A.tsx b/web/src/A.tsx',
    '--- a/web/src/A.tsx',
    '+++ b/web/src/A.tsx',
    '@@ -10 +10 @@',
    '-<button aria-label={`Layout: ${x}`}>',
    '+<button aria-label={`List layout, ${x}`}>',
  ].join('\n');
  assert.deepEqual(parseDiff(diff), [{ file: 'web/src/A.tsx', removed: ['Layout: '], added: ['List layout, '] }]);
});

test('goneFragments: a removed label that still exists elsewhere in the source is not gone', () => {
  const files = [{ file: 'a.tsx', removed: ['Settings sections'] }];
  assert.deepEqual(goneFragments(files, new Set(['Settings sections'])), []);
});

test('goneFragments: a removed label missing from the current source is gone', () => {
  const files = [{ file: 'a.tsx', removed: ['Layout: '] }];
  assert.deepEqual(goneFragments(files, new Set(['List layout, '])), [{ fragment: 'Layout: ', file: 'a.tsx' }]);
});

test('goneFragments: ignores fragments shorter than three characters once trimmed', () => {
  assert.deepEqual(goneFragments([{ file: 'a.tsx', removed: [', ', 'OK'] }], new Set()), []);
});

test('findUsages: reports the file and line where the old label appears', () => {
  const hits = findUsages('Layout: ', uat('const a = 1;\nawait page.click(\'button[aria-label^="Layout: "]\');\n'));
  assert.equal(hits.length, 1);
  assert.equal(hits[0].line, 2);
  assert.equal(hits[0].name, 'web/uat/run.mjs');
});

test('findUsages: a line carrying the ignore comment is exempt', () => {
  const hits = findUsages('Select', uat('// the word Select in a comment, uat-labels: ignore\n'));
  assert.deepEqual(hits, []);
});

test('check: fails when a renamed label is still used by the UAT scripts (the folder-layout rename case)', () => {
  const diff = ['+++ b/web/src/A.tsx', '-<b aria-label={`Layout: ${x}`}>', '+<b aria-label={`List layout, ${x}`}>'].join('\n');
  const problems = check(diff, new Set(['List layout, ']), uat('page.locator(\'[aria-label^="Layout: "]\')'));
  assert.equal(problems.length, 1);
  assert.equal(problems[0].fragment, 'Layout: ');
  assert.equal(problems[0].usages[0].line, 1);
});

test('check: passes when the UAT scripts were updated to the new label', () => {
  const diff = ['+++ b/web/src/A.tsx', '-<b aria-label="Old name">', '+<b aria-label="New name">'].join('\n');
  assert.deepEqual(check(diff, new Set(['New name']), uat('click("New name")')), []);
});

test('check: passes when a removed label was never used by the UAT scripts', () => {
  const diff = ['+++ b/web/src/A.tsx', '-<b aria-label="Unused label">'].join('\n');
  assert.deepEqual(check(diff, new Set(), uat('click("Something else")')), []);
});

test('check: passes for a diff with no aria-label changes', () => {
  assert.deepEqual(check('+++ b/web/src/A.tsx\n-const a = 1;\n+const a = 2;', new Set(), uat('')), []);
});
