import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
} from "@tanstack/react-query";
import { api, ApiError, errorMessage } from "./client";
import type {
  Bootstrap,
  Card,
  FulltextResponse,
  ItemDetail,
  ItemsPage,
  MarkReadResponse,
  OpenResponse,
  Scope,
} from "./types";
import { isOffline, queueRead, queueStar, QueueWriteError, supersede } from "@/lib/offline";
import { toast } from "@/shell/toasts";
import { itemsParams, keys } from "./queryKeys";

export { PAGE_SIZE, keys, scopeKey, parseScopeKey, itemsParams } from "./queryKeys";

/** The toast for a failed change: one made offline that could not be stored says so, not "the server". */
export function changeError(e: unknown): string {
  return e instanceof QueueWriteError ? "Kipple couldn't save that change on this device." : errorMessage(e);
}

/**
 * The bootstrap as the app holds it. `fromCache`: the service worker answered with its stored copy (offline, or the
 * network too slow), so it may be older than what this device already changed; the device settings are not taken
 * from it (App.tsx).
 */
export type BootstrapAnswer = Bootstrap & { fromCache?: true };

export function useBootstrap(enabled = true) {
  return useQuery({
    queryKey: keys.bootstrap,
    queryFn: async ({ signal }): Promise<BootstrapAnswer> => {
      const meta: { cached?: boolean } = {};
      const b = await api<Bootstrap>("/api/bootstrap", { signal, meta });
      return meta.cached ? { ...b, fromCache: true } : b;
    },
    enabled,
    retry: (n, e) => (e as { status?: number }).status !== 401 && n < 2,
    staleTime: 60_000,
  });
}

/** Cursor-paged item list (design 7.1: keyset cursor, limit at most 100). */
export function useItems(scope: Scope, enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.items(scope),
    queryFn: ({ pageParam, signal }) =>
      api<ItemsPage>("/api/items", { params: itemsParams(scope, pageParam || undefined), signal }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled,
    // A search the server refuses (422 too broad) or a cursor it no longer takes (400) will not get better by asking again.
    retry: (n, e) => !(e instanceof ApiError && (e.status === 422 || e.status === 400 || e.status === 401 || e.status === 404)) && n < 2,
    // A list is a snapshot: rows read in place stay visible (dimmed) until the
    // user refreshes or leaves. SSE never refetches it behind the user's back.
    staleTime: Infinity,
    gcTime: 30 * 60_000,
  });
}

export function useItem(id: string | undefined) {
  return useQuery({
    queryKey: keys.item(id ?? ""),
    queryFn: ({ signal }) => api<ItemDetail>(`/api/items/${id}`, { signal }),
    enabled: !!id,
    staleTime: 5 * 60_000,
    // Always ask, even when the browser says it is offline: the service worker may hold this article (saved for
    // offline), and when it does not the request fails at once and the article shows its error screen. Under the
    // default ("online") the query would sit paused, a loading skeleton with no end.
    networkMode: "always",
  });
}

export function flattenItems(data: InfiniteData<ItemsPage> | undefined): Card[] {
  return data?.pages.flatMap((p) => p.items) ?? [];
}

export type ItemPatch = Partial<Pick<Card, "read" | "starred" | "muted_by" | "muted_by_name">>;

/** Take these ids out of the cached lists whose scope key passes `match` (a Muted list after a restore, All after a mute). */
export function dropFromLists(qc: QueryClient, ids: string[], match: (scopeKey: string) => boolean): void {
  const set = new Set(ids);
  qc.setQueriesData<InfiniteData<ItemsPage>>({ queryKey: keys.itemsAll, predicate: (q) => match(String(q.queryKey[1])) }, (old) => {
    if (!old || !old.pages.some((p) => p.items.some((i) => set.has(i.id)))) return old;
    return { ...old, pages: old.pages.map((p) => ({ ...p, items: p.items.filter((i) => !set.has(i.id)) })) };
  });
}

/** Mark the cached lists whose scope key passes `match` stale without refetching: they reload when next shown. */
export function invalidateLists(qc: QueryClient, match: (scopeKey: string) => boolean): void {
  void qc.invalidateQueries({ queryKey: keys.itemsAll, predicate: (q) => match(String(q.queryKey[1])), refetchType: "none" });
}

/** Apply a state patch to every cached list and detail that holds these ids. */
export function patchItems(qc: QueryClient, ids: string[], patch: ItemPatch): void {
  const set = new Set(ids);
  qc.setQueriesData<InfiniteData<ItemsPage>>({ queryKey: keys.itemsAll }, (old) => {
    if (!old) return old;
    let touched = false;
    const pages = old.pages.map((p) => {
      if (!p.items.some((i) => set.has(i.id))) return p;
      touched = true;
      return { ...p, items: p.items.map((i) => (set.has(i.id) ? { ...i, ...patch } : i)) };
    });
    return touched ? { ...old, pages } : old;
  });
  for (const id of ids) {
    qc.setQueryData<ItemDetail>(keys.item(id), (old) => (old ? { ...old, ...patch } : old));
  }
}

/** When the last optimistic count change was made (ms); `counts` events older than it are stale. */
let lastBumpAt = 0;
export const COUNTS_GUARD_MS = 1500;
/** Ms left in the window after a local count change, 0 when none. */
export function countsGuardLeft(now = Date.now()): number {
  return Math.max(0, lastBumpAt + COUNTS_GUARD_MS - now);
}
export function resetCountsGuard(): void {
  lastBumpAt = 0;
}

/** Adjust unread counts locally (total, feed and its folder) so badges move before the counts event lands. */
export function bumpUnread(qc: QueryClient, feedId: string, delta: number): void {
  lastBumpAt = Date.now();
  qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => {
    if (!old) return old;
    const feed = old.feeds.find((f) => f.id === feedId);
    return {
      ...old,
      counts: { ...old.counts, unread: Math.max(0, old.counts.unread + delta) },
      feeds: old.feeds.map((f) => (f.id === feedId ? { ...f, unread: Math.max(0, f.unread + delta) } : f)),
      folders: old.folders.map((fo) =>
        feed && fo.id === feed.folder_id ? { ...fo, unread: Math.max(0, fo.unread + delta) } : fo,
      ),
    };
  });
}

/** Adjust the muted count locally (a restore) before the `counts` event lands. */
export function bumpMuted(qc: QueryClient, delta: number): void {
  qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => (old ? { ...old, counts: { ...old.counts, muted: Math.max(0, (old.counts.muted ?? 0) + delta) } } : old));
}

/** The cached card or detail for an id, wherever it is cached. */
export function findCached(qc: QueryClient, id: string): Pick<Card, "feed_id" | "read"> | undefined {
  const d = qc.getQueryData<ItemDetail>(keys.item(id));
  if (d) return d;
  for (const [, data] of qc.getQueriesData<InfiniteData<ItemsPage>>({ queryKey: keys.itemsAll })) {
    for (const p of data?.pages ?? []) {
      const c = p.items.find((i) => i.id === id);
      if (c) return c;
    }
  }
  return undefined;
}

/**
 * Opening marks the item read. The badge moves optimistically in onMutate, before the request, so a
 * server `counts` event that arrives ahead of the response (absolute numbers) simply overwrites it
 * and nothing is applied twice; onSuccess never bumps. A failure puts everything back.
 */
export function useOpenItem() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, via }: { id: string; via: "tap" | "key" | "nav" }) => {
      await supersede({ read: [id] });
      try {
        return await api<OpenResponse>(`/api/items/${id}/open`, { method: "POST", body: { via } });
      } catch (e) {
        // Offline with the article on the device: it reads, and the read is sent when the network is back.
        const held = qc.getQueryData<ItemDetail>(keys.item(id));
        if (!isOffline(e) || !held) throw e;
        await queueRead([id], true);
        return { session_key: "", item: { ...held, read: true } } satisfies OpenResponse;
      }
    },
    onMutate: ({ id }) => {
      const cached = findCached(qc, id);
      if (!cached || cached.read) return { bumped: null as string | null };
      patchItems(qc, [id], { read: true });
      bumpUnread(qc, cached.feed_id, -1);
      return { bumped: cached.feed_id as string | null };
    },
    onError: (e, { id }, ctx) => {
      // The article itself is still on screen (it is held on the device); only the read could not be kept.
      if (e instanceof QueueWriteError) toast(changeError(e), "error");
      if (!ctx?.bumped) return;
      patchItems(qc, [id], { read: false });
      bumpUnread(qc, ctx.bumped, 1);
    },
    onSuccess: (res, { id }) => {
      qc.setQueryData(keys.item(id), (old: ItemDetail | undefined) => ({ ...(old ?? res.item), ...res.item }));
      patchItems(qc, [id], { read: true });
    },
  });
}

export function useToggleStar() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, starred }: { id: string; starred: boolean }) => {
      await supersede({ star: id });
      try {
        return await api<{ starred: boolean; restored: boolean }>(`/api/items/${id}/star`, { method: "PUT", body: { starred } });
      } catch (e) {
        if (!isOffline(e)) throw e;
        await queueStar(id, starred);
        return { starred, restored: false };
      }
    },
    onMutate: ({ id, starred }) => {
      const prev = qc.getQueryData<ItemDetail>(keys.item(id))?.starred;
      patchItems(qc, [id], { starred });
      return { prev };
    },
    onError: (e, { id }, ctx) => {
      if (ctx?.prev !== undefined) patchItems(qc, [id], { starred: ctx.prev });
      toast(changeError(e), "error");
    },
  });
}

export function useFulltext() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, mode, refresh }: { id: string; mode?: 0 | 1 | null; refresh?: boolean }) =>
      api<FulltextResponse>(`/api/items/${id}/fulltext`, {
        method: "POST",
        body: mode === undefined ? {} : { mode },
        params: refresh ? { refresh: 1 } : undefined,
      }),
    onSuccess: (res, { id }) => {
      if (res.status === "ok" && res.effective === 1 && res.content_html != null) {
        qc.setQueryData<ItemDetail>(keys.item(id), (old) =>
          old
            ? {
                ...old,
                content_html: res.content_html as string,
                fulltext: { mode: res.mode, effective: 1, available: true, error: null },
              }
            : old,
        );
      } else {
        // Feed version (mode 0), skipped or failed: let the server say what to show.
        void qc.invalidateQueries({ queryKey: keys.item(id) });
      }
    },
    onError: (e) => toast(errorMessage(e), "error"),
  });
}

/**
 * Mark ids read or unread with an optimistic patch; reverts and toasts on failure. `onError` replaces the toast
 * for a caller that decides itself what to say (mark-read-on-scroll tells a streak of failures once).
 */
export async function applyRead(
  qc: QueryClient,
  ids: string[],
  read: boolean,
  reason: "swipe" | "key" | "scroll" | "bulk",
  opts: { onError?: (e: unknown) => void } = {},
): Promise<MarkReadResponse | undefined> {
  patchItems(qc, ids, { read });
  await supersede({ read: ids });
  try {
    try {
      return await api<MarkReadResponse>("/api/items/mark-read", { method: "POST", body: { ids, read, reason } });
    } catch (e) {
      // No network: keep the change on screen and send it when the connection returns (lib/offline.ts). A
      // change that could not be stored for later is a failure like any other.
      if (isOffline(e)) return await queueRead(ids, read);
      throw e;
    }
  } catch (e) {
    patchItems(qc, ids, { read: !read });
    if (opts.onError) opts.onError(e);
    else toast(changeError(e), "error");
    return undefined;
  }
}

/** Set starred with an optimistic patch; reverts and toasts on failure. */
export async function applyStar(qc: QueryClient, id: string, starred: boolean): Promise<boolean> {
  patchItems(qc, [id], { starred: starred });
  await supersede({ star: id });
  try {
    try {
      await api(`/api/items/${id}/star`, { method: "PUT", body: { starred } });
    } catch (e) {
      if (!isOffline(e)) throw e;
      await queueStar(id, starred);
    }
    return true;
  } catch (e) {
    patchItems(qc, [id], { starred: !starred });
    toast(changeError(e), "error");
    return false;
  }
}
