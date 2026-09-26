import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
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

/**
 * The saved searches in display order. `counts` also loads the unread numbers (a second, slower request whose
 * failure or timeout leaves every count blank). Only screens that show counts ask for them.
 */
export function useSavedSearches(opts: { counts?: boolean } = {}): { searches: SavedSearch[]; loading: boolean; error: boolean; refetch: () => void } {
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
    retry: false,
  });
  const counted = new Map((withCounts.data ?? []).map((s) => [s.id, s]));
  const searches = (base.data ?? []).map((s) => {
    const c = counted.get(s.id);
    return c ? { ...s, unread: c.unread, unread_capped: c.unread_capped } : s;
  });
  return { searches, loading: base.isPending, error: base.isError, refetch: () => void base.refetch() };
}

/** Refetch the list and its counts (an event, or a write). */
export function invalidateSavedSearches(qc: QueryClient): void {
  void qc.invalidateQueries({ queryKey: savedSearchesKey });
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

/** Reorder with an optimistic list; puts the old order back on failure. Resolves to whether the server took it. */
export function useReorderSavedSearches() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) => reorderSavedSearches(ids),
    onMutate: async (ids) => {
      await qc.cancelQueries({ queryKey: savedSearchesKey });
      const prev = qc.getQueryData<SavedSearch[]>(savedSearchesKey);
      const order = new Map(ids.map((id, i) => [id, i]));
      const sort = (l: SavedSearch[] | undefined) => (l ? [...l].sort((a, b) => (order.get(a.id) ?? 0) - (order.get(b.id) ?? 0)) : l);
      qc.setQueryData<SavedSearch[]>(savedSearchesKey, sort);
      qc.setQueryData<SavedSearch[]>(savedSearchCountsKey, sort);
      return { prev };
    },
    onError: (_e, _ids, ctx) => {
      if (ctx?.prev) qc.setQueryData(savedSearchesKey, ctx.prev);
    },
    onSettled: () => invalidateSavedSearches(qc),
  });
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
