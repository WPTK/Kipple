import { useCallback, useRef } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { SavedSearch, Scope } from "./types";

// Saved searches (docs/design.md 7.1d). The list is fetched twice on purpose: `?counts=0` paints at once, then the
// counts (a server-side search per entry, up to 999 each) arrive without holding the sidebar up. The counts are lazy
// and refreshed by `counts` events at most every 10 s (api/events.ts).

export const savedSearchesKey = ["saved-searches"] as const;
export const savedSearchCountsKey = ["saved-searches", "counts"] as const;

/** The most a sidebar entry shows. */
export const CAP_LABEL = "999+";

const wrap = (r: { saved_searches: SavedSearch[] }): SavedSearch[] => r.saved_searches;

/** Retry policy of the counts request: a slow server search should not leave the sidebar blank for a minute. */
export const COUNTS_RETRY = { attempts: 2, baseMs: 1000 };

/**
 * The saved searches in display order. `counts` also loads the unread numbers (a second, slower request). A row has
 * `unread` only once that request answered: absent means "not loaded" (nothing is drawn), null means the server tried
 * and ran out of time (a dash). The base list (`?counts=0`) always carries `unread: null` on the wire, so it is
 * stripped here; otherwise every entry would look timed out until the counts arrived, or for good if they never do.
 */
export function useSavedSearches(opts: { counts?: boolean } = {}): {
  searches: SavedSearch[];
  loading: boolean;
  error: boolean;
  countsError: boolean;
  refetch: () => void;
  refetchCounts: () => void;
} {
  const base = useQuery({
    queryKey: savedSearchesKey,
    queryFn: ({ signal }) => api<{ saved_searches: SavedSearch[] }>("/api/saved-searches", { params: { counts: 0 }, signal }).then(wrap),
    staleTime: 60_000,
  });
  const withCounts = useQuery({
    queryKey: savedSearchCountsKey,
    queryFn: ({ signal }) => api<{ saved_searches: SavedSearch[] }>("/api/saved-searches", { signal }).then(wrap),
    enabled: opts.counts === true && base.isSuccess,
    staleTime: 60_000,
    retry: COUNTS_RETRY.attempts,
    retryDelay: (n) => COUNTS_RETRY.baseMs * 2 ** n,
  });
  const counted = new Map((withCounts.data ?? []).map((s) => [s.id, s]));
  const searches = (base.data ?? []).map((s) => {
    const { unread: _u, unread_capped: _c, ...rest } = s;
    void _u;
    void _c;
    const c = counted.get(s.id);
    return c ? { ...rest, unread: c.unread ?? null, unread_capped: c.unread_capped } : rest;
  });
  return {
    searches,
    loading: base.isPending,
    error: base.isError,
    countsError: withCounts.isError && !withCounts.data,
    refetch: () => void base.refetch(),
    refetchCounts: () => void withCounts.refetch(),
  };
}

/** A saved-search write of this tab happened just now: its `saved_searches.changed` echo needs no second refetch. */
export const ECHO_WINDOW_MS = 1000;
let lastLocalWrite = -Infinity;
/** Tests: forget the echo window. */
export function resetSavedSearchEcho(): void {
  lastLocalWrite = -Infinity;
}

/**
 * Refetch the list after a change. Only the base list is invalidated (exact); the counts, a server search per entry,
 * go through the throttle in `refreshSavedSearchCounts` so a burst of events cannot keep restarting that request.
 * `echo` marks the `saved_searches.changed` event: dropped when it is the echo of a write this tab just made.
 */
export function invalidateSavedSearches(qc: QueryClient, opts: { echo?: boolean } = {}): void {
  const now = Date.now();
  if (opts.echo) {
    if (now - lastLocalWrite < ECHO_WINDOW_MS) return;
  } else lastLocalWrite = now;
  void qc.invalidateQueries({ queryKey: savedSearchesKey, exact: true });
  refreshSavedSearchCounts(qc);
}

/** The saved searches' unread counts are a search each: refreshed by `counts` events and changes at most every 10 s. */
export const SAVED_COUNTS_MIN_MS = 10_000;
let savedCountsAt = 0;
let savedCountsTimer: ReturnType<typeof setTimeout> | undefined;
export function refreshSavedSearchCounts(qc: QueryClient, now = Date.now()): void {
  const wait = savedCountsAt + SAVED_COUNTS_MIN_MS - now;
  const run = () => {
    savedCountsAt = Date.now();
    void qc.invalidateQueries({ queryKey: savedSearchCountsKey, exact: true });
  };
  if (wait <= 0) run();
  else if (!savedCountsTimer) {
    savedCountsTimer = setTimeout(() => {
      savedCountsTimer = undefined;
      run();
    }, wait);
  }
}
/** Tests: forget the throttle. */
export function resetSavedSearchCounts(): void {
  savedCountsAt = 0;
  if (savedCountsTimer) clearTimeout(savedCountsTimer);
  savedCountsTimer = undefined;
  resetSavedSearchEcho();
}

/** The unread label of one entry: "999+", the number, or null when there is nothing to show (not loaded, or timed out). */
export function unreadLabel(s: Pick<SavedSearch, "unread" | "unread_capped">): string | null {
  if (s.unread === undefined || s.unread === null) return null;
  if (s.unread_capped) return CAP_LABEL;
  return String(s.unread);
}

export interface SavedSearchInput {
  name: string;
  q: string;
  scope?: SavedSearch["scope"] | null;
  order?: SavedSearch["order"] | null;
}

export const createSavedSearch = (body: SavedSearchInput) => api<SavedSearch>("/api/saved-searches", { method: "POST", body });
export const patchSavedSearch = (id: string, body: Partial<SavedSearchInput>) =>
  api<SavedSearch>(`/api/saved-searches/${encodeURIComponent(id)}`, { method: "PATCH", body });
export const deleteSavedSearch = (id: string) => api(`/api/saved-searches/${encodeURIComponent(id)}`, { method: "DELETE" });
export const reorderSavedSearches = (ids: string[]) => api<{ saved_searches: SavedSearch[] }>("/api/saved-searches/reorder", { method: "POST", body: { ids } });

/**
 * Reorder with an optimistic list. Moves are applied to the cache at once and sent one at a time: while a request is
 * in flight later moves only replace the order to send next (the last one wins), so holding an arrow key or double
 * clicking never sends a second reorder built from a stale list. A failure refetches the server's order rather than
 * restoring a snapshot that would wipe a newer optimistic move.
 */
export function useReorderSavedSearches() {
  const qc = useQueryClient();
  const state = useRef<{ inflight: boolean; next: string[] | null; onError?: (e: unknown) => void }>({ inflight: false, next: null });
  return useCallback(
    (ids: string[], onError?: (e: unknown) => void) => {
      const st = state.current;
      const order = new Map(ids.map((id, i) => [id, i]));
      const sort = (l: SavedSearch[] | undefined) => (l ? [...l].sort((a, b) => (order.get(a.id) ?? 0) - (order.get(b.id) ?? 0)) : l);
      void qc.cancelQueries({ queryKey: savedSearchesKey, exact: true });
      qc.setQueryData<SavedSearch[]>(savedSearchesKey, sort);
      qc.setQueryData<SavedSearch[]>(savedSearchCountsKey, sort);
      if (onError) st.onError = onError;
      st.next = ids;
      if (st.inflight) return;
      const pump = (): void => {
        const sending = st.next;
        st.next = null;
        if (!sending) {
          st.inflight = false;
          invalidateSavedSearches(qc);
          return;
        }
        st.inflight = true;
        reorderSavedSearches(sending).then(pump, (e: unknown) => {
          st.onError?.(e);
          if (st.next) return pump();
          st.inflight = false;
          invalidateSavedSearches(qc);
        });
      };
      pump();
    },
    [qc],
  );
}

// ---- running one -----------------------------------------------------------------------------

/**
 * The route of the Search screen for a saved search: its text, scope and order. A saved search always runs as a
 * submitted search (no `typing`). The `ss` param remembers which entry it is, so "Save this search" can offer to
 * update it and the sidebar can mark it current.
 */
export function searchRoute(s: Pick<SavedSearch, "id" | "q" | "scope" | "order">): string {
  const sp = new URLSearchParams({ q: s.q });
  if (s.scope?.feed_id) sp.set("feed", s.scope.feed_id);
  else if (s.scope?.folder_id) sp.set("folder", s.scope.folder_id);
  else if (s.scope?.view) sp.set("view", s.scope.view);
  if (s.order) sp.set("order", s.order);
  sp.set("ss", s.id);
  return `/search?${sp.toString()}`;
}

/** The scope and order a Search-screen URL describes (the inverse of `searchRoute`). Order `date` is the newest-first default. */
export function scopeFromSearchParams(sp: URLSearchParams, defaultOrder: "rank" | "date" | "oldest"): { scope: Pick<Scope, "view" | "feed" | "folder">; order: "rank" | "date" | "oldest" } {
  const feed = sp.get("feed") ?? undefined;
  const folder = sp.get("folder") ?? undefined;
  const v = sp.get("view");
  const view = v === "unread" || v === "starred" || v === "all" ? v : "all";
  const o = sp.get("order");
  const order = o === "rank" || o === "date" || o === "oldest" ? o : defaultOrder;
  return { scope: feed ? { view, feed } : folder ? { view, folder } : { view }, order };
}

/** A list scope's saved-search form (what "Save this search" stores): one of a feed, a folder or a view, omitted for the whole library. */
export function savedScopeOf(s: Pick<Scope, "view" | "feed" | "folder">): SavedSearch["scope"] | undefined {
  if (s.feed) return { feed_id: s.feed };
  if (s.folder) return { folder_id: s.folder };
  if (s.view === "all" || s.view === "unread" || s.view === "starred") return s.view === "all" ? undefined : { view: s.view };
  return undefined;
}
