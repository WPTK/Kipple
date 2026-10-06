// node --test scripts/ci-prose.test.mjs
// scripts/ci-prose.txt lists the files whose changes skip the build and test jobs. This fails if code reads one of them
// (Dockerfile COPY, go:embed, a Go or web test, a script, the web build), because then editing that file could break
// something CI no longer checks. Remove the file from the list when you add such a reader.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const tracked = execFileSync('git', ['ls-files', '-z'], { cwd: root, encoding: 'utf8' }).split('\0').filter(Boolean);

const esc = (s) => [...s].map((c) => (/[A-Za-z0-9_]/.test(c) ? c : String.fromCharCode(92) + c)).join('');
const globToRegExp = (g) => new RegExp('^' + g.split('*').map(esc).join('.*') + '$');
const entries = readFileSync(new URL('./ci-prose.txt', import.meta.url), 'utf8')
  .replace(/\r\n/g, '\n')
  .split('\n')
  .filter((l) => l && !l.startsWith('#'));
const allow = entries.filter((l) => !l.startsWith('!')).map(globToRegExp);
const deny = entries.filter((l) => l.startsWith('!')).map((l) => globToRegExp(l.slice(1)));
const prose = tracked.filter((f) => allow.some((r) => r.test(f)) && !deny.some((r) => r.test(f)));

// A line that reads, embeds or copies a file.
const READS = /readRepoFile|ReadFile|readFileSync|createReadStream|existsSync|go:embed|^\s*(COPY|ADD)\b|\?raw|\bimport\b[^\n]*\.md\b|\bpath:\s*['"`]/;
const isSource = (f) =>
  f !== 'scripts/ci-prose.test.mjs' &&
  /(\.(go|mjs|cjs|js|ts|tsx|ps1|sh|ya?ml)|(^|\/)Dockerfile)$/.test(f) &&
  !f.startsWith('web/node_modules/') &&
  !f.endsWith('package-lock.json');
const isComment = (l) => /^\s*(\/\/|#|\*|\/\*)/.test(l);

test('the prose list matches files that exist', () => {
  assert.ok(prose.length > 5, `only ${prose.length} files on the list`);
  assert.ok(prose.includes('CONTRIBUTING.md'));
  for (const f of ['docs/design.md', 'docs/deploy.md', 'README.md', 'CHANGELOG.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md']) {
    assert.ok(!prose.includes(f), `${f} is read by code and must not be on the list`);
  }
});

test('no code reads a file on the prose list', () => {
  const offences = [];
  for (const src of tracked.filter(isSource)) {
    const lines = readFileSync(`${root}${src}`, 'utf8').split('\n');
    lines.forEach((line, i) => {
      if (isComment(line) || !READS.test(line)) return;
      for (const p of prose) {
        const base = p.slice(p.lastIndexOf('/') + 1);
        // A bare file name only counts when no other tracked file shares it (README.md is in several folders).
        const names = tracked.filter((t) => t.endsWith('/' + base) || t === base).length === 1 ? esc(base) : esc(p);
        const re = new RegExp('(^|[^A-Za-z0-9_./-])(' + esc(p) + '|' + names + ')($|[^A-Za-z0-9_-])');
        if (re.test(line)) offences.push(`${src}:${i + 1} reads ${p}: ${line.trim().slice(0, 100)}`);
      }
    });
  }
  assert.deepEqual(offences, [], 'remove these files from scripts/ci-prose.txt, or stop reading them');
});
