import { useEffect } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import { api, authStore } from "./client";
import { keys, patchItems } from "./queries";
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
  /** Active fetch runs by run id. */
  runs: Record<string, RunStatus>;
  /** Increments on `resync`; screens refetch when it changes. */
  resyncTick: number;
  /** Ids whose background full-text extraction finished. */
  fulltextReady: string[];
  /** Increments when `feed.changed` says the feed list is stale. */
  feedsStaleTick: number;
  transport: "connecting" | "open" | "fallback";
}

export const initialLive: LiveState = {
  pendingByFeed: {},
  runs: {},
  resyncTick: 0,
  fulltextReady: [],
  feedsStaleTick: 0,
  transport: "connecting",
};

export const liveStore = createStore<LiveState>(initialLive);

/** Pure reducer for the parts of an SSE event that live outside the query cache. */
export function reduceEvent(s: LiveState, ev: ServerEvent): LiveState {
  switch (ev.type) {
    case "run.start":
      return {
        ...s,
        runs: {
          ...s.runs,
          [ev.data.run_id]: { id: ev.data.run_id, kind: ev.data.kind, done: 0, total: ev.data.total, new_items: 0, errors: 0 },
        },
      };
    case "run.progress": {
      const cur = s.runs[ev.data.run_id];
      return {
        ...s,
        runs: {
          ...s.runs,
          [ev.data.run_id]: { id: ev.data.run_id, kind: cur?.kind ?? "refresh", ...ev.data },
        },
      };
    }
    case "run.done": {
      const { [ev.data.run_id]: _done, ...rest } = s.runs;
      void _done;
      return { ...s, runs: rest };
    }
    case "fetch.done":
      return ev.data.new_items > 0
        ? { ...s, pendingByFeed: { ...s.pendingByFeed, [ev.data.feed_id]: (s.pendingByFeed[ev.data.feed_id] ?? 0) + ev.data.new_items } }
        : s;
    case "fulltext.ready":
      return { ...s, fulltextReady: [...s.fulltextReady, ...ev.data.ids].slice(-500) };
    case "feed.changed":
      return { ...s, feedsStaleTick: s.feedsStaleTick + 1 };
    case "resync":
      return { ...s, resyncTick: s.resyncTick + 1, pendingByFeed: {} };
    case "items.state":
    case "counts":
      return s; // cache-only events
  }
}

/**
 * New items that would appear in this list: the feeds the scope includes (a feed, a folder's feeds,
 * or all). Starred and search lists never show a pill (their membership is not "new arrivals"),
 * and an oldest-first list gets new items at its far end, so a jump-to-top pill would mislead.
 */
export function pendingFor(
  pending: Record<string, number>,
  scope: { view: string; feed?: string; folder?: string; q?: string; order?: string },
  feeds: readonly { id: string; folder_id: string }[],
): number {
  if (scope.view === "starred" || scope.q || scope.order === "oldest") return 0;
  let n = 0;
  if (scope.feed) return pending[scope.feed] ?? 0;
  if (scope.folder) {
    for (const f of feeds) if (f.folder_id === scope.folder) n += pending[f.id] ?? 0;
    return n;
  }
  for (const v of Object.values(pending)) n += v;
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
const omit = (o: Record<string, number>, ids: string[]) => Object.fromEntries(Object.entries(o).filter(([k]) => !ids.includes(k)));

/** Text for the polite live region, or null when the event is not announced. */
export function announcementFor(ev: ServerEvent): string | null {
  const plural = (n: number) => `${n} new article${n === 1 ? "" : "s"}`;
  if (ev.type === "run.done") {
    if (ev.data.errors > 0) return `Couldn't refresh ${ev.data.errors} feed${ev.data.errors === 1 ? "" : "s"}. The rest updated.`;
    return ev.data.new_items > 0 ? plural(ev.data.new_items) : "No new articles";
  }
  // A scheduled fetch outside any run: announce the new items.
  if (ev.type === "fetch.done" && ev.data.new_items > 0 && !(ev.data.run_ids && ev.data.run_ids.length)) {
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
      counts: { ...old.counts, unread: c.unread_total },
      feeds,
      folders: old.folders.map((fo) => ({ ...fo, unread: folderUnread.get(fo.id) ?? 0 })),
    };
  });
}

/** Everything one event does: reducer, query cache, live region. */
export function handleServerEvent(qc: QueryClient, ev: ServerEvent): void {
  liveStore.set((s) => reduceEvent(s, ev));
  switch (ev.type) {
    case "items.state": {
      const { ids, read, starred } = ev.data;
      const patch: { read?: boolean; starred?: boolean } = {};
      if (read !== undefined) patch.read = read;
      if (starred !== undefined) patch.starred = starred;
      if (Object.keys(patch).length) patchItems(qc, ids, patch);
      break;
    }
    case "counts":
      applyCounts(qc, ev.data);
      break;
    case "feed.changed":
      void qc.invalidateQueries({ queryKey: keys.bootstrap });
      break;
    case "fulltext.ready":
      for (const id of ev.data.ids) void qc.invalidateQueries({ queryKey: keys.item(id) });
      break;
    case "resync":
      void qc.invalidateQueries({ queryKey: keys.bootstrap });
      void qc.invalidateQueries({ queryKey: keys.itemsAll });
      break;
    default:
      break;
  }
  const say = announcementFor(ev);
  if (say) announce(say);
}

export function parseServerEvent(type: string, data: string): ServerEvent | null {
  if (!(SERVER_EVENT_TYPES as string[]).includes(type)) return null;
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

/** Poll GET /api/status once and fold it into the cache and live state. */
export async function pollStatus(qc: QueryClient): Promise<StatusResponse> {
  const st = await api<StatusResponse>("/api/status");
  const runs: Record<string, RunStatus> = {};
  for (const r of st.runs ?? []) runs[r.id] = r;
  const before = liveStore.get().runs;
  const finished = Object.keys(before).filter((id) => !runs[id]);
  liveStore.set((s) => ({ ...s, runs }));
  qc.setQueryData<Bootstrap>(keys.bootstrap, (old) =>
    old ? { ...old, counts: { ...old.counts, unread: st.unread_total } } : old,
  );
  if (finished.length) void qc.invalidateQueries({ queryKey: keys.bootstrap });
  return { ...st, runs: st.runs ?? [] };
}

// ---- Hook ---------------------------------------------------------------------

/** Reconnect delay after the browser gave up on the stream: 1 s doubling to 30 s, with jitter. */
export function reconnectDelay(attempt: number, rand: () => number = Math.random): number {
  const base = Math.min(30_000, 1_000 * 2 ** Math.max(0, attempt));
  return Math.round(base * (0.75 + rand() * 0.5));
}

/**
 * Subscribes to /api/events. If EventSource errors twice without delivering a
 * message it falls back to polling /api/status (2 s during a run, 60 s
 * otherwise); a message or a reopened stream ends the fallback. A browser
 * EventSource only retries by itself while the connection dropped; after a
 * non-200 answer (a proxy 502 during a deploy) it is CLOSED for good, so the
 * hook recreates it with capped exponential backoff and resyncs on reconnect.
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

    const stopPolling = () => {
      if (pollTimer) clearTimeout(pollTimer);
      pollTimer = undefined;
    };
    const poll = async () => {
      if (stopped) return;
      let active = false;
      try {
        const st = await pollStatus(qc);
        active = st.runs.length > 0;
      } catch {
        /* 401 flips authStore; a network error just tries again */
      }
      if (!stopped && liveStore.get().transport === "fallback") pollTimer = setTimeout(poll, pollInterval(active));
    };
    const connected = () => {
      errors = 0;
      attempt = 0;
      stopPolling();
      if (liveStore.get().transport !== "open") liveStore.set((s) => ({ ...s, transport: "open" }));
      if (lost) {
        // Events were missed while disconnected: refetch counts, feeds and the visible lists.
        lost = false;
        handleServerEvent(qc, { type: "resync", data: {} } as ServerEvent);
      }
    };

    const connect = () => {
      retryTimer = undefined;
      const src = new EventSource("/api/events");
      es = src;
      const onMessage = (type: string) => (m: MessageEvent<string>) => {
        connected();
        const ev = parseServerEvent(type, m.data);
        if (ev) handleServerEvent(qc, ev);
      };
      for (const t of SERVER_EVENT_TYPES) src.addEventListener(t, onMessage(t) as EventListener);
      src.onopen = () => connected();
      src.onerror = () => {
        errors += 1;
        if (authStore.get() === "out") return;
        lost = true;
        if (errors >= 2 && liveStore.get().transport !== "fallback") {
          liveStore.set((s) => ({ ...s, transport: "fallback" }));
          void poll();
        }
        if (src.readyState === EventSource.CLOSED) {
          src.close();
          if (!retryTimer && !stopped) retryTimer = setTimeout(connect, reconnectDelay(attempt++));
          // A stream that closed before ever erroring twice still needs the fallback.
          if (liveStore.get().transport !== "fallback") {
            liveStore.set((s) => ({ ...s, transport: "fallback" }));
            void poll();
          }
        }
      };
    };
    connect();

    return () => {
      stopped = true;
      stopPolling();
      if (retryTimer) clearTimeout(retryTimer);
      es?.close();
      liveStore.set((s) => ({ ...s, transport: "connecting" }));
    };
  }, [enabled, qc]);
}
