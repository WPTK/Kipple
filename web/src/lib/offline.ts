import { onlineManager, type QueryClient } from "@tanstack/react-query";
import { api, ApiError, authStore, buildPath } from "@/api/client";
import { itemsParams, keys, PAGE_SIZE, patchItems } from "@/api/queryKeys";
import { toast } from "@/shell/toasts";
import { devicePrefsStore, resolveOrder, sessionLayoutStore } from "./devicePrefs";
import type { MarkReadResponse } from "@/api/types";
import { offlineStore, setOnline, setPending, setUpdateReady } from "./offlineState";
import { wipeStatsQueue } from "./statsSender";

/**
 * Offline support (docs/ui-decisions.md, answer 10): read what is already on the device, keep working, and
 * send the changes when the network is back. Two halves: the service worker (web/sw/sw.js) keeps the app and
 * the last good API answers, and this file keeps the changes made in the meantime.
 *
 * Only two kinds of change are queued, both idempotent on the server: star or unstar one article (sent with
 * `at`, the time it happened) and mark articles read or unread by id. Everything else (subscribing,
 * settings, filters, bulk scope marks) needs the server and fails with its usual error.
 *
 * Conflicts: a star carries `at` and the server keeps the newer of the two. A read mark carries no time, because
 * the server has nothing to compare it with (read state has no timestamp), so a replayed read or unread simply
 * wins over whatever another device did to the same article while this one was offline. That is the accepted
 * rule: the last change to reach the server wins. What this device does online in the meantime is protected by
 * supersede(), which drops the queued change for those articles before the online write is sent.
 */
export type Queued =
  | { kind: "star"; id: string; starred: boolean; at: number }
  | { kind: "read"; ids: string[]; read: boolean };

type Row = Queued & { seq: number };

const DB_NAME = "kipple-offline";
const STORE = "queue";

/** Where queued changes live: IndexedDB, or memory when it is unavailable (private windows, tests). */
export interface Backend {
  /** Rejects when the store cannot be used at all (private windows, blocked storage). */
  probe?(): Promise<void>;
  all(): Promise<Row[]>;
  put(row: Row): Promise<void>;
  del(seq: number): Promise<void>;
  /**
   * Read-modify-write of one row as a single step: `f` gets the row as stored now and returns its new version, or
   * null to delete it. A row that is no longer there (sent and removed by a flush meanwhile) is left gone: writing
   * it back would queue it again and send it twice.
   */
  update(seq: number, f: (row: Row) => Row | null): Promise<void>;
  clear(): Promise<void>;
}

function memoryBackend(): Backend {
  let rows: Row[] = [];
  return {
    all: async () => [...rows],
    put: async (r) => void (rows = [...rows.filter((x) => x.seq !== r.seq), r].sort((a, b) => a.seq - b.seq)),
    del: async (seq) => void (rows = rows.filter((x) => x.seq !== seq)),
    // Synchronous from the lookup to the write: nothing else can run in between.
    update: async (seq, f) => {
      const cur = rows.find((x) => x.seq === seq);
      if (!cur) return;
      const next = f(cur);
      rows = next ? rows.map((x) => (x.seq === seq ? next : x)) : rows.filter((x) => x.seq !== seq);
    },
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
      // Resolve when the transaction commits, not when the request succeeds: a quota error surfaces at commit.
      return await new Promise<T>((resolve, reject) => {
        const tx = db.transaction(STORE, mode);
        const req = f(tx.objectStore(STORE));
        tx.oncomplete = () => resolve(req.result);
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
    } finally {
      db.close();
    }
  };
  return {
    probe: async () => void (await run("readonly", (s) => s.count())),
    all: async () => ((await run("readonly", (s) => s.getAll())) as Row[]).sort((a, b) => a.seq - b.seq),
    put: async (r) => void (await run("readwrite", (s) => s.put(r))),
    del: async (seq) => void (await run("readwrite", (s) => s.delete(seq))),
    // The get and the put or delete share one readwrite transaction, so a flush cannot delete the row in between.
    update: async (seq, f) =>
      void (await run("readwrite", (s) => {
        const req = s.get(seq);
        req.onsuccess = () => {
          const cur = req.result as Row | undefined;
          if (!cur) return;
          const next = f(cur);
          if (next) s.put(next);
          else s.delete(seq);
        };
        return req;
      })),
    clear: async () => void (await run("readwrite", (s) => s.clear())),
  };
}

/**
 * The backend is chosen once, on first use: IndexedDB when a probe of it works, otherwise memory for the whole
 * session. A later error on one operation never swaps it, which would strand the rows already stored.
 */
let backend: Promise<Backend> | undefined;
function store(): Promise<Backend> {
  backend ??= (async () => {
    if (typeof indexedDB === "undefined") return memoryBackend();
    try {
      const b = idbBackend();
      await b.probe?.();
      return b;
    } catch {
      return memoryBackend();
    }
  })();
  return backend;
}

/** Tests: start from an empty in-memory queue. */
export function resetOfflineForTests(): void {
  backend = Promise.resolve(memoryBackend());
  seq = 0;
  sending = undefined;
  setPending(0);
}

/** Tests: a fresh in-memory store to wrap (for example to stage a race). */
export function memoryBackendForTests(): Backend {
  return memoryBackend();
}

/** Tests: use this storage for the queue (for example one whose writes fail). */
export function setOfflineBackendForTests(b: Backend): void {
  backend = Promise.resolve(b);
}

/**
 * A read or a cleanup that fails is treated as "nothing there"; the queue keeps working. Saving a new change is
 * not wrapped in this: a change that could not be stored must not be reported as queued (see QueueWriteError).
 */
async function safe<T>(f: (b: Backend) => Promise<T>, fallback: T): Promise<T> {
  try {
    return await f(await store());
  } catch {
    return fallback;
  }
}

let seq = 0;
/** Rising keys that also differ between two tabs started in the same millisecond. */
function nextSeq(): number {
  seq = Math.max(seq + 1, Date.now() * 1000 + Math.floor(Math.random() * 1000));
  return seq;
}

/** A change made offline could not be stored on the device, so it will not be sent later. */
export class QueueWriteError extends Error {
  constructor(cause: unknown) {
    super("could not store the change on this device", { cause });
    this.name = "QueueWriteError";
  }
}

async function put(row: Row): Promise<void> {
  try {
    await (await store()).put(row);
  } catch (e) {
    throw new QueueWriteError(e);
  }
}

/** True for the failure of a request that never reached the server. */
export function isOffline(e: unknown): boolean {
  return e instanceof ApiError && e.status === 0;
}

/**
 * A read that never reached the server (not even the service worker had a copy) while the browser itself says it is
 * offline. Asking again a second later cannot help, so queries do not retry it: the screen shows its error (with Try
 * again) at once, and the query runs again when the browser comes back online.
 */
export function failedWhileOffline(e: unknown): boolean {
  return isOffline(e) && typeof navigator !== "undefined" && navigator.onLine === false;
}

async function refreshCount(): Promise<void> {
  setPending((await safe((b) => b.all(), [])).length);
}

/**
 * Queue a star change. A later change to the same article replaces an earlier queued one. Rejects with
 * QueueWriteError when the change could not be stored.
 */
export async function queueStar(id: string, starred: boolean, at = Math.floor(Date.now() / 1000)): Promise<void> {
  // Stored first, so a failed save leaves the earlier queued change in place rather than nothing at all.
  const row: Row = { kind: "star", id, starred, at, seq: nextSeq() };
  await put(row);
  const rows = await safe((b) => b.all(), []);
  for (const r of rows) if (r.kind === "star" && r.id === id && r.seq !== row.seq) await safe((b) => b.del(r.seq), undefined);
  await refreshCount();
}

/**
 * Queue a read or unread mark for ids; answers the way the server would for a plain by-id mark. Rejects with
 * QueueWriteError when the change could not be stored.
 */
export async function queueRead(ids: string[], read: boolean): Promise<MarkReadResponse> {
  await put({ kind: "read", ids, read, seq: nextSeq() });
  await refreshCount();
  return { changed: ids, restored: [] };
}

/**
 * The service worker's stored copy of a list or an article is the server's word as of the time it was stored; the queue
 * holds what this user did since. This is the one place the two are put together: it lays the queued changes, oldest
 * first so a later one wins, over articles that came from that copy. Everything that serves the stored copy calls it.
 */
export async function overlayPending<T extends { id: string; read: boolean; starred: boolean }>(items: T[]): Promise<T[]> {
  const read = new Map<string, boolean>();
  const starred = new Map<string, boolean>();
  for (const r of await safe((b) => b.all(), [])) {
    if (r.kind === "star") starred.set(r.id, r.starred);
    else for (const id of r.ids) read.set(id, r.read);
  }
  return items.map((i) => (read.has(i.id) || starred.has(i.id) ? { ...i, read: read.get(i.id) ?? i.read, starred: starred.get(i.id) ?? i.starred } : i));
}

/**
 * A change made with the network up settles what a queued change to the same articles would have said, so the
 * queued one must not be replayed over it later. Called before an online write; cheap when nothing waits.
 */
export async function supersede(change: { star?: string; read?: string[] }): Promise<void> {
  if (offlineStore.get().pending === 0) return;
  const rows = await safe((b) => b.all(), []);
  const ids = new Set(change.read ?? []);
  const touches = (r: Queued) => (r.kind === "star" ? r.id === change.star : r.ids.some((i) => ids.has(i)));
  for (const r of rows) {
    if (!touches(r)) continue;
    // Narrowed against the row as stored at the moment of the write, never the copy read above: a flush may have
    // sent and removed it meanwhile, and writing that copy back would queue it again.
    await safe(
      (b) =>
        b.update(r.seq, (cur) => {
          if (cur.kind === "star") return null;
          const rest = cur.ids.filter((i) => !ids.has(i));
          return rest.length ? { ...cur, ids: rest } : null;
        }),
      undefined,
    );
  }
  await refreshCount();
  // A running flush may already have sent one of those rows: let that request finish first, so the online write
  // that follows lands after it and has the last word. Only a request for the same articles is waited for, and
  // never for long: a stalled request (a network switch) must not hold up every online change behind it.
  const inFlight = sending;
  if (inFlight && touches(inFlight.row)) await waitAtMost(inFlight.done, SUPERSEDE_WAIT_MS);
}

/** How long an online change waits for a queued request to the same articles that is already on its way. */
export const SUPERSEDE_WAIT_MS = 3000;
/** A queued change being sent is given up (and kept for the next run) after this long without an answer. */
export const FLUSH_REQUEST_MS = 20_000;

function waitAtMost(p: Promise<unknown>, ms: number): Promise<void> {
  return new Promise<void>((resolve) => {
    const t = setTimeout(resolve, ms);
    p.then(
      () => (clearTimeout(t), resolve()),
      () => (clearTimeout(t), resolve()),
    );
  });
}

let flushing: Promise<void> | undefined;
/** The queued change being sent right now and its request, if any (supersede waits for it). */
let sending: { row: Queued; done: Promise<unknown> } | undefined;

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
  let sent = 0;
  let dropped = 0;
  const confirmed: Queued[] = [];
  // Each row is read from the store right before it is sent, never from a list taken at the start: an online
  // write in the meantime (supersede) may have removed it or narrowed its ids, and replaying the old copy
  // would undo that write. Rows queued during the run are picked up too, in order.
  let after = -Infinity;
  for (;;) {
    const r = (await safe((b) => b.all(), [])).find((x) => x.seq > after);
    if (!r) break;
    after = r.seq;
    // A request that never answers (a stalled connection) is given up after a while, like a network failure: the
    // row stays for the next run, and the run ends instead of holding every later flush behind it.
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(new DOMException("the queued change got no answer", "TimeoutError")), FLUSH_REQUEST_MS);
    try {
      const done =
        r.kind === "star"
          ? api(`/api/items/${r.id}/star`, { method: "PUT", body: { starred: r.starred, at: r.at }, signal: ctl.signal })
          : api("/api/items/mark-read", { method: "POST", body: { ids: r.ids, read: r.read, reason: "key" }, signal: ctl.signal });
      sending = { row: r, done };
      await done;
      confirmed.push(r);
    } catch (e) {
      if (ctl.signal.aborted || isOffline(e) || (e instanceof ApiError && (e.status === 401 || e.status === 429 || e.status >= 500))) break;
      // Any other refusal is final: drop the change, and say so below.
      dropped++;
    } finally {
      clearTimeout(timer);
      sending = undefined;
    }
    await safe((b) => b.del(r.seq), undefined);
    sent++;
  }
  await refreshCount();
  if (sent > 0 && qc) void qc.invalidateQueries({ queryKey: keys.bootstrap });
  // What the server just took is what the screen shows, whether or not the event stream is up to say so.
  if (qc) for (const r of confirmed) patchItems(qc, r.kind === "star" ? [r.id] : r.ids, r.kind === "star" ? { starred: r.starred } : { read: r.read });
  if (dropped > 0) {
    // The screen still shows what those changes would have done; reload it from the server.
    if (qc) void qc.invalidateQueries({ queryKey: keys.itemsAll });
    if (qc) void qc.invalidateQueries({ queryKey: ["item"] });
    toast(dropped === 1 ? "A change made offline couldn't be saved." : `${dropped} changes made offline couldn't be saved.`, "error");
  }
}

/**
 * Sign-out ends the device's offline data: the queue, and the worker's copies of API answers and images. Only an
 * explicit sign-out does this. An expired session (401) keeps the queue so the changes survive logging back in.
 */
export async function wipeOfflineData(): Promise<void> {
  await safe((b) => b.clear(), undefined);
  setPending(0);
  wipeStatsQueue();
  // From the page, not only through the worker: a page that is not controlled (hard reload) cannot message it.
  if (typeof caches !== "undefined") await Promise.all([caches.delete("kipple-data"), caches.delete("kipple-images")]).catch(() => {});
  navigator.serviceWorker?.controller?.postMessage({ type: "clear-data" });
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
  // The list the app itself asks for first, so the worker files the answer under the same address.
  const order = resolveOrder(devicePrefsStore.get(), {}, sessionLayoutStore.get()) === "oldest" ? "oldest" : undefined;
  const params = itemsParams({ view: "unread", order }, undefined, PAGE_SIZE);
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
  // TanStack Query assumes it starts online and learns otherwise only from an `offline` event, so an app launched
  // offline never sees a change when the network comes back, and its failed screens would not load again by
  // themselves (refetchOnReconnect). Start it from what the browser says.
  onlineManager.setOnline(navigator.onLine !== false);
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

  // A request that got through after a failure is the earliest sign the network is back, often before the
  // browser's own `online` event (which a patchy connection never fires): send what waits.
  let wasOnline = offlineStore.get().online;
  const unsubNet = offlineStore.subscribe(() => {
    const now = offlineStore.get().online;
    if (now && !wasOnline) void flushQueue(qc);
    wasOnline = now;
  });
  cleanups.push(unsubNet);

  if (import.meta.env.PROD && "serviceWorker" in navigator) {
    cleanups.push(watchForUpdates(navigator.serviceWorker));
    void navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => {});
  }
  return () => cleanups.forEach((f) => f());
}

/**
 * The new worker skips waiting and takes over, so a controller change on a page that was already controlled means
 * new code is ready; reloading is the user's call, so a banner offers it. Whether the page was controlled is read
 * when each change happens, not once at launch: a tab opened before any worker existed gets its first controller
 * (that is the install, not an update), and every change after that is an update.
 */
export function watchForUpdates(sw: Pick<ServiceWorkerContainer, "controller" | "addEventListener" | "removeEventListener">): () => void {
  let controlled = !!sw.controller;
  const onChange = () => {
    if (controlled) setUpdateReady();
    controlled = !!sw.controller || controlled;
  };
  sw.addEventListener("controllerchange", onChange);
  return () => sw.removeEventListener("controllerchange", onChange);
}
