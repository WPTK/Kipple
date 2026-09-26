import type { QueryClient } from "@tanstack/react-query";
import { api, ApiError, authStore, buildPath } from "@/api/client";
import { itemsParams, keys, PAGE_SIZE } from "@/api/queries";
import type { MarkReadResponse } from "@/api/types";
import { setOnline, setPending, setUpdateReady } from "./offlineState";

/**
 * Offline support (docs/ui-decisions.md, answer 10): read what is already on the device, keep working, and
 * send the changes when the network is back. Two halves: the service worker (web/sw/sw.js) keeps the app and
 * the last good API answers, and this file keeps the changes made in the meantime.
 *
 * Only two kinds of change are queued, both idempotent on the server: star or unstar one article (sent with
 * `at`, the time it happened) and mark articles read or unread by id. Everything else (subscribing,
 * settings, filters, bulk scope marks) needs the server and fails with its usual error.
 */
export type Queued =
  | { kind: "star"; id: string; starred: boolean; at: number }
  | { kind: "read"; ids: string[]; read: boolean; at: number };

type Row = Queued & { seq: number };

const DB_NAME = "kipple-offline";
const STORE = "queue";

/** Where queued changes live: IndexedDB, or memory when it is unavailable (private windows, tests). */
interface Backend {
  all(): Promise<Row[]>;
  put(row: Row): Promise<void>;
  del(seq: number): Promise<void>;
  clear(): Promise<void>;
}

function memoryBackend(): Backend {
  let rows: Row[] = [];
  return {
    all: async () => [...rows],
    put: async (r) => void (rows = [...rows.filter((x) => x.seq !== r.seq), r].sort((a, b) => a.seq - b.seq)),
    del: async (seq) => void (rows = rows.filter((x) => x.seq !== seq)),
    clear: async () => void (rows = []),
  };
}

function idbBackend(): Backend {
  const open = () =>
    new Promise<IDBDatabase>((resolve, reject) => {
      const req = indexedDB.open(DB_NAME, 1);
      req.onupgradeneeded = () => req.result.createObjectStore(STORE, { keyPath: "seq" });
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  const run = async <T>(mode: IDBTransactionMode, f: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> => {
    const db = await open();
    try {
      return await new Promise<T>((resolve, reject) => {
        const req = f(db.transaction(STORE, mode).objectStore(STORE));
        req.onsuccess = () => resolve(req.result);
        req.onerror = () => reject(req.error);
      });
    } finally {
      db.close();
    }
  };
  return {
    all: async () => ((await run("readonly", (s) => s.getAll())) as Row[]).sort((a, b) => a.seq - b.seq),
    put: async (r) => void (await run("readwrite", (s) => s.put(r))),
    del: async (seq) => void (await run("readwrite", (s) => s.delete(seq))),
    clear: async () => void (await run("readwrite", (s) => s.clear())),
  };
}

let backend: Backend | undefined;
function store(): Backend {
  if (!backend) {
    try {
      backend = typeof indexedDB === "undefined" ? memoryBackend() : idbBackend();
    } catch {
      backend = memoryBackend();
    }
  }
  return backend;
}

/** Tests: start from an empty in-memory queue. */
export function resetOfflineForTests(): void {
  backend = memoryBackend();
  seq = Date.now();
  setPending(0);
}

async function safe<T>(f: () => Promise<T>, fallback: T): Promise<T> {
  try {
    return await f();
  } catch {
    // IndexedDB refused (quota, private window): drop to memory for the rest of the session.
    backend = memoryBackend();
    return fallback;
  }
}

let seq = Date.now();

/** True for the failure of a request that never reached the server. */
export function isOffline(e: unknown): boolean {
  return e instanceof ApiError && e.status === 0;
}

async function refreshCount(): Promise<void> {
  setPending((await safe(() => store().all(), [])).length);
}

/** Queue a star change. A later change to the same article replaces an earlier queued one. */
export async function queueStar(id: string, starred: boolean, at = Math.floor(Date.now() / 1000)): Promise<void> {
  const rows = await safe(() => store().all(), []);
  for (const r of rows) if (r.kind === "star" && r.id === id) await safe(() => store().del(r.seq), undefined);
  await safe(() => store().put({ kind: "star", id, starred, at, seq: ++seq }), undefined);
  await refreshCount();
}

/** Queue a read or unread mark for ids; answers the way the server would for a plain by-id mark. */
export async function queueRead(ids: string[], read: boolean, at = Math.floor(Date.now() / 1000)): Promise<MarkReadResponse> {
  await safe(() => store().put({ kind: "read", ids, read, at, seq: ++seq }), undefined);
  await refreshCount();
  return { changed: ids, restored: [] };
}

let flushing: Promise<void> | undefined;

/**
 * Send the queued changes in the order they were made. Stops, keeping the rest, at the first request that
 * does not reach the server or is refused with 401, 429 or a 5xx; a change the server rejects for good (the
 * article is gone, a bad request) is dropped, since retrying cannot help. Concurrent calls share one run.
 */
export function flushQueue(qc?: QueryClient): Promise<void> {
  if (!flushing) {
    flushing = doFlush(qc).finally(() => {
      flushing = undefined;
    });
  }
  return flushing;
}

async function doFlush(qc?: QueryClient): Promise<void> {
  if (authStore.get() === "out") return;
  const rows = await safe(() => store().all(), []);
  let sent = 0;
  for (const r of rows) {
    try {
      if (r.kind === "star") {
        await api(`/api/items/${r.id}/star`, { method: "PUT", body: { starred: r.starred, at: r.at } });
      } else {
        await api("/api/items/mark-read", { method: "POST", body: { ids: r.ids, read: r.read, reason: "key" } });
      }
    } catch (e) {
      if (isOffline(e) || (e instanceof ApiError && (e.status === 401 || e.status === 429 || e.status >= 500))) break;
      // Any other refusal is final: fall through and drop the change.
    }
    await safe(() => store().del(r.seq), undefined);
    sent++;
  }
  await refreshCount();
  if (sent > 0 && qc) void qc.invalidateQueries({ queryKey: keys.bootstrap });
}

/** Forget everything queued (sign-out: the changes belong to the session that made them). */
export async function clearQueue(): Promise<void> {
  await safe(() => store().clear(), undefined);
  setPending(0);
}

// ---- Prefetch for offline reading ------------------------------------------------------------

const PREFETCH_EVERY_MS = 15 * 60_000;
let lastPrefetch = 0;

/**
 * Fetch the first page of Unread with full content, so the service worker keeps every article for offline
 * reading (it stores the page and each item separately). Once per 15 minutes at most, only while online, and
 * never with the browser's data-saver on. The answer is discarded here: the worker's copy is the point.
 */
export async function prefetchUnread(now = Date.now()): Promise<void> {
  const nav = navigator as Navigator & { connection?: { saveData?: boolean } };
  if (!navigator.onLine || nav.connection?.saveData || now - lastPrefetch < PREFETCH_EVERY_MS) return;
  lastPrefetch = now;
  const params = itemsParams({ view: "unread" }, undefined, PAGE_SIZE);
  try {
    await api(buildPath("/api/items", params) + "&include=content", { quiet: true });
  } catch {
    lastPrefetch = 0; // try again next time
  }
}

/** Tests: allow an immediate prefetch. */
export function resetPrefetchForTests(): void {
  lastPrefetch = 0;
}

// ---- Wiring ----------------------------------------------------------------------------------

/**
 * Register the service worker (production builds only) and wire the online/offline events, the queue flush and
 * the update check. Called once from main.tsx. Returns a cleanup for tests.
 */
export function initOffline(qc: QueryClient): () => void {
  const cleanups: Array<() => void> = [];
  const on = <K extends keyof WindowEventMap>(t: K, f: () => void) => {
    window.addEventListener(t, f);
    cleanups.push(() => window.removeEventListener(t, f));
  };
  on("online", () => {
    setOnline(true);
    void flushQueue(qc);
  });
  on("offline", () => setOnline(false));
  const onVisible = () => {
    if (document.visibilityState !== "visible") return;
    void flushQueue(qc);
    if (authStore.get() === "in") void prefetchUnread();
    void navigator.serviceWorker
      ?.getRegistration()
      .then((r) => r?.update())
      .catch(() => {});
  };
  document.addEventListener("visibilitychange", onVisible);
  cleanups.push(() => document.removeEventListener("visibilitychange", onVisible));
  // A queue left by an earlier session is sent at launch; while changes wait, try again now and then.
  void refreshCount().then(() => flushQueue(qc));
  const timer = window.setInterval(() => void flushQueue(qc), 60_000);
  cleanups.push(() => window.clearInterval(timer));

  // Sign-out ends the session's offline data: the worker's copies of the API answers and the queue.
  const unsubAuth = authStore.subscribe(() => {
    if (authStore.get() !== "out") return;
    void clearQueue();
    navigator.serviceWorker?.controller?.postMessage({ type: "clear-data" });
  });
  cleanups.push(unsubAuth);

  if (import.meta.env.PROD && "serviceWorker" in navigator) {
    const had = !!navigator.serviceWorker.controller;
    // The new worker skips waiting and takes over: a controller change on a page that already had one
    // means new code is ready. Reloading is the user's call, so a banner offers it.
    const onChange = () => {
      if (had) setUpdateReady();
    };
    navigator.serviceWorker.addEventListener("controllerchange", onChange);
    cleanups.push(() => navigator.serviceWorker.removeEventListener("controllerchange", onChange));
    void navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => {});
  }
  return () => cleanups.forEach((f) => f());
}
