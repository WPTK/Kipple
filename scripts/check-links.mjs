// node scripts/check-links.mjs [file.md ...]     (no files: every tracked *.md)
// node --test scripts/check-links.test.mjs
//
// Finds the http(s) URLs in Markdown and fails only on links that are genuinely dead. Each URL lands in one bucket:
//   ok         2xx, or a redirect chain that ends on 2xx
//   blocked    401/403/other 4xx where the Internet Archive has a recent 200 snapshot (the page is live, this network is
//              refused), or the host is listed in blockedHosts
//   throttled  429; never fails
//   broken     404/410, 5xx after the retries, DNS/TLS failure, timeout, or a refusal with no recent snapshot
// Only `broken` sets a non-zero exit. Settings live in check-links.json next to this file. Nothing but the URL is sent.
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
  for (const k of ['timeoutMs', 'retries', 'retryDelayMs', 'workers', 'perHostConcurrency', 'waybackMaxAgeDays']) {
    if (!Number.isFinite(s[k]) || s[k] < 0) throw new Error(`${path}: ${k} must be a non-negative number`);
  }
  for (const k of ['blockedHosts', 'ignoredUrls']) {
    if (!Array.isArray(s[k])) throw new Error(`${path}: ${k} must be an array`);
  }
  for (const i of s.ignoredUrls) {
    if (typeof i?.prefix !== 'string' || typeof i?.reason !== 'string' || !i.reason) {
      throw new Error(`${path}: every ignoredUrls entry needs a prefix and a reason`);
    }
  }
  return s;
}

// Documentation examples are not links: reserved example names (RFC 2606), loopback, IP addresses, single-label hosts.
export function isExampleHost(hostname) {
  const h = hostname.toLowerCase();
  return (
    h === 'localhost' || h.endsWith('.localhost') || h.endsWith('.test') || h.endsWith('.invalid') ||
    h.endsWith('.example') || h === 'example.com' || h.endsWith('.example.com') || h === 'example.org' ||
    h.endsWith('.example.org') || h === 'example.net' || h.endsWith('.example.net') ||
    /^\d{1,3}(\.\d{1,3}){3}$/.test(h) || h.startsWith('[') || !h.includes('.')
  );
}

// [{ url, line }] for every http(s) URL outside fenced code blocks and inline code, fragment removed.
export function extractLinks(text) {
  const out = [];
  let fence = null;
  text.split(/\r?\n/).forEach((raw, i) => {
    const m = /^\s*(`{3,}|~{3,})/.exec(raw);
    if (m) {
      if (!fence) fence = m[1][0];
      else if (m[1][0] === fence) fence = null;
      return;
    }
    if (fence) return;
    const line = raw.replace(/`[^`]*`/g, '');
    for (const hit of line.matchAll(/https?:\/\/[^\s<>"'`\]]+/g)) {
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
    return { error: err?.cause?.code || err?.name || 'error' };
  }
}

// True when the Internet Archive holds a 200 snapshot no older than maxAgeDays. Any failure of the archive is "no".
export async function hasRecentSnapshot(url, { fetchFn, maxAgeDays, timeoutMs, now = Date.now() }) {
  try {
    const res = await fetchFn(WAYBACK + encodeURIComponent(url), { signal: AbortSignal.timeout(timeoutMs) });
    if (!res.ok) return false;
    const snap = (await res.json())?.archived_snapshots?.closest;
    const m = /^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})$/.exec(snap?.timestamp || '');
    if (!snap?.available || !m || !String(snap.status || '').startsWith('2')) return false;
    const taken = Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]);
    return now - taken <= maxAgeDays * DAY_MS;
  } catch {
    return false;
  }
}

// { bucket, detail } for one URL. fetchFn and sleepFn are injectable so the tests need no network and no waiting.
export async function classify(url, settings, { fetchFn = fetch, sleepFn = sleep, now = Date.now() } = {}) {
  const host = new URL(url).hostname.toLowerCase();
  const blockedHost = settings.blockedHosts.some((h) => host === h || host.endsWith(`.${h}`));
  let last;
  for (let attempt = 0; attempt <= settings.retries; attempt++) {
    if (attempt) await sleepFn(settings.retryDelayMs * attempt);
    last = await probe(url, fetchFn, settings.timeoutMs);
    const s = last.status;
    if (s && s >= 200 && s < 300) return { bucket: 'ok', detail: String(s) };
    if (s === 429) return { bucket: 'throttled', detail: '429' };
    if (s === 404 || s === 410) return { bucket: 'broken', detail: String(s) };
    if (s && s >= 400 && s < 500) {
      if (blockedHost) return { bucket: 'blocked', detail: `${s}, host in blockedHosts` };
      const live = await hasRecentSnapshot(url, { fetchFn, maxAgeDays: settings.waybackMaxAgeDays, timeoutMs: settings.timeoutMs, now });
      return live
        ? { bucket: 'blocked', detail: `${s}, live in the Internet Archive` }
        : { bucket: 'broken', detail: `${s}, no recent Internet Archive snapshot` };
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
      if (settings.ignoredUrls.some((i) => url.startsWith(i.prefix))) continue;
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
