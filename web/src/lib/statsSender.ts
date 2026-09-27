import { useCallback, useEffect, useSyncExternalStore } from "react";
import { hashKey, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, ApiError, authStore, clientKind } from "@/api/client";
import { keys } from "@/api/queryKeys";
import type { Bootstrap } from "@/api/types";
import type { LinkTarget } from "./devicePrefs";
import { openExternal } from "./links";
import { offlineStore } from "./offlineState";
import { safeHttpUrl } from "./safeUrl";
import { shareLink, type ShareResult } from "./share";

/**
 * The web client's half of the stats contract (docs/design.md section 8, rule 5): active reading time, scroll depth,
 * open-original and share events, sent to POST /api/stats/events. Nothing here touches read state.
 *
 * One session per article open, keyed by the `session_key` the open answered with. Time counts in whole seconds, a
 * tick at a time, and only while the tab is visible and focused, the reader has done something in the article in the
 * last two minutes, and the article is on screen (the hook is mounted by the article view, so leaving it ends the
 * session). Every event carries an `event_id` made once, when the event is made, and kept through the queue and every
 * retry: the server drops an id it has already seen, so a repeated send is harmless.
 */

export type StatsKind = "read_time" | "scroll" | "open_original" | "share";

export interface StatsEvent {
  kind: StatsKind;
  item_id: number;
  session_key?: string;
  value?: number;
  /** Made once when the event is made (newEventId) and never changed: the server's duplicate check. */
  event_id: string;
}

export const STATS_URL = "/api/stats/events";
/** The most seconds one read_time event may carry (the server rejects more). */
export const MAX_EVENT_SECONDS = 60;
export const FLUSH_EVERY_MS = 15_000;
/** No scroll, touch, key or pointer activity in the article for this long stops the clock until the next one. */
export const IDLE_CUTOFF_MS = 120_000;
/** Session keys are honoured by the server for 12 hours after the open. */
export const SESSION_TTL_MS = 12 * 3_600_000;
/** Events without a session (open_original, share) wait in the queue at most this long. */
export const ITEM_EVENT_TTL_MS = 24 * 3_600_000;
/** An article that fits the screen counts as seen in full only after this many active seconds (its content settles first). */
export const FIT_AFTER_SECONDS = 3;
/** A queued request that gets no answer for this long is given up (the batch stays queued). */
export const FLUSH_TIMEOUT_MS = 20_000;
/** Scroll events on the article in a session's first half second are the view's own scroll to the top, not reading. */
export const SCROLL_GRACE_MS = 500;
/** After the tab comes back, look again this long after for focus, and start the clock if it is there. */
export const VISIBLE_RECHECK_MS = [1_000, 3_000] as const;

/** 128 random bits as 32 hex characters. crypto.getRandomValues when there is one, else Math.random. */
export function newEventId(): string {
  const b = new Uint8Array(16);
  const c = typeof globalThis !== "undefined" ? globalThis.crypto : undefined;
  if (c && typeof c.getRandomValues === "function") c.getRandomValues(b);
  else for (let i = 0; i < b.length; i++) b[i] = Math.floor(Math.random() * 256);
  let s = "";
  for (const x of b) s += x.toString(16).padStart(2, "0");
  return s;
}

// ---- The setting ------------------------------------------------------------------------------

/** On, off, or not known yet (the bootstrap answer is not in the cache). */
export type StatsState = "on" | "off" | "unknown";

export function statsStateOf(qc: QueryClient | undefined): StatsState {
  const b = qc?.getQueryData<Bootstrap>(keys.bootstrap);
  if (b === undefined) return "unknown";
  return b.settings?.["stats.enabled"] === false ? "off" : "on";
}

/** The app's query client, whose bootstrap answer holds the setting. Set by initStatsQueue. */
let source: QueryClient | undefined;

/** Where the setting is read from (initStatsQueue does this; tests call it directly). */
export function setStatsClient(qc: QueryClient | undefined): void {
  source = qc;
}

/**
 * The one place sending checks the setting. Read live from the bootstrap answer, so a switch-off is seen by the very
 * next send (a session's last flush after the switch included). Unknown sends nothing and keeps the queue.
 */
export function statsState(): StatsState {
  return statsStateOf(source);
}

let bootHash: string | undefined;
const bootstrapHash = () => (bootHash ??= hashKey(keys.bootstrap));

// ---- Session state ---------------------------------------------------------------------------

interface SessionState {
  startedAt: number;
  /** Whole seconds counted and not yet sent. */
  pending: number;
  /** The deepest scroll already sent for this session: a picked-up session sends only a deeper one. */
  sentScroll: number;
}

const sessions = new Map<string, SessionState>();
const KEEP_SESSIONS = 50;

function stateFor(key: string, now: number): SessionState {
  let s = sessions.get(key);
  if (!s) {
    s = { startedAt: now, pending: 0, sentScroll: 0 };
    sessions.set(key, s);
    // Oldest first: a Map iterates in insertion order.
    while (sessions.size > KEEP_SESSIONS) sessions.delete(sessions.keys().next().value as string);
  }
  return s;
}

/** Tests: forget every session, the queue flush state and the setting's source. */
export function resetStatsForTests(): void {
  sessions.clear();
  flushing = undefined;
  again = false;
  source = undefined;
  wiped = false;
}

/** Turn what a session has gathered into events and mark it sent. Whole seconds in chunks of at most a minute. */
function drain(itemId: number, key: string, s: SessionState, maxScroll: number): StatsEvent[] {
  const out: StatsEvent[] = [];
  while (s.pending > 0) {
    const value = Math.min(MAX_EVENT_SECONDS, s.pending);
    s.pending -= value;
    out.push({ kind: "read_time", item_id: itemId, session_key: key, value, event_id: newEventId() });
  }
  if (maxScroll > s.sentScroll) {
    s.sentScroll = maxScroll;
    out.push({ kind: "scroll", item_id: itemId, session_key: key, value: maxScroll, event_id: newEventId() });
  }
  return out;
}

// ---- The session timer -----------------------------------------------------------------------

export interface SessionOptions {
  itemId: number;
  sessionKey: string;
  /** The article's own scroll container: its scrolling is activity and its depth is measured. Never the list. */
  scroller?: HTMLElement | null;
  /**
   * The article pane: pointer and touch activity counts only inside it, keys inside it or with nothing focused.
   * Defaults to the scroller. Activity over the list beside it (the wide layout) never counts.
   */
  pane?: HTMLElement | null;
  send?: (events: StatsEvent[]) => void;
}

type Cancel = () => void;
function nextFrame(f: () => void): Cancel {
  if (typeof requestAnimationFrame === "function") {
    const h = requestAnimationFrame(f);
    return () => cancelAnimationFrame(h);
  }
  const h = setTimeout(f, 16);
  return () => clearTimeout(h);
}

/**
 * Start counting for one article open. Returns the stop function, which sends what is left.
 *
 * The 1 s tick runs only while it can count: a tick that finds the tab hidden or unfocused, the reader idle or the
 * session expired stops it (and sends what it holds), and activity, the tab coming back or focus starts it again.
 * Past the session's 12 h it stops for good. Scroll depth moves only on real scrolls of the article (measured once a
 * frame), never at start or stop: at stop the scroller may already show the next article.
 */
export function startReadingSession(o: SessionOptions): () => void {
  const send = o.send ?? sendStats;
  const s = stateFor(o.sessionKey, Date.now());
  const el = o.scroller ?? null;
  const region = o.pane ?? el;
  const openedAt = Date.now();
  let lastActivity = openedAt;
  let maxScroll = 0;
  /** Seconds this run has counted: the "fits the screen" rule waits for FIT_AFTER_SECONDS of them. */
  let active = 0;
  let ticks = 0;
  let timer: ReturnType<typeof setInterval> | undefined;
  let stopped = false;
  let expired = false;

  const flush = () => {
    const events = drain(o.itemId, o.sessionKey, s, maxScroll);
    if (events.length) send(events);
  };

  const pastTtl = (now: number) => now - s.startedAt > SESSION_TTL_MS;
  // document.hasFocus() gates the count as the design says (rule 5). Still to be checked on an iOS device (a PWA and
  // Safari): if it reads false there while the article is being read, time would never count on iOS.
  const canCount = (now: number) => document.visibilityState === "visible" && document.hasFocus() && now - lastActivity < IDLE_CUTOFF_MS;

  const pause = () => {
    if (timer === undefined) return;
    clearInterval(timer);
    timer = undefined;
  };

  // ---- "fits the screen" ----
  // Evaluated on a tick, once the run has counted FIT_AFTER_SECONDS, while the scroller is still in the document, and
  // again whenever the content changes size (a late image). It can only raise the depth.
  let fitDirty = true;
  const ro = el && typeof ResizeObserver === "function" ? new ResizeObserver(() => void (fitDirty = true)) : undefined;
  if (el) ro?.observe(el.firstElementChild ?? el);
  const checkFits = () => {
    if (!el || active < FIT_AFTER_SECONDS || !fitDirty || maxScroll >= 100) return;
    if (ro) fitDirty = false; // without an observer there is no signal: look on every tick
    if (!el.isConnected) return;
    const h = el.scrollHeight;
    if (h > 0 && h <= el.clientHeight + 1) maxScroll = 100;
  };

  const tick = () => {
    const now = Date.now();
    if (pastTtl(now)) expired = true;
    if (expired || !canCount(now)) {
      pause();
      flush();
      return;
    }
    s.pending++;
    active++;
    checkFits();
    if (++ticks % (FLUSH_EVERY_MS / 1000) === 0) flush();
  };

  const arm = () => {
    if (stopped || expired || timer !== undefined) return;
    const now = Date.now();
    if (pastTtl(now)) {
      expired = true; // the server would drop anything more
      return;
    }
    if (canCount(now)) timer = setInterval(tick, 1000);
  };

  const touch = () => {
    lastActivity = Date.now();
    arm();
  };
  const inPane = (t: EventTarget | null) => !!region && t instanceof Node && region.contains(t);
  const onPointer = (e: Event) => {
    if (inPane(e.target)) touch();
  };
  const onKey = (e: Event) => {
    const t = e.target;
    // A key with nothing focused lands on the body (or the document): it is the reader's, not the list's.
    if (inPane(t) || t === null || t === document || t === document.body || t === document.documentElement || t === window) touch();
  };

  // ---- scroll depth ----
  let cancelFrame: Cancel | undefined;
  const measure = () => {
    cancelFrame = undefined;
    if (stopped || !el) return;
    // Read once a frame, however many scroll events came in it.
    const top = el.scrollTop;
    const client = el.clientHeight;
    const height = el.scrollHeight;
    if (height <= 0) return;
    const pct = Math.round(((top + client) / height) * 100);
    maxScroll = Math.max(maxScroll, Math.min(100, Math.max(0, pct)));
  };
  const onScroll = () => {
    // The article view scrolls itself to the top when the article changes (a programmatic scrollTo): a scroll event
    // this early is that, not the reader, so it neither counts as activity nor moves the depth.
    if (Date.now() - openedAt < SCROLL_GRACE_MS) return;
    touch();
    cancelFrame ??= nextFrame(measure);
  };

  // Coming back to the tab, focus may land a moment after the visibilitychange (and on iOS, whether it lands at
  // all is still to be verified on a device). Look again at about 1 s and 3 s, so the clock does not wait for a touch.
  let rechecks: ReturnType<typeof setTimeout>[] = [];
  const clearRechecks = () => {
    for (const h of rechecks) clearTimeout(h);
    rechecks = [];
  };
  const onVisibility = () => {
    clearRechecks();
    if (document.visibilityState === "hidden") {
      pause();
      flush();
    } else {
      touch(); // coming back is a sign of life
      rechecks = VISIBLE_RECHECK_MS.map((ms) => setTimeout(arm, ms)); // arm starts the tick only if visible and focused
    }
  };
  const onPageHide = () => flush();

  const doc = document;
  const pointer = ["touchstart", "pointerdown", "pointermove"] as const;
  for (const t of pointer) doc.addEventListener(t, onPointer, { passive: true, capture: true });
  doc.addEventListener("keydown", onKey, { passive: true, capture: true });
  el?.addEventListener("scroll", onScroll, { passive: true });
  doc.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("pagehide", onPageHide);
  // No blur listener: focus moving into an embedded player's iframe blurs the window while document.hasFocus()
  // stays true, and that is still reading. A tick that finds the document unfocused pauses by itself.
  window.addEventListener("focus", touch);

  arm();

  return () => {
    stopped = true;
    pause();
    clearRechecks();
    cancelFrame?.();
    cancelFrame = undefined;
    ro?.disconnect();
    for (const t of pointer) doc.removeEventListener(t, onPointer, { capture: true });
    doc.removeEventListener("keydown", onKey, { capture: true });
    el?.removeEventListener("scroll", onScroll);
    doc.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("pagehide", onPageHide);
    window.removeEventListener("focus", touch);
    flush(); // no measuring here: the scroller may already hold another article
  };
}

// ---- The offline queue: one localStorage key per batch ----------------------------------------

interface Batch {
  id: number;
  at: number;
  client: "web" | "pwa";
  events: StatsEvent[];
}
interface Entry extends Batch {
  key: string;
}

const Q_PREFIX = "kipple.stats.q.";
/** The single-array queue of an earlier build: its entries carry no event_id, so it is deleted, not migrated. */
const OLD_QUEUE_KEY = "kipple.stats.queue.v1";
/** The queue holds at most this many events; the oldest go first. */
export const QUEUE_MAX_EVENTS = 1000;
/** The server takes at most 200 events per request. */
const BATCH_MAX = 200;
const KINDS = new Set<string>(["read_time", "scroll", "open_original", "share"]);
const EVENT_ID = /^[A-Za-z0-9_-]{16,64}$/;

function storage(): Storage | undefined {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined; // blocked storage throws on access
  }
}

function isEvent(v: unknown): v is StatsEvent {
  if (!v || typeof v !== "object") return false;
  const e = v as Record<string, unknown>;
  return (
    typeof e.kind === "string" &&
    KINDS.has(e.kind) &&
    typeof e.item_id === "number" &&
    Number.isSafeInteger(e.item_id) &&
    e.item_id > 0 &&
    typeof e.event_id === "string" &&
    EVENT_ID.test(e.event_id) &&
    (e.session_key === undefined || (typeof e.session_key === "string" && e.session_key !== "")) &&
    (e.value === undefined || (typeof e.value === "number" && Number.isFinite(e.value)))
  );
}

function isBatchBody(v: unknown): v is Omit<Batch, "id"> {
  if (!v || typeof v !== "object") return false;
  const b = v as Record<string, unknown>;
  return (
    typeof b.at === "number" &&
    Number.isFinite(b.at) &&
    (b.client === "web" || b.client === "pwa") &&
    Array.isArray(b.events) &&
    b.events.length > 0 &&
    b.events.length <= BATCH_MAX &&
    b.events.every(isEvent)
  );
}

/** Whether an event queued at `at` is still worth sending: 12 h for a session's events, 24 h for the rest. */
const fresh = (e: StatsEvent, at: number, now: number) => now - at < (e.session_key ? SESSION_TTL_MS : ITEM_EVENT_TTL_MS);

/**
 * Every queued batch, oldest first. A key that does not parse or has the wrong shape is deleted; so is one whose
 * events are all past their age. The old single-key queue is deleted on the way.
 */
function readEntries(now = Date.now()): Entry[] {
  const ls = storage();
  if (!ls) return [];
  const out: Entry[] = [];
  try {
    ls.removeItem(OLD_QUEUE_KEY);
    const found: string[] = [];
    for (let i = 0; i < ls.length; i++) {
      const k = ls.key(i);
      if (k?.startsWith(Q_PREFIX)) found.push(k);
    }
    for (const key of found) {
      const id = Number(key.slice(Q_PREFIX.length));
      let v: unknown;
      try {
        v = JSON.parse(ls.getItem(key) ?? "");
      } catch {
        v = undefined;
      }
      if (!Number.isSafeInteger(id) || id <= 0 || !isBatchBody(v)) {
        ls.removeItem(key);
        continue;
      }
      const events = v.events.filter((e) => fresh(e, v.at, now));
      if (events.length === 0) {
        ls.removeItem(key);
        continue;
      }
      out.push({ id, key, at: v.at, client: v.client, events });
    }
  } catch {
    /* storage went away mid-read: what was read is still good */
  }
  return out.sort((a, b) => a.id - b.id);
}

let lastId = 0;
/** Rising ids; a key another tab took in the same millisecond is skipped rather than overwritten. */
function freeKey(ls: Storage): string {
  lastId = Math.max(lastId + 1, Date.now() * 1000 + Math.floor(Math.random() * 1000));
  while (ls.getItem(Q_PREFIX + lastId) !== null) lastId++;
  return Q_PREFIX + lastId;
}

/**
 * Set by the sign-out wipe and cleared by the next sign-in. Anything that tries to queue in between (a session's
 * last flush after the wipe, a keepalive answered late) is dropped: it belongs to the account that signed out, and
 * the next person at this browser must not send it under their own.
 */
let wiped = false;
authStore.subscribe(() => {
  if (authStore.get() === "in") wiped = false;
});

/** Whether events may be kept for later: not while signed out, nor after the sign-out wipe until the next sign-in. */
const mayQueue = () => !wiped && authStore.get() !== "out";

function enqueue(client: "web" | "pwa", events: StatsEvent[]): void {
  const ls = storage();
  if (!ls || events.length === 0 || !mayQueue()) return;
  const at = Date.now();
  try {
    for (let i = 0; i < events.length; i += BATCH_MAX) {
      ls.setItem(freeKey(ls), JSON.stringify({ at, client, events: events.slice(i, i + BATCH_MAX) }));
    }
  } catch {
    /* full or blocked storage: these events are lost */
  }
  // The cap: the oldest batches go first.
  const all = readEntries(at);
  let total = all.reduce((n, b) => n + b.events.length, 0);
  for (const b of all) {
    if (total <= QUEUE_MAX_EVENTS) break;
    total -= b.events.length;
    try {
      ls.removeItem(b.key);
    } catch {
      /* nothing to do */
    }
  }
}

/**
 * Sign-out: every queued stats batch goes (lib/offline.ts wipeOfflineData calls this), with every session's unsent
 * counts, and from here until the next sign-in nothing more is queued or sent (a session stopped after this, when the
 * cleared cache ends it, is dropped).
 */
export function wipeStatsQueue(): void {
  wiped = true;
  sessions.clear();
  const ls = storage();
  if (!ls) return;
  try {
    const found: string[] = [];
    for (let i = 0; i < ls.length; i++) {
      const k = ls.key(i);
      if (k?.startsWith(Q_PREFIX)) found.push(k);
    }
    for (const k of found) ls.removeItem(k);
    ls.removeItem(OLD_QUEUE_KEY);
  } catch {
    /* nothing to do */
  }
}

function removeKey(key: string): void {
  try {
    storage()?.removeItem(key);
  } catch {
    /* nothing to do */
  }
}

/** Tests: a session's counters, to set up cases the timers alone cannot reach. */
export function sessionStateForTests(key: string): SessionState {
  return stateFor(key, Date.now());
}

/** Tests: what waits for the network, oldest first. */
export function queuedStatsForTests(): Batch[] {
  return readEntries().map(({ id, at, client, events }) => ({ id, at, client, events }));
}

// ---- Sending ---------------------------------------------------------------------------------

/**
 * Send events. With stats off they are dropped, and so they are while signed out and after the sign-out wipe until
 * the next sign-in; before the setting is known (and not signed out) they wait in the queue (and go, or are emptied,
 * once it is). sendBeacon when the browser has it (the only call that survives the page going away), with
 * the JSON as a plain string body, so no browser refuses its type; a false answer means the browser refused to queue
 * it, so the events wait in the local queue. When the app already knows it is offline they go straight to the queue.
 * Without sendBeacon a keepalive fetch does the same, classifying the answer like the queue flush: a network failure,
 * an opaque redirect (the access proxy's login), 429 and 5xx queue; 401 and other refusals drop.
 */
export function sendStats(events: StatsEvent[]): void {
  if (events.length === 0) return;
  // Signed out, or signed out and wiped (the cache cleared, so the setting reads unknown): dropped, never queued.
  if (!mayQueue()) return;
  const state = statsState();
  if (state === "off") return;
  const client = clientKind();
  if (state === "unknown" || (typeof navigator !== "undefined" && navigator.onLine === false) || !offlineStore.get().online) {
    return enqueue(client, events);
  }
  const body = JSON.stringify({ client, events });
  try {
    if (typeof navigator.sendBeacon === "function") {
      if (!navigator.sendBeacon(STATS_URL, body)) enqueue(client, events);
      return;
    }
    fetch(STATS_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Kipple-Client": client },
      body,
      keepalive: true,
      credentials: "same-origin",
      redirect: "manual",
    }).then(
      (res) => {
        if (res.type === "opaqueredirect" || res.status === 429 || res.status >= 500) enqueue(client, events);
      },
      () => enqueue(client, events),
    );
  } catch {
    enqueue(client, events);
  }
}

/** Open-original and share carry the item alone. The setting is checked by sendStats. */
export function sendItemEvent(kind: "open_original" | "share", itemId: string): void {
  const n = Number(itemId);
  if (Number.isSafeInteger(n) && n > 0) sendStats([{ kind, item_id: n, event_id: newEventId() }]);
}

/**
 * Open an article's original page, only when its address is plain http or https (it comes from the feed), and record
 * it. Every "open original" in the app goes through here. Returns whether anything opened.
 */
export function openOriginalAndRecord(item: { id: string; url?: string | null }, target?: LinkTarget): boolean {
  const url = safeHttpUrl(item.url);
  if (!url) return false;
  openExternal(url, target);
  sendItemEvent("open_original", item.id);
  return true;
}

/**
 * Share an article's link and record it. A share counts when the sheet resolved or the link was copied; a dismissed
 * sheet ("cancelled") and a failed copy are not shares. Every share in the app goes through here.
 */
export async function shareAndRecord(item: { id: string; title: string; url: string }): Promise<ShareResult> {
  const r = await shareLink(item);
  if (r === "shared" || r === "copied") sendItemEvent("share", item.id);
  return r;
}

// ---- Flushing the queue ----------------------------------------------------------------------

let flushing: Promise<void> | undefined;
let again = false;

/**
 * Send what waited. Kept apart from the change queue in offline.ts on purpose: that one is ordered and replayed
 * against read state and stars, and stats must never delay or reorder it.
 *
 * One run at a time per tab, and across tabs through the Web Locks API when the browser has it (a tab that finds the
 * lock taken skips its run: the other tab is sending the same keys). Nothing is sent while signed out or while the
 * setting is not known; with stats off the queue is emptied unsent. Batches are joined into requests of at most 200
 * events per client, oldest first. A run stops, keeping the batch, at a network failure, a 20 s timeout, 401, 429,
 * 5xx or the access proxy's redirect; any other refusal drops the batch. A batch's key is removed only after the whole
 * request carrying it succeeded.
 */
export function flushStatsQueue(): Promise<void> {
  // A call that lands while a run is going may mean events arrived after that run looked: go round once more.
  if (flushing) {
    again = true;
    return flushing;
  }
  flushing = (async () => {
    do {
      again = false;
      await withFlushLock(doFlush);
    } while (again);
  })().finally(() => void (flushing = undefined));
  return flushing;
}

async function withFlushLock(f: () => Promise<void>): Promise<void> {
  const locks = typeof navigator !== "undefined" ? (navigator as Navigator & { locks?: LockManager }).locks : undefined;
  if (!locks || typeof locks.request !== "function") return f();
  let ran = false;
  try {
    await locks.request("kipple-stats-flush", { ifAvailable: true }, async (lock) => {
      if (!lock) return; // another tab is flushing
      ran = true;
      await f();
    });
  } catch {
    if (!ran) await f(); // the lock manager itself failed: this tab's own guard still holds
  }
}

type Outcome = "sent" | "dropped" | "stop";

async function post(client: "web" | "pwa", events: StatsEvent[]): Promise<Outcome> {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), FLUSH_TIMEOUT_MS);
  try {
    // The shared helper: a 401 signs the app out and the access proxy's redirect marks the session expired, as
    // everywhere else. Quiet: a stats failure is not what decides the app is offline.
    await api(STATS_URL, { method: "POST", body: { client, events }, signal: ctl.signal, quiet: true });
    return "sent";
  } catch (e) {
    if (ctl.signal.aborted || !(e instanceof ApiError)) return "stop";
    if (e.status === 0 || e.status === 401 || e.status === 429 || e.status >= 500) return "stop";
    return "dropped";
  } finally {
    clearTimeout(timer);
  }
}

async function doFlush(): Promise<void> {
  if (authStore.get() === "out") return;
  const state = statsState();
  if (state === "unknown") return; // wait: the next trigger looks again
  if (state === "off") return wipeStatsQueue();
  const done = new Set<string>();
  for (;;) {
    // Read afresh each round: batches queued meanwhile join in order.
    const entries = readEntries().filter((e) => !done.has(e.key));
    const first = entries[0];
    if (!first) return;
    const group: Entry[] = [];
    let n = 0;
    for (const e of entries) {
      if (e.client !== first.client) continue;
      if (n + e.events.length > BATCH_MAX) break;
      group.push(e);
      n += e.events.length;
    }
    const outcome = await post(
      first.client,
      group.flatMap((e) => e.events),
    );
    if (outcome === "stop") return;
    for (const e of group) {
      done.add(e.key);
      removeKey(e.key);
    }
  }
}

/**
 * Wire the queue's flush to the same signals the change queue uses: the browser's online event, the app's own
 * online flag turning true, the tab becoming visible, a sign-in, the setting becoming known or changing, launch, and
 * a minute timer. Called once from main.tsx; returns a cleanup for tests.
 */
export function initStatsQueue(qc: QueryClient): () => void {
  setStatsClient(qc);
  const go = () => void flushStatsQueue();
  const cleanups: Array<() => void> = [];
  const on = (t: string, f: () => void) => {
    window.addEventListener(t, f);
    cleanups.push(() => window.removeEventListener(t, f));
  };
  on("online", go);
  const onVisible = () => document.visibilityState === "visible" && go();
  document.addEventListener("visibilitychange", onVisible);
  cleanups.push(() => document.removeEventListener("visibilitychange", onVisible));
  let was = offlineStore.get().online;
  cleanups.push(
    offlineStore.subscribe(() => {
      const now = offlineStore.get().online;
      if (now && !was) go();
      was = now;
    }),
  );
  let auth = authStore.get();
  cleanups.push(
    authStore.subscribe(() => {
      const now = authStore.get();
      if (now === "in" && auth !== "in") go();
      auth = now;
    }),
  );
  let state = statsStateOf(qc);
  cleanups.push(
    qc.getQueryCache().subscribe((e) => {
      if (e.query.queryHash !== bootstrapHash()) return;
      const now = statsStateOf(qc);
      if (now === state) return;
      state = now;
      if (now !== "unknown") go(); // known on: send; known off: empty the queue
    }),
  );
  const timer = window.setInterval(go, 60_000);
  cleanups.push(() => window.clearInterval(timer));
  cleanups.push(() => {
    if (source === qc) source = undefined;
  });
  go();
  return () => cleanups.forEach((f) => f());
}

// ---- React -----------------------------------------------------------------------------------

/**
 * Whether stats are on, read from the bootstrap answer the app already holds (it follows a settings change). Reads
 * the cache only: nothing is fetched here. Until the answer is there it is off, so nothing counts on a guess.
 */
export function useStatsEnabled(): boolean {
  const qc = useQueryClient();
  // Not useQuery: a second observer on the bootstrap key would replace its options (the fetch function) with ours.
  // Stable per client, and woken only by the bootstrap query, not by every cache event.
  const subscribe = useCallback(
    (notify: () => void) =>
      qc.getQueryCache().subscribe((e) => {
        if (e.query.queryHash === bootstrapHash()) notify();
      }),
    [qc],
  );
  return useSyncExternalStore(subscribe, () => statsStateOf(qc) === "on");
}

/**
 * Count reading time and scroll depth for the article that is open. A session runs only while both the scroller and
 * the pane element exist. It ends, and what it holds is sent, when the article changes, either element changes or
 * goes (the listeners move to the new one), the view unmounts or the setting is switched off.
 */
export function useReadingStats(o: { itemId: string; sessionKey: string; enabled: boolean; scroller: HTMLElement | null; pane?: HTMLElement | null }): void {
  const { itemId, sessionKey, enabled, scroller, pane } = o;
  useEffect(() => {
    const n = Number(itemId);
    // Both elements, or no session: without them the article is not on screen (the error screen after a failed
    // refetch has neither), and a running session ends here through the cleanup.
    if (!enabled || !sessionKey || !Number.isFinite(n) || !scroller || !pane) return;
    return startReadingSession({ itemId: n, sessionKey, scroller, pane });
  }, [itemId, sessionKey, enabled, scroller, pane]);
}
