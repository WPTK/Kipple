// node --test scripts/check-links.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { classify, extractLinks, checkTexts, report, loadSettings, isExampleHost } from './check-links.mjs';

const settings = {
  timeoutMs: 1000, deadlineMs: 600000, retries: 2, retryDelayMs: 0, workers: 4, perHostConcurrency: 2,
  waybackMaxAgeDays: 730, blockedHosts: ['blocked.test-host.com'], ignoredUrls: [],
};
const NOW = Date.UTC(2026, 9, 5);
const noSleep = async () => {};

const stamp = (daysAgo) => {
  const d = new Date(NOW - daysAgo * 86400000);
  return d.toISOString().replace(/[-:T]/g, '').slice(0, 14);
};
const resp = (status, body) => ({ status, ok: status >= 200 && status < 300, body: null, json: async () => body });

// A stub fetch: `pages` maps a URL to a status (or a function returning one); the archive answers from `archive`.
function stub(pages, archive = {}) {
  const calls = [];
  const fn = async (url) => {
    calls.push(url);
    if (url.startsWith('https://archive.org/wayback/available?url=')) {
      const target = decodeURIComponent(url.split('url=')[1]);
      return resp(200, { archived_snapshots: archive[target] ? { closest: archive[target] } : {} });
    }
    const p = pages[url];
    if (p instanceof Error) throw p;
    return resp(typeof p === 'function' ? p() : p ?? 404);
  };
  fn.calls = calls;
  return fn;
}
const run = (url, pages, archive, s = settings) => classify(url, s, { fetchFn: stub(pages, archive), sleepFn: noSleep, now: NOW });

test('2xx is ok', async () => {
  assert.equal((await run('https://a.com/x', { 'https://a.com/x': 200 })).bucket, 'ok');
});

test('404 and 410 are broken without asking the archive', async () => {
  for (const s of [404, 410]) {
    const fetchFn = stub({ 'https://a.com/x': s }, { 'https://a.com/x': { available: true, status: '200', timestamp: stamp(1) } });
    const r = await classify('https://a.com/x', settings, { fetchFn, sleepFn: noSleep, now: NOW });
    assert.equal(r.bucket, 'broken');
    assert.equal(fetchFn.calls.length, 1);
  }
});

test('403 with a recent 200 snapshot is blocked, not broken', async () => {
  const r = await run('https://a.com/x', { 'https://a.com/x': 403 }, { 'https://a.com/x': { available: true, status: '200', timestamp: stamp(30) } });
  assert.equal(r.bucket, 'blocked');
});

test('403 with no snapshot, a stale one, or a failing snapshot is broken', async () => {
  const pages = { 'https://a.com/x': 403 };
  assert.equal((await run('https://a.com/x', pages, {})).bucket, 'broken');
  assert.equal((await run('https://a.com/x', pages, { 'https://a.com/x': { available: true, status: '200', timestamp: stamp(731) } })).bucket, 'broken');
  assert.equal((await run('https://a.com/x', pages, { 'https://a.com/x': { available: true, status: '404', timestamp: stamp(1) } })).bucket, 'broken');
});

test('the snapshot age limit comes from the settings', async () => {
  const archive = { 'https://a.com/x': { available: true, status: '200', timestamp: stamp(100) } };
  const pages = { 'https://a.com/x': 401 };
  assert.equal((await run('https://a.com/x', pages, archive, { ...settings, waybackMaxAgeDays: 90 })).bucket, 'broken');
  assert.equal((await run('https://a.com/x', pages, archive, { ...settings, waybackMaxAgeDays: 365 })).bucket, 'blocked');
});

// A page that answers `page` while the archive answers with `archiveReply(callNumber)`.
const withArchive = (page, archiveReply) => {
  let n = 0;
  const fn = async (url) => {
    if (!url.startsWith('https://archive.org/')) return resp(page);
    n++;
    return archiveReply(n);
  };
  fn.archiveCalls = () => n;
  return fn;
};
const unavailable = async (fetchFn) => classify('https://a.com/x', settings, { fetchFn, sleepFn: noSleep, now: NOW });

test('an archive that cannot answer never turns a refusal into broken', async () => {
  const cases = {
    'network error': () => { throw new Error('down'); },
    timeout: () => { throw Object.assign(new Error('t'), { name: 'TimeoutError' }); },
    '429 throughout': () => resp(429),
    '503': () => resp(503),
    '404': () => resp(404),
    'malformed JSON': () => ({ status: 200, ok: true, json: async () => { throw new SyntaxError('bad'); } }),
    'no archived_snapshots key': () => resp(200, { unexpected: true }),
  };
  for (const [name, reply] of Object.entries(cases)) {
    const r = await unavailable(withArchive(403, reply));
    assert.deepEqual([name, r.bucket, r.detail], [name, 'blocked', '403, archive unavailable']);
  }
});

test('an archive 429 waits and retries, and a later answer decides', async () => {
  const sleeps = [];
  const fetchFn = withArchive(403, (n) => (n < 3 ? resp(429) : resp(200, { archived_snapshots: { closest: { available: true, status: '200', timestamp: stamp(5) } } })));
  const r = await classify('https://a.com/x', { ...settings, retryDelayMs: 10 }, { fetchFn, sleepFn: async (ms) => { sleeps.push(ms); }, now: NOW });
  assert.equal(r.bucket, 'blocked');
  assert.match(r.detail, /live in the Internet Archive/);
  assert.equal(fetchFn.archiveCalls(), 3);
  assert.deepEqual(sleeps, [10, 20]);
});

test('archive lookups run one at a time', async () => {
  let active = 0;
  let peak = 0;
  const fetchFn = async (url) => {
    if (!url.startsWith('https://archive.org/')) return resp(403);
    active++; peak = Math.max(peak, active);
    await new Promise((r) => setTimeout(r, 5));
    active--;
    return resp(200, { archived_snapshots: {} });
  };
  const text = Array.from({ length: 6 }, (_, i) => `https://h${i}.com/p`).join(' ');
  const out = await checkTexts([{ name: 'a.md', text }], { ...settings, workers: 6 }, { fetchFn, sleepFn: noSleep, now: NOW });
  assert.equal(out.counts.broken, 6);
  assert.equal(peak, 1);
});

test('other 4xx refusals (400, 405, 451) take the archive path like 403', async () => {
  for (const s of [400, 405, 451]) {
    const live = { 'https://a.com/x': { available: true, status: '200', timestamp: stamp(3) } };
    assert.equal((await run('https://a.com/x', { 'https://a.com/x': s }, live)).bucket, 'blocked', String(s));
    assert.equal((await run('https://a.com/x', { 'https://a.com/x': s }, {})).bucket, 'broken', String(s));
  }
});

test('a non-standard status such as 999 is blocked with its code and is not retried', async () => {
  const fetchFn = stub({ 'https://a.com/x': 999 });
  const r = await classify('https://a.com/x', settings, { fetchFn, sleepFn: noSleep, now: NOW });
  assert.deepEqual([r.bucket, r.detail], ['blocked', '999, non-standard status']);
  assert.equal(fetchFn.calls.length, 1);
});

test('a 5xx that turns into 404 on the retry is broken as 404', async () => {
  let n = 0;
  const r = await run('https://a.com/x', { 'https://a.com/x': () => (++n === 1 ? 503 : 404) });
  assert.deepEqual([r.bucket, r.detail, n], ['broken', '404', 2]);
});

test('retry delays double', async () => {
  const sleeps = [];
  const fetchFn = stub({ 'https://a.com/x': 500 });
  await classify('https://a.com/x', { ...settings, retries: 3, retryDelayMs: 100 }, { fetchFn, sleepFn: async (ms) => { sleeps.push(ms); } });
  assert.deepEqual(sleeps, [100, 200, 400]);
});

test('a redirect loop reports the cause message', async () => {
  const loop = Object.assign(new TypeError('fetch failed'), { cause: new Error('redirect count exceeded') });
  const r = await run('https://a.com/x', { 'https://a.com/x': loop });
  assert.deepEqual([r.bucket, r.detail], ['broken', 'redirect count exceeded']);
});

test('a listed host is blocked on 403 without asking the archive, but a 404 there is still broken', async () => {
  const fetchFn = stub({ 'https://blocked.test-host.com/a': 403, 'https://blocked.test-host.com/b': 404 });
  assert.equal((await classify('https://blocked.test-host.com/a', settings, { fetchFn, sleepFn: noSleep, now: NOW })).bucket, 'blocked');
  assert.equal((await classify('https://blocked.test-host.com/b', settings, { fetchFn, sleepFn: noSleep, now: NOW })).bucket, 'broken');
  assert.equal(fetchFn.calls.some((u) => u.startsWith('https://archive.org/')), false);
});

test('429 is throttled and never retried into a failure', async () => {
  const r = await run('https://a.com/x', { 'https://a.com/x': 429 });
  assert.equal(r.bucket, 'throttled');
});

test('5xx is retried and recovers, or is broken after the retries', async () => {
  let n = 0;
  assert.equal((await run('https://a.com/x', { 'https://a.com/x': () => (++n < 3 ? 503 : 200) })).bucket, 'ok');
  assert.equal(n, 3);
  let m = 0;
  const r = await run('https://a.com/y', { 'https://a.com/y': () => (++m, 502) });
  assert.deepEqual([r.bucket, r.detail, m], ['broken', '502', 3]);
});

test('DNS, TLS and timeout failures are broken after the retries', async () => {
  const dns = Object.assign(new Error('fetch failed'), { cause: { code: 'ENOTFOUND' } });
  const r = await run('https://a.com/x', { 'https://a.com/x': dns });
  assert.deepEqual([r.bucket, r.detail], ['broken', 'ENOTFOUND']);
  const timeout = Object.assign(new Error('t'), { name: 'TimeoutError' });
  assert.equal((await run('https://a.com/x', { 'https://a.com/x': timeout })).detail, 'TimeoutError');
});

test('the request carries the URL and a user agent, nothing else', async () => {
  let init;
  const fetchFn = async (_url, i) => { init = i; return resp(200); };
  await classify('https://a.com/x', settings, { fetchFn, sleepFn: noSleep });
  assert.deepEqual(Object.keys(init.headers), ['user-agent']);
  assert.equal(init.method, undefined);
  assert.equal(init.body, undefined);
});

test('extractLinks: inline, autolink, bare, parenthesised, fragments, code', () => {
  const md = [
    'See [a](https://a.com/one) and <https://b.com/two>.',
    'Bare https://c.com/three, then (https://d.com/four) and https://en.wikipedia.org/wiki/Foo_(bar).',
    '`https://skipped.com/inline` and https://e.com/page#frag',
    '```',
    'https://skipped.com/fenced',
    '```',
    'https://f.com/after',
  ].join('\n');
  assert.deepEqual(extractLinks(md), [
    { url: 'https://a.com/one', line: 1 },
    { url: 'https://b.com/two', line: 1 },
    { url: 'https://c.com/three', line: 2 },
    { url: 'https://d.com/four', line: 2 },
    { url: 'https://en.wikipedia.org/wiki/Foo_(bar)', line: 2 },
    { url: 'https://e.com/page', line: 3 },
    { url: 'https://f.com/after', line: 7 },
  ]);
});

test('example hosts are not checked', () => {
  const hosts = ['localhost', '127.0.0.1', 'rss.example.com', 'example.org', 'kipple', 'my.test', 'nas.local', 'box.lan', 'svc.internal', 'router.home.arpa'];
  for (const h of hosts) assert.ok(isExampleHost(h), h);
  for (const h of ['github.com', 'example.com.evil.io']) assert.ok(!isExampleHost(h), h);
});

test('a seeded dead URL fails the run, a CI-403 page that is live in the archive does not', async () => {
  const files = [{ name: 'docs/a.md', text: 'x\n[dead](https://dead.com/gone) [walled](https://walled.com/page)\n[ok](https://ok.com/)' }];
  const archive = { 'https://walled.com/page': { available: true, status: '200', timestamp: stamp(10) } };
  const fetchFn = stub({ 'https://dead.com/gone': 404, 'https://walled.com/page': 403, 'https://ok.com/': 200 }, archive);
  const out = await checkTexts(files, settings, { fetchFn, sleepFn: noSleep, now: NOW });
  assert.deepEqual(out.counts, { ok: 1, blocked: 1, throttled: 0, broken: 1 });
  const lines = [];
  assert.equal(report(out, (l) => lines.push(l)), 1);
  assert.ok(lines.some((l) => l.startsWith('BROKEN: docs/a.md:2: https://dead.com/gone')));

  const clean = await checkTexts(files.slice(0, 1).map((f) => ({ ...f, text: '[w](https://walled.com/page)' })), settings, { fetchFn, sleepFn: noSleep, now: NOW });
  assert.equal(report(clean, () => {}), 0);
});

test('ignoredUrls skips an exact URL only; a repeated URL is fetched once and reports every place', async () => {
  const s = { ...settings, ignoredUrls: [{ url: 'https://skip.com/x', reason: 'test' }] };
  const files = [
    { name: 'a.md', text: 'https://skip.com/x https://dup.com/y' },
    { name: 'b.md', text: '\nhttps://dup.com/y' },
  ];
  const fetchFn = stub({ 'https://dup.com/y': 200 });
  const out = await checkTexts(files, s, { fetchFn, sleepFn: noSleep, now: NOW });
  assert.equal(fetchFn.calls.length, 1);
  assert.deepEqual(out.results[0].where, ['a.md:1', 'b.md:2']);
});

test('per-host concurrency is capped', async () => {
  let active = 0;
  let peak = 0;
  const fetchFn = async () => {
    active++; peak = Math.max(peak, active);
    await new Promise((r) => setTimeout(r, 5));
    active--;
    return resp(200);
  };
  const text = Array.from({ length: 10 }, (_, i) => `https://one.com/${i}`).join(' ');
  const out = await checkTexts([{ name: 'a.md', text }], { ...settings, workers: 8, perHostConcurrency: 2 }, { fetchFn, sleepFn: noSleep });
  assert.equal(out.counts.ok, 10);
  assert.equal(peak, 2);
});

test('the shipped settings file is valid', () => {
  const s = loadSettings();
  assert.ok(s.perHostConcurrency >= 1 && s.waybackMaxAgeDays > 0);
});

test('extractLinks: a comment opener in a code block or inline code swallows nothing', () => {
  const md = ['```html', '<!-- not a comment', '```', 'https://a.com/one', '`<!--` https://b.com/two', 'https://c.com/three -->'].join('\n');
  assert.deepEqual(urls(md), ['https://a.com/one', 'https://b.com/two', 'https://c.com/three']);
  const real = ['x <!-- https://skip.com/a', 'https://skip.com/b --> https://d.com/after', 'https://e.com/next'].join('\n');
  assert.deepEqual(urls(real), ['https://d.com/after', 'https://e.com/next']);
});

test('extractLinks: a nested item then an outer paragraph keeps the outer item content column', () => {
  const md = ['- outer', '  - inner', '', '  outer para', '', '    still outer item https://x.com/y', '', '        https://skip.com/code'].join('\n');
  assert.deepEqual(urls(md), ['https://x.com/y']);
});

test('extractLinks: a fence opened on a list-marker line is code', () => {
  const md = ['1. ```sh', '   https://skip.com/a', '   ```', '- ````', '  https://skip.com/b', '  ````', 'https://a.com/after'].join('\n');
  assert.deepEqual(urls(md), ['https://a.com/after']);
});

test('a redirect loop is broken at once, with no retries and no sleeping', async () => {
  const loop = Object.assign(new TypeError('fetch failed'), { cause: new Error('redirect count exceeded') });
  const fetchFn = stub({ 'https://a.com/x': loop });
  const sleeps = [];
  const r = await classify('https://a.com/x', settings, { fetchFn, sleepFn: async (ms) => { sleeps.push(ms); } });
  assert.deepEqual([r.bucket, fetchFn.calls.length, sleeps.length], ['broken', 1, 0]);
});

test('after the run deadline queued archive lookups give up as unavailable and the run still finishes', async () => {
  let t = 0;
  const archiveCalls = [];
  const fetchFn = async (url) => {
    if (url.startsWith('https://archive.org/')) {
      archiveCalls.push(url);
      t += 1000; // each lookup takes one second of the fake clock
      return resp(200, { archived_snapshots: {} });
    }
    return resp(403);
  };
  const text = Array.from({ length: 6 }, (_, i) => `https://h${i}.com/p`).join(' ');
  const out = await checkTexts([{ name: 'a.md', text }], { ...settings, workers: 6, deadlineMs: 2500 }, { fetchFn, sleepFn: noSleep, now: NOW, clock: () => t });
  assert.equal(archiveCalls.length, 3);
  assert.deepEqual(out.counts, { ok: 0, blocked: 3, throttled: 0, broken: 3 });
  assert.equal(out.results.filter((r) => r.detail === '403, archive unavailable').length, 3);
  assert.equal(report(out, () => {}), 1); // the three genuinely dead ones still fail; none of the skipped ones do
});

test('tracked files are listed NUL-separated, so a name git would quote is still read', () => {
  const dir = mkdtempSync(join(tmpdir(), 'l262-'));
  try {
    const here = (f) => new URL(`./${f}`, import.meta.url);
    mkdirSync(join(dir, 'scripts'));
    for (const f of ['check-links.mjs', 'check-links.json']) writeFileSync(join(dir, 'scripts', f), readFileSync(here(f)));
    writeFileSync(join(dir, 'café notes.md'), 'Only an example link: https://rss.example.com/x\n');
    const git = (...a) => spawnSync('git', a, { cwd: dir, encoding: 'utf8' });
    git('init', '-q');
    git('add', 'café notes.md');
    const r = spawnSync(process.execPath, [join(dir, 'scripts', 'check-links.mjs')], { encoding: 'utf8', cwd: dir });
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /^0 links/m);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('an ignore is exact: a longer URL with the same start is still checked', async () => {
  const s = { ...settings, ignoredUrls: [{ url: 'https://skip.com/x', reason: 'test' }] };
  const fetchFn = stub({ 'https://skip.com/xyz': 200 });
  const out = await checkTexts([{ name: 'a.md', text: 'https://skip.com/x https://skip.com/xyz' }], s, { fetchFn, sleepFn: noSleep });
  assert.deepEqual(fetchFn.calls, ['https://skip.com/xyz']);
  assert.equal(out.counts.ok, 1);
});

test('CHANGELOG compare links to the newest version are skipped (its tag is pushed after the merge), older ones are checked', async () => {
  const log = [
    '## [Unreleased]',
    '## [1.2.3-beta.4] - 2026-01-02',
    '## [1.2.3-beta.3] - 2026-01-01',
    '[Unreleased]: https://github.com/o/r/compare/v1.2.3-beta.4...HEAD',
    '[1.2.3-beta.4]: https://github.com/o/r/compare/v1.2.3-beta.3...v1.2.3-beta.4',
    '[1.2.3-beta.3]: https://github.com/o/r/compare/v1.2.3-beta.2...v1.2.3-beta.3',
  ].join('\n');
  const fetchFn = stub({ 'https://github.com/o/r/compare/v1.2.3-beta.2...v1.2.3-beta.3': 200 });
  const out = await checkTexts([{ name: 'CHANGELOG.md', text: log }], settings, { fetchFn, sleepFn: noSleep });
  assert.deepEqual(fetchFn.calls, ['https://github.com/o/r/compare/v1.2.3-beta.2...v1.2.3-beta.3']);
  assert.equal(out.counts.broken, 0);
  const other = await checkTexts([{ name: 'README.md', text: log }], settings, { fetchFn: stub({}), sleepFn: noSleep });
  assert.equal(other.results.length, 3);
});

const urls = (md) => extractLinks(md).map((l) => l.url);

test('extractLinks: a longer outer fence is not closed by a shorter inner one', () => {
  const md = ['````md', '```', 'https://in.com/a', '```', 'https://in.com/b', '````', 'https://out.com/c'].join('\n');
  assert.deepEqual(urls(md), ['https://out.com/c']);
  const tilde = ['~~~', '```', 'https://in.com/a', '~~~', 'https://out.com/b'].join('\n');
  assert.deepEqual(urls(tilde), ['https://out.com/b']);
  const info = ['```sh', 'https://in.com/a', '```bash', 'https://still.in.com/b', '```', 'https://out.com/c'].join('\n');
  assert.deepEqual(urls(info), ['https://out.com/c']);
});

test('extractLinks: indented code is skipped, list continuations and nested lists are not', () => {
  const md = [
    'Prose https://a.com/prose',
    '',
    '    https://skip.com/indented',
    '    https://skip.com/indented2',
    '',
    'After https://b.com/after',
    '',
    '11. **Step** https://c.com/item',
    '',
    '    Continuation https://d.com/continuation',
    '',
    '        https://skip.com/code-in-list',
    '        --flag https://skip.com/code-in-list2',
    '',
    '    - nested https://e.com/nested',
    '',
    'Back out https://f.com/out',
    '',
    '- bullet',
    '',
    '      https://skip.com/bullet-code',
  ].join('\n');
  assert.deepEqual(urls(md), [
    'https://a.com/prose', 'https://b.com/after', 'https://c.com/item',
    'https://d.com/continuation', 'https://e.com/nested', 'https://f.com/out',
  ]);
});

test('extractLinks: HTML comments, single and multi-line, are skipped', () => {
  const md = ['A <!-- https://skip.com/one --> https://a.com/x', '<!--', 'https://skip.com/two', '-->', 'https://b.com/y'].join('\n');
  assert.deepEqual(extractLinks(md), [{ url: 'https://a.com/x', line: 1 }, { url: 'https://b.com/y', line: 5 }]);
});

test('extractLinks: a table cell does not carry the trailing pipe', () => {
  assert.deepEqual(urls('| name | https://a.com/x |\n|---|---|\n| [l](https://b.com/y)| https://c.com/z|'),
    ['https://a.com/x', 'https://b.com/y', 'https://c.com/z']);
});

test('loadSettings rejects workers, perHostConcurrency and timeoutMs that are not integers of at least 1', () => {
  const good = JSON.parse(readFileSync(new URL('./check-links.json', import.meta.url), 'utf8'));
  const dir = mkdtempSync(join(tmpdir(), 'l262-'));
  try {
    const load = (patch) => {
      const p = join(dir, 'settings.json');
      writeFileSync(p, JSON.stringify({ ...good, ...patch }));
      return loadSettings(p);
    };
    assert.doesNotThrow(() => load({}));
    for (const k of ['workers', 'perHostConcurrency', 'timeoutMs', 'deadlineMs']) {
      for (const bad of [0, -1, 1.5, '2', null]) assert.throws(() => load({ [k]: bad }), new RegExp(`${k} must be an integer`), `${k}=${bad}`);
    }
    assert.throws(() => load({ retries: 1.5 }), /retries/);
    assert.throws(() => load({ ignoredUrls: [{ url: 'https://a.com/', reason: '' }] }), /reason/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('an invalid settings file exits 2 with a clear message', () => {
  const dir = mkdtempSync(join(tmpdir(), 'l262-'));
  try {
    const good = JSON.parse(readFileSync(new URL('./check-links.json', import.meta.url), 'utf8'));
    writeFileSync(join(dir, 'a.md'), 'no links');
    // The checker reads its settings from beside the script, so run a copy of both in a scratch directory.
    writeFileSync(join(dir, 'check-links.json'), JSON.stringify({ ...good, workers: 0 }));
    writeFileSync(join(dir, 'check-links.mjs'), readFileSync(fileURLToPath(new URL('./check-links.mjs', import.meta.url))));
    const r = spawnSync(process.execPath, [join(dir, 'check-links.mjs'), join(dir, 'a.md')], { encoding: 'utf8', cwd: dir });
    assert.equal(r.status, 2);
    assert.match(r.stderr, /workers must be an integer/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
