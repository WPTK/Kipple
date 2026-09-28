import { useEffect } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import { api, authStore } from "./client";
import { filtersKey } from "./filters";
import { wasFilterTouched } from "./filterEdits";
import { countsGuardLeft, dropFromLists, invalidateLists, keys, patchItems, type ItemPatch } from "./queries";
import { SAVED_COUNTS_MIN_MS, invalidateSavedSearches, refreshSavedSearchCounts, resetSavedSearchCounts } from "./savedSearches";
import { announce } from "@/shell/toasts";
import { createStore } from "@/lib/store";
import {
  SERVER_EVENT_TYPES,
  type Bootstrap,
  type CountsEvent,
  type RunStatus,
  type ServerEvent,
  type StatusResponse,
} from "./types";

// ---- Live state: what the UI shows about background activity ----------------

export interface LiveState {
  /** New items fetched since the visible list loaded, by feed id (drives the "n new" pill). */
  pendingByFeed: Record<string, number>;
  /** The new item ids behind `pendingByFeed`, when the server sent them: an id the list already has is not "new". */
  pendingIds: Record<string, string[]>;
  /** Active fetch runs by run id. */
  runs: Record<string, RunStatus>;
  /**
   * How recent runs ended, by run id: the `run.done` numbers (`changed`, `scanned`, `error`) that the last progress
   * event does not have (the server throttles those to one per 500 ms). Bounded and short lived.
   */
  finished: Record<string, FinishedRun>;
  transport: "connecting" | "open" | "fallback";
}

export interface FinishedRun {
  kind?: string;
  changed?: number;
  scanned?: number;
  errors: number;
  error?: string;
  at: number;
}

/** How long, and how many, finished runs are remembered. */
export const FINISHED_KEEP_MS = 5 * 60_000;
export const FINISHED_KEEP_MAX = 20;

export const initialLive: LiveState = {
  pendingByFeed: {},
  pendingIds: {},
  runs: {},
  finished: {},
  transport: "connecting",
};

export const liveStore = createStore<LiveState>(initialLive);

/** Pure reducer for the parts of an SSE event that live outside the query cache. */
export function reduceEvent(s: LiveState, ev: ServerEvent): LiveState {
  switch (ev.type) {
    case "run.start": {
      const id = String(ev.data.run_id);
      const filter = ev.data.filter_id !== undefined ? { filter_id: String(ev.data.filter_id) } : {};
      return { ...s, runs: { ...s.runs, [id]: { id, kind: ev.data.kind, done: 0, total: ev.data.total, new_items: 0, errors: 0, ...filter } } };
    }
    case "run.progress": {
      const id = String(ev.data.run_id);
      const cur = s.runs[id];
      // The kind comes from run.start (or bootstrap and /api/status) and nothing else: no field of a progress event
      // says what a run is. One we never saw start is "unknown", which is quiet (not a refresh spinner).
      const kind = cur?.kind ?? "unknown";
      return { ...s, runs: { ...s.runs, [id]: { ...(cur?.filter_id ? { filter_id: cur.filter_id } : {}), ...ev.data, id, kind } } };
    }
    case "run.done": {
      const id = String(ev.data.run_id);
      const { [id]: gone, ...rest } = s.runs;
      const now = Date.now();
      const kept = Object.entries(s.finished ?? {})
        .filter(([, f]) => now - f.at < FINISHED_KEEP_MS)
        .sort((a, b) => b[1].at - a[1].at)
        .slice(0, FINISHED_KEEP_MAX - 1);
      const d = ev.data;
      const done: FinishedRun = {
        kind: d.kind ?? gone?.kind,
        ...(d.changed !== undefined ? { changed: d.changed } : {}),
        ...(d.scanned !== undefined ? { scanned: d.scanned } : {}),
        errors: d.errors,
        ...(d.error ? { error: d.error } : {}),
        at: now,
      };
      return { ...s, runs: rest, finished: { ...Object.fromEntries(kept), [id]: done } };
    }
    case "fetch.done": {
      if (!(ev.data.new_items > 0) || !isManualTrigger(ev.data.trigger)) return s;
      const feed = String(ev.data.feed_id);
      const ids = (ev.data.new_item_ids ?? []).map(String);
      return {
        ...s,
        pendingByFeed: { ...s.pendingByFeed, [feed]: (s.pendingByFeed[feed] ?? 0) + ev.data.new_items },
        pendingIds: ids.length ? { ...s.pendingIds, [feed]: [...(s.pendingIds[feed] ?? []), ...ids].slice(-PENDING_IDS_CAP) } : s.pendingIds,
      };
    }
    case "resync":
      return { ...s, pendingByFeed: {}, pendingIds: {} };
    case "fulltext.ready":
    case "feed.changed":
    case "filters.changed":
    case "saved_searches.changed":
    case "folder.changed":
    case "items.state":
    case "counts":
      return s; // cache-only events
  }
}

const PENDING_IDS_CAP = 500;

/** Run kinds that are housekeeping or a rule at work, not a refresh; "unknown" is a run whose start was never seen. */
const QUIET_KINDS: readonly string[] = ["retention", "filter_apply", "auto_read", "unknown"];

/** Only refresh-like runs (manual, import) are "refreshing"; the retention sweep, a filter apply and auto-read are not. */
export const isRefreshKind = (kind: string): boolean => !QUIET_KINDS.includes(kind);

/**
 * The "N new articles" pill and its announcement are for a refresh the user asked for: a manual refresh (all
 * feeds or one feed). A periodic background poll, a newly subscribed feed's first fetch, an OPML import and a
 * retention-only trim all bring in new items too, but silently — no pill, no toast.
 */
const isManualTrigger = (trigger: string | undefined): boolean => trigger === "manual" || trigger === "feed_manual";

/**
 * New items that would appear in this list: the feeds the scope includes (a feed, a folder's feeds,
 * or all). Starred and search lists never show a pill (their membership is not "new arrivals"),
 * and an oldest-first list gets new items at its far end, so a jump-to-top pill would mislead.
 */
export function pendingFor(
  pending: Record<string, number>,
  scope: { view: string; feed?: string; folder?: string; q?: string; order?: string },
  feeds: readonly { id: string; folder_id: string }[],
  /** Ids the list already holds (with `ids`, the pending ids per feed): those are not new to it. */
  loaded?: { ids: ReadonlySet<string>; pendingIds: Record<string, string[]> },
): number {
  if (scope.view === "starred" || scope.view === "muted" || scope.q || scope.order === "oldest") return 0;
  const count = (id: string): number => {
    const n = pending[id] ?? 0;
    if (!loaded || n === 0) return n;
    const seen = (loaded.pendingIds[id] ?? []).filter((x) => loaded.ids.has(x)).length;
    return Math.max(0, n - seen);
  };
  if (scope.feed) return count(scope.feed);
  let n = 0;
  if (scope.folder) {
    for (const f of feeds) if (f.folder_id === scope.folder) n += count(f.id);
    return n;
  }
  for (const id of Object.keys(pending)) n += count(id);
  return n;
}

/** `pending` without the feeds a just-reloaded list for `scope` covered. */
export function clearPending(
  pending: Record<string, number>,
  scope: Parameters<typeof pendingFor>[1],
  feeds: readonly { id: string; folder_id: string }[],
): Record<string, number> {
  if (pendingFor(pending, scope, feeds) === 0) return pending;
  if (scope.feed) return omit(pending, [scope.feed]);
  if (scope.folder) return omit(pending, feeds.filter((f) => f.folder_id === scope.folder).map((f) => f.id));
  return {};
}
const omit = <T,>(o: Record<string, T>, ids: string[]) => Object.fromEntries(Object.entries(o).filter(([k]) => !ids.includes(k))) as Record<string, T>;

/** Text for the polite live region, or null when the event is not announced. */
export function announcementFor(ev: ServerEvent, runKind?: string): string | null {
  const plural = (n: number) => `${n} new article${n === 1 ? "" : "s"}`;
  if (ev.type === "run.done") {
    const kind = runKind ?? ev.data.kind;
    if (kind === "filter_apply") {
      // Editing or deleting a rule mid-apply makes the server cancel it and report an error: that was intended.
      // The server names how an apply ended: cancelled or filter_changed (the rule was edited or deleted), shutdown,
      // timed_out, apply_failed.
      if (ev.data.error === "cancelled" || ev.data.error === "filter_changed") return "Stopped because the rule changed";
      if (ev.data.error === "shutdown") return null;
      if (ev.data.error === "timed_out") return "The filter took too long and was stopped";
      if (ev.data.error && wasFilterTouched(ev.data.filter_id)) return "Stopped because the rule changed";
      if (ev.data.error || ev.data.errors > 0) return "Couldn't finish applying the filter";
      const n = ev.data.changed ?? 0;
      return n > 0 ? `Filter applied: ${n} article${n === 1 ? "" : "s"} changed` : "Filter applied: no articles changed";
    }
    if (kind === "auto_read") return ev.data.error ? "Couldn't finish marking old articles as read" : null;
    // The retention sweep is housekeeping, not a refresh: nothing to announce.
    if (kind !== undefined && !isRefreshKind(kind)) return null;
    if (ev.data.errors > 0) return `Couldn't refresh ${ev.data.errors} feed${ev.data.errors === 1 ? "" : "s"}. The rest updated.`;
    if (ev.data.new_items > 0) return plural(ev.data.new_items);
    // Kind unknown (its run.start was missed): an empty result is not worth a toast.
    return kind === undefined ? null : "No new articles";
  }
  // A manual per-feed refresh outside any run (or one that arrives before its run.done announcement): announce
  // the new items. A scheduled poll, a new feed's first fetch, an import or a retention trim never do.
  if (ev.type === "fetch.done" && ev.data.new_items > 0 && isManualTrigger(ev.data.trigger)) {
    return plural(ev.data.new_items);
  }
  return null;
}

/** Patch unread counts in the cached bootstrap from a `counts` event. */
export function applyCounts(qc: QueryClient, c: CountsEvent): void {
  qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => {
    if (!old) return old;
    const feeds = old.feeds.map((f) => ({ ...f, unread: c.feeds[f.id] ?? 0 }));
    const folderUnread = new Map<string, number>();
    for (const f of feeds) folderUnread.set(f.folder_id, (folderUnread.get(f.folder_id) ?? 0) + f.unread);
    return {
      ...old,
      counts: { ...old.counts, unread: c.unread_total, ...(typeof c.muted === "number" ? { muted: c.muted } : {}) },
      feeds,
      folders: old.folders.map((fo) => ({ ...fo, unread: folderUnread.get(fo.id) ?? 0 })),
    };
  });
}

let countsRefetch: ReturnType<typeof setTimeout> | undefined;

export { SAVED_COUNTS_MIN_MS, refreshSavedSearchCounts, resetSavedSearchCounts };

/** Everything one event does: reducer, query cache, live region. */
export function handleServerEvent(qc: QueryClient, ev: ServerEvent): void {
  const runKind = ev.type === "run.done" ? liveStore.get().runs[String(ev.data.run_id)]?.kind : undefined;
  liveStore.set((s) => reduceEvent(s, ev));
  switch (ev.type) {
    case "items.state": {
      const { ids, read, starred, muted } = ev.data;
      const patch: ItemPatch = {};
      if (read !== undefined) patch.read = read;
      if (starred !== undefined) patch.starred = starred;
      if (muted === false) {
        patch.muted_by = null;
        patch.muted_by_name = null;
      }
      if (Object.keys(patch).length) patchItems(qc, ids, patch);
      if (muted === true) {
        // A mute leaves All and search (the item is read now); the Muted list loads it next time it is shown.
        dropFromLists(qc, ids, (k) => k.startsWith("all") || k.includes("|q:"));
        invalidateLists(qc, (k) => k.startsWith("muted"));
      } else if (muted === false) {
        // Un-muted: gone from Muted, and back in All, Unread and search the next time those are shown.
        dropFromLists(qc, ids, (k) => k.startsWith("muted"));
        invalidateLists(qc, (k) => !k.startsWith("muted"));
      }
      break;
    }
    case "filters.changed":
      void qc.invalidateQueries({ queryKey: filtersKey });
      void qc.invalidateQueries({ queryKey: keys.bootstrap });
      break;
    case "saved_searches.changed":
      invalidateSavedSearches(qc, { echo: true });
      break;
    case "counts": {
      refreshSavedSearchCounts(qc);
      // An event already in flight when the user opened an item carries the old numbers and would undo the
      // optimistic bump. Inside the window, skip it and refetch the truth once the window is over.
      const left = countsGuardLeft();
      if (left > 0) {
        if (!countsRefetch) {
          countsRefetch = setTimeout(() => {
            countsRefetch = undefined;
            void qc.invalidateQueries({ queryKey: keys.bootstrap });
          }, left + 50);
        }
      } else applyCounts(qc, ev.data);
      break;
    }
    case "folder.changed":
      // A folder was created, renamed, reordered or deleted (here or elsewhere). Deleting one drops it as the scope of
      // saved searches on the server, so those are refetched too; item lists are not (no full list invalidation).
      void qc.invalidateQueries({ queryKey: keys.bootstrap });
      invalidateSavedSearches(qc);
      break;
    case "feed.changed":
      void qc.invalidateQueries({ queryKey: keys.bootstrap });
      // Deleting a feed drops it as the scope of a saved search on the server, silently.
      invalidateSavedSearches(qc);
      break;
    case "fulltext.ready":
      for (const id of ev.data.ids) void qc.invalidateQueries({ queryKey: keys.item(id) });
      break;
    case "resync":
      // The server says it cannot replay what we missed: only then are the lists refetched.
      void qc.invalidateQueries({ queryKey: keys.bootstrap });
      void qc.invalidateQueries({ queryKey: keys.itemsAll });
      invalidateSavedSearches(qc);
      break;
    default:
      break;
  }
  const say = announcementFor(ev, runKind);
  if (say) announce(say);
}

export function parseServerEvent(type: string, data: string): ServerEvent | null {
  if (!(SERVER_EVENT_TYPES as readonly string[]).includes(type)) return null;
  try {
    return { type, data: JSON.parse(data) } as ServerEvent;
  } catch {
    return null;
  }
}

// ---- Fallback polling ---------------------------------------------------------

/** Every 2 s while a run is active, every 60 s otherwise (design 7.3). */
export function pollInterval(active: boolean): number {
  return active ? 2_000 : 60_000;
}

/**
 * Poll GET /api/status once and fold it into the cache and live state. The runs it reports replace the
 * live runs wholesale, so a run whose `run.done` was missed stops counting as active.
 */
export async function pollStatus(qc: QueryClient): Promise<StatusResponse> {
  const st = await api<StatusResponse>("/api/status");
  const runs: Record<string, RunStatus> = {};
  for (const r of st.runs ?? []) runs[String(r.id)] = { ...r, id: String(r.id) };
  const before = liveStore.get().runs;
  const finished = Object.keys(before).filter((id) => !runs[id]);
  liveStore.set((s) => ({ ...s, runs }));
  qc.setQueryData<Bootstrap>(keys.bootstrap, (old) =>
    old ? { ...old, counts: { ...old.counts, unread: st.unread_total, ...(typeof st.muted === "number" ? { muted: st.muted } : {}) } } : old,
  );
  if (finished.length) void qc.invalidateQueries({ queryKey: keys.bootstrap });
  return { ...st, runs: st.runs ?? [] };
}

/**
 * After a gap in the stream: reconcile runs and counts from the server instead of trusting events that
 * were never delivered. Lists are deliberately not refetched (a list is a snapshot; the "n new" pill and
 * the stale marker tell the user); only counts and the feed tree refresh.
 */
export async function reconcile(qc: QueryClient): Promise<void> {
  await pollStatus(qc);
  void qc.invalidateQueries({ queryKey: keys.bootstrap });
}

/**
 * Show a run the moment the server accepts it (the 202 body), without waiting for `run.start` (the stream can be down,
 * and polling is 60 s). Nothing is added for a run that already finished: its `run.done` may have come first.
 */
export function seedRun(run: RunStatus): void {
  liveStore.set((s) => (s.finished?.[run.id] || s.runs[run.id] ? s : { ...s, runs: { ...s.runs, [run.id]: run } }));
}

/** Seed live runs from a freshly loaded bootstrap when nothing is known yet (a run already going at load). */
export function seedRuns(runs: RunStatus[] | undefined): void {
  if (!runs?.length || Object.keys(liveStore.get().runs).length) return;
  liveStore.set((s) => ({ ...s, runs: Object.fromEntries(runs.map((r) => [String(r.id), { ...r, id: String(r.id) }])) }));
}

// ---- Hook ---------------------------------------------------------------------

/** A stream open at least this long counts as healthy when it later drops. */
const STABLE_MS = 10_000;

/** No event of any kind for this long on a server that sends heartbeats: the stream is hung. */
export const WATCHDOG_MS = 45_000;

/** Reconnect delay after the browser gave up on the stream: 1 s doubling to 30 s, with jitter. */
export function reconnectDelay(attempt: number, rand: () => number = Math.random): number {
  const base = Math.min(30_000, 1_000 * 2 ** Math.max(0, attempt));
  return Math.round(base * (0.75 + rand() * 0.5));
}

/**
 * Subscribes to /api/events. If EventSource errors twice without delivering a
 * message it falls back to polling /api/status (2 s during a run, 60 s
 * otherwise); a message or a reopened stream ends the fallback. The browser's
 * native retry is disabled (the source is closed on every error) so that
 * exactly one reconnect is scheduled per backoff interval, 1 s doubling to
 * 30 s, and runs and counts are reconciled from /api/status on reconnect.
 */
export function useServerEvents(enabled: boolean): void {
  const qc = useQueryClient();
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    let stopped = false;
    let errors = 0;
    let attempt = 0;
    let lost = false;
    let es: EventSource | null = null;
    let pollTimer: ReturnType<typeof setTimeout> | undefined;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;

    // Each poll loop owns a generation: a poll still awaiting when a newer loop starts (or polling stops)
    // does not reschedule, so two loops can never run at once.
    let pollGen = 0;
    const stopPolling = () => {
      pollGen++;
      if (pollTimer) clearTimeout(pollTimer);
      pollTimer = undefined;
    };
    const poll = async (gen?: number) => {
      if (stopped) return;
      if (gen === undefined) {
        stopPolling();
        gen = pollGen;
      } else if (gen !== pollGen) return;
      let active = false;
      try {
        const st = await pollStatus(qc);
        active = st.runs.length > 0;
      } catch {
        /* 401 flips authStore; a network error just tries again */
      }
      if (!stopped && gen === pollGen && liveStore.get().transport === "fallback") {
        const g = gen;
        pollTimer = setTimeout(() => void poll(g), pollInterval(active));
      }
    };
    let openedAt = 0;
    // Older servers send no heartbeat; the watchdog only arms once one has been seen.
    let heartbeatSeen = false;
    let watchdog: ReturnType<typeof setTimeout> | undefined;
    let fail: (src: EventSource) => void = () => undefined;
    const pet = (src: EventSource) => {
      if (watchdog) clearTimeout(watchdog);
      watchdog = undefined;
      if (heartbeatSeen && !stopped) watchdog = setTimeout(() => fail(src), WATCHDOG_MS);
    };
    let lastEventId = "";
    /** The stream is up: leave fallback polling and reconcile runs and counts if events were missed. */
    const connected = (proven = true) => {
      // Only a delivered message or heartbeat proves the stream healthy; a bare onopen does not (a proxy
      // that accepts then resets would otherwise never reach the polling fallback).
      if (proven) errors = 0;
      stopPolling();
      if (liveStore.get().transport !== "open") liveStore.set((s) => ({ ...s, transport: "open" }));
      if (lost) {
        lost = false;
        reconcile(qc).catch(() => {
          lost = true; // try again on the next message
        });
      }
    };
    // A run.done can be missed while a phone sleeps with the stream apparently open: look again on return.
    const onVisible = () => {
      if (document.visibilityState === "visible" && Object.keys(liveStore.get().runs).length > 0) void reconcile(qc).catch(() => undefined);
    };
    document.addEventListener("visibilitychange", onVisible);
    const stopSeed = qc.getQueryCache().subscribe((e) => {
      if (e.type === "updated" && e.action.type === "success" && e.query.queryKey[0] === "bootstrap") {
        seedRuns((e.query.state.data as Bootstrap | undefined)?.runs);
      }
    });

    const connect = () => {
      retryTimer = undefined;
      // A recreated EventSource never sends Last-Event-ID, so the last id we saw goes in the URL; a server
      // that ignores it is still fine (runs and counts are reconciled from /api/status either way).
      const src = new EventSource(lastEventId ? `/api/events?last_event_id=${encodeURIComponent(lastEventId)}` : "/api/events");
      es = src;
      src.addEventListener("heartbeat", () => {
        heartbeatSeen = true;
        attempt = 0;
        connected();
        pet(src);
      });
      const onMessage = (type: string) => (m: MessageEvent<string>) => {
        attempt = 0; // a delivered message proves the stream is healthy
        if (m.lastEventId) lastEventId = m.lastEventId;
        connected();
        pet(src);
        const ev = parseServerEvent(type, m.data);
        if (ev) handleServerEvent(qc, ev);
      };
      for (const t of SERVER_EVENT_TYPES) src.addEventListener(t, onMessage(t) as EventListener);
      src.onopen = () => {
        openedAt = Date.now();
        connected(false);
        pet(src);
      };
      fail = (s: EventSource) => {
        if (s === src) src.onerror?.(new Event("error"));
      };
      src.onerror = () => {
        if (watchdog) clearTimeout(watchdog);
        watchdog = undefined;
        // We own reconnection. The browser's own retry (the server's `retry: 3000`, every 3 s with
        // no backoff) would stack on top of ours, so close the source on EVERY error and schedule
        // exactly one attempt. A stream that stayed up a while before dropping restarts the backoff;
        // one that opened and died at once (a proxy that accepts then resets) does not.
        src.close();
        if (es !== src) return; // a late error from a source we already replaced
        errors += 1;
        if (authStore.get() === "out") return;
        if (openedAt && Date.now() - openedAt >= STABLE_MS) attempt = 0;
        openedAt = 0;
        lost = true;
        if (errors >= 2 && liveStore.get().transport !== "fallback") {
          liveStore.set((s) => ({ ...s, transport: "fallback" }));
          void poll();
        }
        if (!retryTimer && !stopped) retryTimer = setTimeout(connect, reconnectDelay(attempt++));
      };
    };
    connect();

    return () => {
      stopped = true;
      document.removeEventListener("visibilitychange", onVisible);
      stopSeed();
      stopPolling();
      if (retryTimer) clearTimeout(retryTimer);
      if (watchdog) clearTimeout(watchdog);
      es?.close();
      liveStore.set((s) => ({ ...s, transport: "connecting" }));
    };
  }, [enabled, qc]);
}
