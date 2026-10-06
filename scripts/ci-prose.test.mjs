// node --test scripts/ci-prose.test.mjs
// scripts/ci-prose.txt lists the files whose changes skip the build and test jobs. This fails if code names one of them
// (Dockerfile COPY, go:embed, a Go or web test, a script, the web build), because then editing that file could break
// something CI no longer checks. Remove the file from the list when you add such a reader.
//
// Detection is by name, not by read function, so it does not depend on how a reader is spelled: any quoted string
// whose whole value is a listed path (leading ./ and ../ ignored), a directory holding listed files, or a glob that
// matches one is a reader. Prose that merely mentions a path inside a sentence is not. In shell, PowerShell and
// Dockerfile sources every word of a non-comment line counts too.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const tracked = execFileSync('git', ['ls-files', '-z'], { cwd: root, encoding: 'utf8' }).split('\0').filter(Boolean);

const esc = (s) => [...s].map((c) => (/[A-Za-z0-9_]/.test(c) ? c : String.fromCharCode(92) + c)).join('');
// Only `*` is special in a list line, here and in ci-changes.sh.
const globToRegExp = (g) => new RegExp('^' + g.split('*').map(esc).join('.*') + '$');

export function proseFiles(listText, files) {
  const entries = listText.replace(/\r\n/g, '\n').split('\n').filter((l) => l && !l.startsWith('#'));
  const allow = entries.filter((l) => !l.startsWith('!')).map(globToRegExp);
  const deny = entries.filter((l) => l.startsWith('!')).map((l) => globToRegExp(l.slice(1)));
  return files.filter((f) => allow.some((r) => r.test(f)) && !deny.some((r) => r.test(f)));
}

const BS = String.fromCharCode(92);
const stripRelative = (v) => v.split(BS).join('/').replace(/^(\.{1,2}\/)+/, '');
const isEmbed = (l) => /^\s*\/\/go:embed\b/.test(l);
const isComment = (l) => !isEmbed(l) && /^\s*(\/\/|#|\*|\/\*)/.test(l);
const SHELLISH = /(\.(sh|ps1)|(^|\/)Dockerfile)$/;
// A bare word ("changes", "docs", "web") is not a path: a job, a label or a client name can be spelled the same.
const pathLike = (v) => v.includes('/') || /[.][A-Za-z0-9]+$/.test(v) || v.includes('*');
// Commands whose bare words are paths.
const FILE_COMMAND = /\b(COPY|ADD|cat|cp|mv|ls|cd|source|Get-Content|Copy-Item|tar|rsync)\b/;

// Does this value name a listed file, a directory holding one, or a glob matching one? `bareOk` says a bare word is a
// path here because it is an argument of a join call or of a file command.
function namesProse(value, prose, all, bareOk = false) {
  const v = stripRelative(value.trim());
  if (!v || (!bareOk && !pathLike(v))) return null;
  if (prose.includes(v)) return v;
  const dir = v.replace(/\/+$/, '');
  // A directory holding listed files. A bare word such as "web" is only a directory when most of what is tracked
  // under it is listed, so ordinary words like "web" or "docs" in a Go string do not drown the real readers.
  if (!v.includes('*') && dir && prose.some((p) => p.startsWith(dir + '/'))) {
    const under = (all ?? prose).filter((f) => f.startsWith(dir + '/'));
    if (v.includes('/') || under.filter((f) => prose.includes(f)).length * 2 >= under.length) return `${dir}/ (a directory holding listed files)`;
  }
  // A glob, when it looks like a path pattern (has a / or ends in a Markdown or YAML extension).
  if (v.includes('*') && ((v.includes('/') && !v.startsWith('*')) || /[*][.](md|ya?ml)$/.test(v))) {
    const re = globToRegExp(v);
    const hit = prose.find((p) => re.test(p));
    if (hit) return `${v} (a glob matching ${hit})`;
  }
  return null;
}

// Sources the guard knows read nothing in the PR's build or tests; each needs a reason.
const ALLOWED = {
  // `changelog.mjs check` is not gated by ci.yml, so reading the fragments cannot go unchecked.
  'scripts/changelog.mjs': ['changes'],
  // .dockerignore keeps docs, *.md (except two) and the listed web files out of the build context.
  Dockerfile: ['web/'],
  // links.yml runs on its own for every Markdown change, not through ci.yml.
  'scripts/check-links.mjs': ['*.md'],
  // The local CI runner mirrors ci.yml and links.yml; it reads no file as input to a test.
  'scripts/ci-local.ps1': ['*.md'],
};

export function findReaders(sources, prose, all) {
  const out = [];
  for (const { name, text } of sources) {
    const allowed = ALLOWED[name] ?? [];
    text.split('\n').forEach((line, i) => {
      if (isComment(line)) return;
      const values = [];
      const literals = [...line.matchAll(/(["'`])((?:(?!\1)[^\n])*)\1/g)].map((m) => m[2]);
      for (const v of literals) values.push([v, false]);
      // join("..", "docs", "x.md") reads docs/x.md; join("changes", f) reads the changes directory.
      for (const m of line.matchAll(/\b(?:join|Join|resolve|Resolve)\s*\(([^\n]*)/g)) {
        const parts = [...m[1].matchAll(/(["'`])((?:(?!\1)[^\n])*)\1/g)].map((x) => x[2]).filter((x) => x !== '.' && x !== '..');
        if (parts.length) values.push([parts.join('/'), true]);
      }
      if (isEmbed(line)) values.push(...line.split(/\s+/).slice(1).map((v) => [v, false]));
      if (SHELLISH.test(name)) {
        const bare = FILE_COMMAND.test(line);
        values.push(...line.split(/[\s"'`()=,;|<>]+/).map((v) => [v, bare]));
      }
      for (const [v, bareOk] of values) {
        const hit = namesProse(v, prose, all, bareOk);
        if (hit && !allowed.includes(stripRelative(v.trim()))) out.push(`${name}:${i + 1} names ${hit}: ${line.trim().slice(0, 100)}`);
      }
    });
  }
  return out;
}

const prose = proseFiles(readFileSync(new URL('./ci-prose.txt', import.meta.url), 'utf8'), tracked);
const isSource = (f) =>
  f !== 'scripts/ci-prose.test.mjs' &&
  f !== 'scripts/ci-changes.test.sh' && // its fixtures are made-up paths in a throwaway repository
  /(\.(go|mjs|cjs|js|ts|tsx|ps1|sh)|(^|\/)Dockerfile)$/.test(f) &&
  !f.startsWith('web/node_modules/');

test('the prose list matches files that exist', () => {
  assert.ok(prose.length > 5, `only ${prose.length} files on the list`);
  assert.ok(prose.includes('CONTRIBUTING.md'));
  for (const f of ['docs/design.md', 'docs/deploy.md', 'README.md', 'CHANGELOG.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md']) {
    assert.ok(!prose.includes(f), `${f} is read by code and must not be on the list`);
  }
});

test('no code names a file on the prose list', () => {
  const sources = tracked.filter(isSource).map((name) => ({ name, text: readFileSync(`${root}${name}`, 'utf8') }));
  assert.deepEqual(findReaders(sources, prose, tracked), [], 'remove these files from scripts/ci-prose.txt, or stop naming them');
});

// The detector itself: each of these readers must be caught, and plain mentions must not be.
test('the guard catches the ways code reads a file', () => {
  const p = ['docs/RELEASING.md', 'docs/troubleshooting.md', 'SECURITY.md', 'web/ACCESSIBILITY.md'];
  const caught = [
    ['a.mjs', "readFileSync('docs/RELEASING.md')"],
    ['a.mjs', "readFileSync(join('..', 'docs', 'x.md'))\nconst f = '../docs/RELEASING.md';\nread(f);"],
    ['a.mjs', "const f = '../docs/RELEASING.md';\nreadFileSync(f);"],
    ['a_test.go', 'b, _ := os.ReadFile("../../SECURITY.md")'],
    ['a.mjs', "new URL('../web/ACCESSIBILITY.md', import.meta.url)"],
    ['a_test.go', 'var userDocs = []string{"README.md",\n\t"docs/troubleshooting.md"}'],
    ['Dockerfile', 'COPY docs/ /app/docs/'],
    ['Dockerfile', 'COPY SECURITY.md /licenses/'],
    ['a.ts', "import.meta.glob('../../docs/*.md', { query: '?raw' })"],
    ['a.sh', 'cat SECURITY.md'],
    ['a.ps1', 'Get-Content docs/RELEASING.md'],
    ['a.go', '//go:embed docs/RELEASING.md'],
    ['a_test.go', 'os.ReadFile(filepath.Join("..", "..", "docs", "RELEASING.md"))'],
    ['a_test.go', 'entries, _ := os.ReadDir(filepath.Join("..", "..", "docs"))'],
    ['a.mjs', "readdirSync(path.join(root, 'docs'))"],
    ['a.mjs', "fs.readFileSync(path.join('docs', name))"],
    ['a.mjs', "const f = 'docs" + BS + "RELEASING.md'; // a Windows path"],
    ['Dockerfile', 'COPY docs /app/docs'],
  ];
  for (const [name, text] of caught) {
    assert.notDeepEqual(findReaders([{ name, text }], p), [], `not caught: ${name}: ${text}`);
  }
  const mentions = [
    ['a.go', 'return fmt.Errorf("see docs/RELEASING.md for the steps")'],
    ['a.ts', '// docs/RELEASING.md describes this'],
    ['a.sh', '# cat SECURITY.md'],
    ['a.ts', 'const x = "README.md"'],
    ['a.mjs', "for (const j of ['docs', 'changes', 'go']) run(j);"],
    ['a.go', 'client := "docs"; label("changes")'],
    ['a.sh', 'echo docs changes'],
  ];
  for (const [name, text] of mentions) {
    assert.deepEqual(findReaders([{ name, text }], p), [], `false alarm: ${name}: ${text}`);
  }
});

test('the allowlist file rules: * is the only special character, ! removes', () => {
  const files = ['docs/a.md', 'docs/design.md', 'docs/x?.md', 'a.md'];
  assert.deepEqual(proseFiles('docs/*.md\n!docs/design.md\n', files), ['docs/a.md', 'docs/x?.md']);
  assert.deepEqual(proseFiles('docs/x?.md\n', files), ['docs/x?.md']);
});
