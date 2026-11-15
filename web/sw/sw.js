/*
 * Kipple service worker. web/vite.config.ts (the kipple-sw plugin) fills the two markers below with the
 * build id and the list of files to precache, and writes the result to dist/sw.js. Plain JavaScript,
 * served as it is. Tested by src/sw.test.ts, which runs this file against a fake worker scope.
 *
 * What it does, and no more:
 *  - precaches the app shell (index, hashed JS and CSS, icons) so a launch works offline;
 *  - navigations: network first, the cached shell when the network is down or slow;
 *  - /assets/*: cache first (hashed names never change);
 *  - a few read-only API GETs (bootstrap, item lists, one item, the saved searches): network first, the last good answer when
 *    the network is down or slow, marked X-Kipple-Cache: 1. A list fetched with include=content is also
 *    stored as one entry per item, so each article opens offline;
 *  - images (/img/*, feed icons): cache first, bounded;
 *  - every other request, and every write, goes straight to the network. Writes never reach this worker at all
 *    (a static route, below): a stats beacon sent as the page unloads is lost if the browser has to wake the worker
 *    to be told "not mine".
 * Sign-out sends {type:"clear-data"} and everything read from the API is dropped. Each clear starts a new
 * generation: a copy that a request from before it was still going to store (an answer landing late, an image
 * still downloading) is dropped instead of filling the caches again with the signed-out session's data.
 */
"use strict";

const BUILD = /*BUILD*/ "dev";
const PRECACHE = /*PRECACHE*/ [];

const SHELL_PREFIX = "kipple-shell-";
const SHELL = SHELL_PREFIX + BUILD;
const DATA = "kipple-data";
// The images cache was renamed once so that originals kept under thumbnail addresses by earlier workers are dropped
// (activate deletes the old name).
const IMAGES = "kipple-images-2";
const OLD_IMAGES = "kipple-images";
// An image answer is kept only when the server meant it to last at least this long (a feed icon's week passes).
const IMAGE_KEEP_MIN_S = 86400;
const STATIC = "kipple-static";
const DATA_MAX = 600;
const IMAGES_MAX = 400;
const STATIC_MAX = 200;
const NET_WAIT_MS = 5000;

const API_READS = /^\/api\/(bootstrap|items|items\/\d+|saved-searches)$/;
const ICONS = /^\/api\/feeds\/\d+\/icon$/;

/** Bumped by every clear-data; a copy whose request started in an older generation is not stored. */
let generation = 0;

self.addEventListener("install", (event) => {
  event.waitUntil(Promise.all([routeWritesToNetwork(event), precache()]).then(() => self.skipWaiting()));
});

/**
 * Every request from a page this worker controls is first dispatched to the worker, even one it will not answer. A
 * keepalive request made as the page unloads (the stats beacon on pagehide) is cancelled in Chromium before the
 * dispatch completes, so a reload or a typed address recorded nothing. A static route lets the browser decide without
 * waking the worker: a write goes to the network. Browsers without the static routing API (and without the loss) skip it;
 * a failure here costs only that, never the install.
 */
async function routeWritesToNetwork(event) {
  if (typeof event.addRoutes !== "function") return;
  try {
    await event.addRoutes({ condition: { not: { requestMethod: "GET" } }, source: "network" });
  } catch {
    /* the worker still answers every request itself, as before */
  }
}

async function precache() {
  const cache = await caches.open(SHELL);
  // All or nothing: a half-filled shell would make an offline launch fail in a confusing way, and a
  // failed install keeps the previous worker running.
  try {
    await Promise.all(
      PRECACHE.map(async (u) => {
        const res = await fetch(u, { cache: "reload" });
        if (!res.ok) throw new Error(u + " " + res.status);
        await cache.put(u, res);
      }),
    );
  } catch (e) {
    // Never leave a half-filled shell behind: offline navigations would prefer it to a complete older one.
    await caches.delete(SHELL);
    throw e;
  }
}

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      // Keep this build's shell and the one before it: a page from the previous build may still ask for its
      // lazy chunks until it reloads.
      const others = (await caches.keys()).filter((k) => k.startsWith(SHELL_PREFIX) && k !== SHELL).sort();
      const keep = new Set([SHELL, others[others.length - 1]]); // this build's shell always stays
      await Promise.all([...others.filter((k) => !keep.has(k)), OLD_IMAGES].map((k) => caches.delete(k)));
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("message", (event) => {
  // The page's "Reload" button asks a waiting worker to take over before it reloads.
  if (event.data && event.data.type === "skip-waiting") self.skipWaiting();
  if (event.data && event.data.type === "clear-data") {
    generation++;
    event.waitUntil(Promise.all([caches.delete(DATA), caches.delete(IMAGES), caches.delete(OLD_IMAGES)]));
  }
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  const p = url.pathname;
  if (req.mode === "navigate") {
    if (isAppPath(p)) event.respondWith(navigation(event, req));
    return;
  }
  if (p.startsWith("/assets/")) {
    event.respondWith(assetFirst(event, req));
  } else if (p.startsWith("/img/") || ICONS.test(p)) {
    event.respondWith(imageFirst(event, req));
  } else if (API_READS.test(p)) {
    event.respondWith(dataFirst(event, req, url));
  } else if (PRECACHE.includes(p)) {
    event.respondWith(rootFile(req));
  }
});

function isAppPath(p) {
  return !(p.startsWith("/api/") || p.startsWith("/img/") || p === "/healthz" || p.startsWith("/_status"));
}

function withTimeout(promise, ms) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("timeout")), ms);
    promise.then(
      (v) => {
        clearTimeout(t);
        resolve(v);
      },
      (e) => {
        clearTimeout(t);
        reject(e);
      },
    );
  });
}

async function shellIndex() {
  // This build's own shell first, then the newest of the others.
  const others = (await caches.keys()).filter((k) => k.startsWith(SHELL_PREFIX) && k !== SHELL).sort().reverse();
  for (const k of [SHELL, ...others]) {
    const hit = await (await caches.open(k)).match("/");
    if (hit) return hit;
  }
  return undefined;
}

// Must match SIGN_IN_RELOAD in src/lib/reload.ts.
const SIGN_IN_RELOAD = "kipple-signin";

// The page's ETag is the hash of index.html, a dot, and the image mode its policy was written for (internal/web
// pageETag). The server makes it weak (W/) when it compresses the answer and leaves it strong when it does not,
// so the prefix is no part of the comparison. null for any other tag (a proxy's login page, a tag a proxy rewrote).
function pageTag(res) {
  const tag = (res.headers.get("ETag") || "").replace(/^W\//, "");
  const i = tag.lastIndexOf(".");
  return i > 0 ? { file: tag.slice(0, i), mode: tag.slice(i + 1) } : null;
}

// Navigations are numbered as they start. The shell policy is brought up to date from the newest navigation's answer
// that was applied: a slow answer from an earlier navigation must not put back an older policy over a newer one.
let navSeq = 0;
let shellSeq = 0;
// One update at a time, so each reads the shell the one before it left.
let shellWork = Promise.resolve();

// The stored shell carries the Content-Security-Policy it was fetched with, and the page policy follows the
// image mode. When the server answers a navigation with this build's index.html under another mode, the stored
// copy is replaced by that answer, so a launch from the shell runs under the policy the server sends now.
// Any other page (another build's index, a proxy's login page) leaves the shell alone.
function keepShellPolicy(res, seq) {
  const run = shellWork.then(() => applyShellPolicy(res, seq));
  shellWork = run.catch(() => {});
  return run;
}

async function applyShellPolicy(res, seq) {
  // Never open the shell by name just to look: that would create an empty cache where precache left none.
  if (!(await caches.keys()).includes(SHELL)) return release(res);
  const cache = await caches.open(SHELL);
  const held = await cache.match("/");
  const was = held && pageTag(held);
  const now = pageTag(res);
  if (!was || !now || was.file !== now.file || seq < shellSeq) return release(res);
  shellSeq = seq;
  if (was.mode === now.mode) return release(res);
  await cache.put("/", res);
}

// Not awaited: a branch of a cloned body settles its cancel only once the page has also read or dropped its own.
function release(res) {
  if (res.body) res.body.cancel().catch(() => {});
}

async function navigation(event, req) {
  // "Reload to sign in again": the stored shell would only load the app into the expired sign-in again, so this
  // navigation waits for the network however slow it is (and so reaches the access proxy's login page). Only a
  // network that is really down still gets the shell.
  if (new URL(req.url).searchParams.has(SIGN_IN_RELOAD)) {
    try {
      return await fetch(req);
    } catch {
      return (await shellIndex()) || Response.error();
    }
  }
  const seq = ++navSeq;
  const network = fetch(req);
  let res;
  try {
    res = await withTimeout(network, NET_WAIT_MS);
  } catch {
    // Slow rather than down: an answer that still lands brings the stored shell's policy up to date for next time,
    // and any other answer is dropped so its connection is released.
    event.waitUntil(network.then((r) => (r.status === 200 && pageTag(r) ? keepShellPolicy(r, seq) : release(r))).catch(() => {}));
    return (await shellIndex()) || Response.error();
  }
  if (res.status === 200 && pageTag(res)) event.waitUntil(keepShellPolicy(res.clone(), seq).catch(() => {}));
  if (res.status < 500) return res;
  return (await shellIndex()) || res;
}

async function assetFirst(event, req) {
  const hit = await caches.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) {
    const copy = res.clone();
    // Fonts and lazy chunks that the precache leaves out: kept once seen, so the next offline launch has them.
    event.waitUntil(
      caches
        .open(STATIC)
        .then(async (c) => {
          await c.put(req, copy);
          await trim(c, STATIC_MAX);
        })
        .catch(() => {}),
    );
  }
  return res;
}

async function imageFirst(event, req) {
  const gen = generation;
  const cache = await caches.open(IMAGES);
  const hit = await cache.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  // 200 only (a 206 partial cannot be stored), and only an answer the server meant to last: the proxy sends the
  // original at a thumbnail URL as "no-cache" while the thumbnail is being made, or for as long as a refusal is
  // remembered ("max-age" of the time left), and a copy kept here would be served in the thumbnail's place for good.
  // Never in the way of the answer: a full or failing cache must not break an image that loaded fine.
  if (res.status === 200 && lastsLong(res.headers.get("Cache-Control") || "")) {
    const copy = res.clone();
    if (gen === generation) event.waitUntil(cache.put(req, copy).then(() => trim(cache, IMAGES_MAX)).catch(() => {}));
  }
  return res;
}

// True for "immutable" or a max-age of at least IMAGE_KEEP_MIN_S; false for no-cache, no-store, a short max-age and
// an answer that names no lifetime.
function lastsLong(cc) {
  if (/\bno-(cache|store)\b/i.test(cc)) return false;
  if (/\bimmutable\b/i.test(cc)) return true;
  const m = /\bmax-age=(\d+)/i.exec(cc);
  return m !== null && Number(m[1]) >= IMAGE_KEEP_MIN_S;
}

// The manifest and icons have fixed names, so a cached copy could be a build old: network first.
async function rootFile(req) {
  try {
    return await fetch(req);
  } catch (e) {
    const hit = await caches.match(req);
    if (hit) return hit;
    throw e;
  }
}

function keyOf(url) {
  return url.pathname + url.search;
}

async function dataFirst(event, req, url) {
  const gen = generation;
  const cache = await caches.open(DATA);
  const fallback = async () => {
    const hit = await cache.match(keyOf(url));
    return hit ? marked(hit) : undefined;
  };
  // A search being typed is not worth a slot: it would push saved articles out of the cache.
  const keep = !url.searchParams.has("typing");
  const network = fetch(req);
  let res;
  try {
    res = await withTimeout(network, NET_WAIT_MS);
  } catch (e) {
    const hit = await fallback();
    if (hit) {
      // Slow rather than down: let the answer still arrive and refresh the copy for next time.
      if (keep) event.waitUntil(network.then((r) => (r.ok ? storeData(cache, url, r.clone(), gen) : undefined)).catch(() => {}));
      return hit;
    }
    // Nothing kept: wait for the real answer however long it takes; only a genuine failure is an error.
    if (e && e.message === "timeout") res = await network;
    else throw e;
  }
  if (res.ok) {
    if (keep) event.waitUntil(storeData(cache, url, res.clone(), gen).catch(() => {}));
    return res;
  }
  // A 5xx (the origin is down behind a proxy) is treated like no network; a 4xx, 401 above all, is the answer.
  if (res.status >= 500) return (await fallback()) || res;
  return res;
}

function marked(res) {
  const headers = new Headers(res.headers);
  headers.set("X-Kipple-Cache", "1");
  return new Response(res.body, { status: res.status, statusText: res.statusText, headers });
}

function jsonResponse(v) {
  return new Response(JSON.stringify(v), { headers: { "Content-Type": "application/json" } });
}

// `gen` is the generation the request started in. Every put checks it, since a clear can land between two puts.
async function storeData(cache, url, res, gen) {
  const stale = () => gen !== generation;
  if (url.pathname === "/api/items" && url.searchParams.get("include") === "content") {
    let body;
    try {
      body = JSON.parse(await res.text());
    } catch {
      return;
    }
    for (const it of (body && body.items) || []) {
      if (stale()) return;
      if (it && it.id && typeof it.content_html === "string") await cache.put("/api/items/" + it.id, jsonResponse(it));
    }
    // The list is stored where the app asks for it (same query, no include), so it opens offline too.
    const plain = new URL(url.href);
    plain.searchParams.delete("include");
    if (stale()) return;
    await cache.put(keyOf(plain), jsonResponse(body));
  } else {
    if (stale()) return;
    await cache.put(keyOf(url), res);
  }
  if (stale()) return;
  await trim(cache, DATA_MAX);
}

// Oldest first: Cache.keys() lists in insertion order.
async function trim(cache, max) {
  const keys = await cache.keys();
  await Promise.all(keys.slice(0, Math.max(0, keys.length - max)).map((k) => cache.delete(k)));
}
