/*
 * Kipple service worker. web/vite.config.ts (the kipple-sw plugin) fills the two markers below with the
 * build id and the list of files to precache, and writes the result to dist/sw.js. Plain JavaScript,
 * served as it is. Tested by src/sw.test.ts, which runs this file against a fake worker scope.
 *
 * What it does, and no more:
 *  - precaches the app shell (index, hashed JS and CSS, icons) so a launch works offline;
 *  - navigations: network first, the cached shell when the network is down or slow;
 *  - /assets/*: cache first (hashed names never change);
 *  - a few read-only API GETs (bootstrap, item lists, one item): network first, the last good answer when
 *    the network is down or slow, marked X-Kipple-Cache: 1. A list fetched with include=content is also
 *    stored as one entry per item, so each article opens offline;
 *  - images (/img/*, feed icons): cache first, bounded;
 *  - every other request, and every write, goes straight to the network.
 * Sign-out sends {type:"clear-data"} and everything read from the API is dropped.
 */
"use strict";

const BUILD = /*BUILD*/ "dev";
const PRECACHE = /*PRECACHE*/ [];

const SHELL_PREFIX = "kipple-shell-";
const SHELL = SHELL_PREFIX + BUILD;
const DATA = "kipple-data";
const IMAGES = "kipple-images";
const STATIC = "kipple-static";
const DATA_MAX = 600;
const IMAGES_MAX = 400;
const NET_WAIT_MS = 5000;

const API_READS = /^\/api\/(bootstrap|items|items\/\d+)$/;
const ICONS = /^\/api\/feeds\/\d+\/icon$/;

self.addEventListener("install", (event) => {
  event.waitUntil(precache().then(() => self.skipWaiting()));
});

async function precache() {
  const cache = await caches.open(SHELL);
  // All or nothing: a half-filled shell would make an offline launch fail in a confusing way, and a
  // failed install keeps the previous worker running.
  await Promise.all(
    PRECACHE.map(async (u) => {
      const res = await fetch(u, { cache: "reload" });
      if (!res.ok) throw new Error(u + " " + res.status);
      await cache.put(u, res);
    }),
  );
}

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      // Keep this build's shell and the one before it: a page from the previous build may still ask for its
      // lazy chunks until it reloads.
      const shells = (await caches.keys()).filter((k) => k.startsWith(SHELL_PREFIX)).sort();
      await Promise.all(shells.slice(0, -2).map((k) => caches.delete(k)));
      await self.clients.claim();
    })(),
  );
});

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "clear-data") {
    event.waitUntil(Promise.all([caches.delete(DATA), caches.delete(IMAGES)]));
  }
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  const p = url.pathname;
  if (req.mode === "navigate") {
    if (isAppPath(p)) event.respondWith(navigation(req));
    return;
  }
  if (p.startsWith("/assets/")) {
    event.respondWith(assetFirst(event, req));
  } else if (p.startsWith("/img/") || ICONS.test(p)) {
    event.respondWith(imageFirst(req));
  } else if (API_READS.test(p)) {
    event.respondWith(dataFirst(event, req, url));
  } else if (PRECACHE.includes(p)) {
    event.respondWith(assetFirst(event, req));
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
  const shells = (await caches.keys()).filter((k) => k.startsWith(SHELL_PREFIX)).sort().reverse();
  for (const k of shells) {
    const hit = await (await caches.open(k)).match("/");
    if (hit) return hit;
  }
  return undefined;
}

async function navigation(req) {
  try {
    const res = await withTimeout(fetch(req), NET_WAIT_MS);
    if (res.status < 500) return res;
    return (await shellIndex()) || res;
  } catch {
    return (await shellIndex()) || Response.error();
  }
}

async function assetFirst(event, req) {
  const hit = await caches.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) {
    const copy = res.clone();
    // Fonts and lazy chunks that the precache leaves out: kept once seen, so the next offline launch has them.
    event.waitUntil(caches.open(STATIC).then((c) => c.put(req, copy)).catch(() => {}));
  }
  return res;
}

async function imageFirst(req) {
  const cache = await caches.open(IMAGES);
  const hit = await cache.match(req);
  if (hit) return hit;
  const res = await fetch(req);
  if (res.ok) {
    await cache.put(req, res.clone());
    await trim(cache, IMAGES_MAX);
  }
  return res;
}

function keyOf(url) {
  return url.pathname + url.search;
}

async function dataFirst(event, req, url) {
  const cache = await caches.open(DATA);
  const fallback = async () => {
    const hit = await cache.match(keyOf(url));
    return hit ? marked(hit) : undefined;
  };
  let res;
  try {
    res = await withTimeout(fetch(req), NET_WAIT_MS);
  } catch (e) {
    const hit = await fallback();
    if (hit) return hit;
    throw e;
  }
  if (res.ok) {
    event.waitUntil(storeData(cache, url, res.clone()).catch(() => {}));
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

async function storeData(cache, url, res) {
  if (url.pathname === "/api/items" && url.searchParams.get("include") === "content") {
    let body;
    try {
      body = JSON.parse(await res.text());
    } catch {
      return;
    }
    for (const it of (body && body.items) || []) {
      if (it && it.id && typeof it.content_html === "string") await cache.put("/api/items/" + it.id, jsonResponse(it));
    }
    // The list is stored where the app asks for it (same query, no include), so it opens offline too.
    const plain = new URL(url.href);
    plain.searchParams.delete("include");
    await cache.put(keyOf(plain), jsonResponse(body));
  } else {
    await cache.put(keyOf(url), res);
  }
  await trim(cache, DATA_MAX);
}

// Oldest first: Cache.keys() lists in insertion order.
async function trim(cache, max) {
  const keys = await cache.keys();
  await Promise.all(keys.slice(0, Math.max(0, keys.length - max)).map((k) => cache.delete(k)));
}
