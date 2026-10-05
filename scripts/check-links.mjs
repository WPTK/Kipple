// node scripts/check-links.mjs [file.md ...]     (no files: every tracked *.md)
// node --test scripts/check-links.test.mjs
//
// Finds the http(s) URLs in Markdown prose (not code blocks, inline code or HTML comments) and fails only on links that
// are genuinely dead. Each URL lands in one bucket:
//   ok         2xx, or a redirect chain that ends on 2xx
//   blocked    the page refuses this network but is not shown to be dead: 401/403 or another 4xx (except 404/410) where
//              the Internet Archive has a recent 200 snapshot, or cannot be asked (archive down, throttled, bad answer;
//              printed as "archive unavailable"), or the host is in blockedHosts; also a non-standard status such as 999
//              (printed with its code, never retried: a site that invents a code is refusing bots, not reporting a dead page)
//   throttled  429; never fails
//   broken     404/410 (the archive is never asked), 5xx or 3xx-without-target after the retries, DNS/TLS failure, redirect
//              loop, timeout, or a 4xx refusal that the archive says is not live (no recent 200 snapshot)
// Only `broken` sets a non-zero exit. Retries wait retryDelayMs, then twice that, and so on. Archive lookups run one at a
// time. Settings live in check-links.json next to this file. Nothing but the URL is sent.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath, pathToFileURL } from 'node:url';

const HERE = fileURLToPath(new URL('.', import.meta.url));
const USER_AGENT = 'Mozilla/5.0 (compatible; kipple-link-check)';
const WAYBACK = 'https://archive.org/wayback/available?url=';
const DAY_MS = 86400000;

export function loadSettings(path = `${HERE}check-links.json`) {
  const s = JSON.parse(readFileSync(path, 'utf8'));
  for (const k of ['timeoutMs', 'workers', 'perHostConcurrency']) {
    if (!Number.isInteger(s[k]) || s[k] < 1) throw new Error(`${path}: ${k} must be an integer of at least 1`);
  }
  if (!Number.isInteger(s.retries) || s.retries < 0) throw new Error(`${path}: retries must be an integer of at least 0`);
  for (const k of ['retryDelayMs', 'waybackMaxAgeDays']) {
    if (!Number.isFinite(s[k]) || s[k] < 0) throw new Error(`${path}: ${k} must be a non-negative number`);
  }
  if (!Array.isArray(s.blockedHosts)) throw new Error(`${path}: blockedHosts must be an array`);
  if (!Array.isArray(s.ignoredUrls)) throw new Error(`${path}: ignoredUrls must be an array`);
  for (const i of s.ignoredUrls) {
    if (typeof i?.url !== 'string' || typeof i?.reason !== 'string' || !i.reason) {
      throw new Error(`${path}: every ignoredUrls entry needs an exact url and a reason`);
    }
  }
  return s;
}

// Documentation examples and private networks are not links: reserved example names (RFC 2606), loopback, IP addresses,
// single-label hosts and the private-network suffixes.
export function isExampleHost(hostname) {
  const h = hostname.toLowerCase();
  const ends = (...suffixes) => suffixes.some((s) => h === s.slice(1) || h.endsWith(s));
  return (
    ends('.localhost', '.test', '.invalid', '.example', '.example.com', '.example.org', '.example.net') ||
    ends('.local', '.lan', '.internal', '.home.arpa') ||
    /^\d{1,3}(\.\d{1,3}){3}$/.test(h) || h.startsWith('[') || !h.includes('.')
  );
}

const indentOf = (line) => {
  let w = 0;
  for (const c of line) {
    if (c === ' ') w++;
    else if (c === '\t') w += 4 - (w % 4);
    else break;
  }
  return w;
};

// [{ url, line }] for every http(s) URL in prose, fragment removed. Not extracted: fenced code (a fence closes only on the
// same character, at least as long as it opened, with nothing after it), indented code (four columns past the enclosing
// list item's content, after a blank line), inline code, HTML comments. A "|" ends a URL, so table cells do not leak.
export function extractLinks(text) {
  const out = [];
  const body = text.replace(/<!--[\s\S]*?-->/g, (c) => c.replace(/[^\n]/g, ' '));
  let fence = null; // { ch, len }
  let listIndent = 0;
  let prevBlank = true;
  let inIndented = false;
  body.split(/\r?\n/).forEach((raw, i) => {
    if (fence) {
      const close = /^\s*(`{3,}|~{3,})\s*$/.exec(raw);
      if (close && close[1][0] === fence.ch && close[1].length >= fence.len) fence = null;
      return;
    }
    if (!raw.trim()) { prevBlank = true; return; }
    const open = /^\s*(`{3,}|~{3,})/.exec(raw);
    const indent = indentOf(raw);
    const item = /^\s*(?:[-*+]|\d{1,9}[.)])(\s+)\S/.exec(raw);
    if (inIndented && indent >= listIndent + 4) { prevBlank = false; return; }
    inIndented = false;
    if (prevBlank && indent >= listIndent + 4 && !open) { inIndented = true; prevBlank = false; return; }
    if (item) listIndent = indent + raw.trimStart().indexOf(item[1]) + Math.min(item[1].length, 4);
    else if (prevBlank && indent < listIndent) listIndent = 0;
    prevBlank = false;
    if (open) { fence = { ch: open[1][0], len: open[1].length }; return; }
    const line = raw.replace(/`[^`]*`/g, '');
    for (const hit of line.matchAll(/https?:\/\/[^\s<>"'`\]|]+/g)) {
      let url = hit[0];
      // Trailing punctuation, and a ")" that closes a Markdown link rather than the URL itself.
      for (;;) {
        const before = url;
        url = url.replace(/[.,;:!?*_]+$/, '');
        if (url.endsWith(')') && (url.match(/\)/g) || []).length > (url.match(/\(/g) || []).length) url = url.slice(0, -1);
        if (url === before) break;
      }
      url = url.replace(/#.*$/, '');
      try { new URL(url); } catch { continue; }
      if (url.includes('${') || url.includes('{{')) continue;
      out.push({ url, line: i + 1 });
    }
  });
  return out;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const backoff = (settings, attempt) => settings.retryDelayMs * 2 ** (attempt - 1);

async function probe(url, fetchFn, timeoutMs) {
  try {
    const res = await fetchFn(url, {
      redirect: 'follow',
      headers: { 'user-agent': USER_AGENT },
      signal: AbortSignal.timeout(timeoutMs),
    });
    try { await res.body?.cancel(); } catch { /* the status is all that is needed */ }
    return { status: res.status };
  } catch (err) {
    return { error: err?.cause?.code || err?.cause?.message || err?.name || 'error' };
  }
}

// Archive lookups run one at a time so this checker cannot throttle itself against archive.org.
let archiveQueue = Promise.resolve();
const oneAtATime = (fn) => {
  const run = archiveQueue.then(fn, fn);
  archiveQueue = run.catch(() => {});
  return run;
};

// 'live' (a 200 snapshot no older than maxAgeDays), 'dead' (the archive answered and has none) or 'unknown' (it did not
// answer usefully: down, throttled after the retries, non-2xx, timeout, malformed). Only 'dead' may fail a link.
export function lookupSnapshot(url, { fetchFn, maxAgeDays, timeoutMs, retries = 0, retryDelayMs = 0, sleepFn = sleep, now = Date.now() }) {
  return oneAtATime(async () => {
    for (let attempt = 0; attempt <= retries; attempt++) {
      if (attempt) await sleepFn(retryDelayMs * 2 ** (attempt - 1));
      let res;
      try {
        res = await fetchFn(WAYBACK + encodeURIComponent(url), { signal: AbortSignal.timeout(timeoutMs) });
      } catch {
        return 'unknown';
      }
      if (res.status === 429) continue;
      if (!res.ok) return 'unknown';
      let data;
      try { data = await res.json(); } catch { return 'unknown'; }
      const snaps = data?.archived_snapshots;
      if (!snaps || typeof snaps !== 'object') return 'unknown';
      const snap = snaps.closest;
      const m = /^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$/.exec(snap?.timestamp || '');
      if (!snap?.available || !m || !String(snap.status || '').startsWith('2')) return 'dead';
      const taken = Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]);
      return now - taken <= maxAgeDays * DAY_MS ? 'live' : 'dead';
    }
    return 'unknown';
  });
}

// { bucket, detail } for one URL. fetchFn and sleepFn are injectable so the tests need no network and no waiting.
export async function classify(url, settings, { fetchFn = fetch, sleepFn = sleep, now = Date.now() } = {}) {
  const host = new URL(url).hostname.toLowerCase();
  const blockedHost = settings.blockedHosts.some((h) => host === h || host.endsWith(`.${h}`));
  let last;
  for (let attempt = 0; attempt <= settings.retries; attempt++) {
    if (attempt) await sleepFn(backoff(settings, attempt));
    last = await probe(url, fetchFn, settings.timeoutMs);
    const s = last.status;
    if (s >= 200 && s < 300) return { bucket: 'ok', detail: String(s) };
    if (s === 429) return { bucket: 'throttled', detail: '429' };
    if (s === 404 || s === 410) return { bucket: 'broken', detail: String(s) };
    if (s < 100 || s >= 600) return { bucket: 'blocked', detail: `${s}, non-standard status` };
    if (s >= 400 && s < 500) {
      if (blockedHost) return { bucket: 'blocked', detail: `${s}, host in blockedHosts` };
      const snap = await lookupSnapshot(url, {
        fetchFn, maxAgeDays: settings.waybackMaxAgeDays, timeoutMs: settings.timeoutMs,
        retries: settings.retries, retryDelayMs: settings.retryDelayMs, sleepFn, now,
      });
      if (snap === 'live') return { bucket: 'blocked', detail: `${s}, live in the Internet Archive` };
      if (snap === 'unknown') return { bucket: 'blocked', detail: `${s}, archive unavailable` };
      return { bucket: 'broken', detail: `${s}, no recent Internet Archive snapshot` };
    }
    // 5xx, 3xx without a Location, network error or timeout: worth another try.
  }
  return { bucket: 'broken', detail: last.status ? String(last.status) : last.error };
}

// Runs fn over items with at most `workers` in flight and at most `perHost` per host key.
async function pool(items, hostOf, workers, perHost, fn) {
  const pending = [...items];
  const active = new Map();
  let wake = [];
  const run = async () => {
    while (pending.length) {
      const i = pending.findIndex((it) => (active.get(hostOf(it)) || 0) < perHost);
      if (i < 0) { await new Promise((r) => wake.push(r)); continue; }
      const item = pending.splice(i, 1)[0];
      const h = hostOf(item);
      active.set(h, (active.get(h) || 0) + 1);
      try { await fn(item); } finally {
        active.set(h, active.get(h) - 1);
        const w = wake; wake = [];
        w.forEach((r) => r());
      }
    }
  };
  await Promise.all(Array.from({ length: Math.max(1, workers) }, run));
}

// files: [{ name, text }]. Returns { results: [{ url, bucket, detail, where: ['file:line'] }], counts }.
export async function checkTexts(files, settings, deps = {}) {
  const byUrl = new Map();
  for (const { name, text } of files) {
    for (const { url, line } of extractLinks(text)) {
      if (isExampleHost(new URL(url).hostname)) continue;
      if (settings.ignoredUrls.some((i) => url === i.url)) continue;
      if (!byUrl.has(url)) byUrl.set(url, []);
      byUrl.get(url).push(`${name}:${line}`);
    }
  }
  const results = [];
  await pool([...byUrl], ([u]) => new URL(u).hostname, settings.workers, settings.perHostConcurrency, async ([url, where]) => {
    results.push({ url, where, ...(await classify(url, settings, deps)) });
  });
  results.sort((a, b) => a.where[0].localeCompare(b.where[0], 'en', { numeric: true }));
  const counts = { ok: 0, blocked: 0, throttled: 0, broken: 0 };
  for (const r of results) counts[r.bucket]++;
  return { results, counts };
}

export function report({ results, counts }, log = console.log) {
  for (const b of ['blocked', 'throttled']) {
    for (const r of results.filter((x) => x.bucket === b)) log(`${b}: ${r.where[0]}: ${r.url} (${r.detail})`);
  }
  for (const r of results.filter((x) => x.bucket === 'broken')) {
    log(`BROKEN: ${r.where.join(', ')}: ${r.url} (${r.detail})`);
  }
  log(`${results.length} links: ${counts.ok} ok, ${counts.blocked} blocked, ${counts.throttled} throttled, ${counts.broken} broken`);
  return counts.broken ? 1 : 0;
}

async function main(argv) {
  const root = fileURLToPath(new URL('..', import.meta.url));
  const names = argv.length ? argv : execFileSync('git', ['ls-files', '*.md'], { cwd: root, encoding: 'utf8' }).split('\n').filter(Boolean);
  const files = names.map((name) => ({ name, text: readFileSync(resolve(root, name), 'utf8') }));
  return report(await checkTexts(files, loadSettings()));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2)).then((c) => { process.exitCode = c; }, (e) => { console.error(e.message); process.exitCode = 2; });
}
