// node --test scripts/check-links.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { classify, extractLinks, checkTexts, report, loadSettings, isExampleHost } from './check-links.mjs';

const settings = {
  timeoutMs: 1000, retries: 2, retryDelayMs: 0, workers: 4, perHostConcurrency: 2,
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

test('an archive outage means broken, not a crash', async () => {
  const fetchFn = async (url) => {
    if (url.startsWith('https://archive.org/')) throw new Error('down');
    return resp(403);
  };
  assert.equal((await classify('https://a.com/x', settings, { fetchFn, sleepFn: noSleep, now: NOW })).bucket, 'broken');
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
  for (const h of ['localhost', '127.0.0.1', 'rss.example.com', 'example.org', 'kipple', 'my.test']) assert.ok(isExampleHost(h), h);
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

test('ignoredUrls skips by prefix; a repeated URL is fetched once and reports every place', async () => {
  const s = { ...settings, ignoredUrls: [{ prefix: 'https://skip.com/', reason: 'test' }] };
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
