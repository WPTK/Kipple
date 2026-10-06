// node --test scripts/ci-prose.test.mjs
// Every line of scripts/ci-prose.txt must name something that exists, so a renamed or deleted file cannot leave a dead
// entry behind. That the listed files are really unread is not checked here by name: ci.yml deletes them
// (scripts/ci-prune-prose.sh) before the build and test steps, so a reader fails its own test.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const tracked = execFileSync('git', ['ls-files', '-z'], { cwd: root, encoding: 'utf8' }).split('\0').filter(Boolean);
const lines = readFileSync(new URL('./ci-prose.txt', import.meta.url), 'utf8')
  .replace(/\r\n/g, '\n')
  .split('\n')
  .filter((l) => l && !l.startsWith('#'));

// Only `*` is special in a list line.
const esc = (s) => [...s].map((c) => (/[A-Za-z0-9_]/.test(c) ? c : String.fromCharCode(92) + c)).join('');
const toRegExp = (g) => new RegExp('^' + g.split('*').map(esc).join('.*') + '$');

test('every line of the prose list matches a tracked file', () => {
  for (const l of lines) {
    const pattern = l.startsWith('!') ? l.slice(1) : l;
    assert.ok(tracked.some((f) => toRegExp(pattern).test(f)), `${l}: matches no tracked file`);
  }
});

test('files that code is known to read are not on the list', () => {
  const allow = lines.filter((l) => !l.startsWith('!')).map(toRegExp);
  const deny = lines.filter((l) => l.startsWith('!')).map((l) => toRegExp(l.slice(1)));
  const listed = (f) => allow.some((r) => r.test(f)) && !deny.some((r) => r.test(f));
  for (const f of ['docs/design.md', 'docs/deploy.md', 'README.md', 'CHANGELOG.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md']) {
    assert.ok(!listed(f), `${f} is read by code and must not be on the list`);
  }
});
