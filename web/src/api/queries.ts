import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
} from "@tanstack/react-query";
import { api, ApiError } from "./client";
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
import { toast } from "@/shell/toasts";
import { errorMessage } from "./client";

export const PAGE_SIZE = 50;

export const keys = {
  bootstrap: ["bootstrap"] as const,
  items: (scope: Scope) => ["items", scopeKey(scope)] as const,
  itemsAll: ["items"] as const,
  item: (id: string) => ["item", id] as const,
};

/** Stable string for a scope; also used in the article route's `from` param. */
export function scopeKey(s: Scope): string {
  const parts: string[] = [s.view];
  if (s.feed) parts.push(`feed:${s.feed}`);
  if (s.folder) parts.push(`folder:${s.folder}`);
  if (s.q) parts.push(`q:${encodeURIComponent(s.q)}`);
  if (s.order === "oldest" || s.order === "rank") parts.push(`order:${s.order}`);
  if (s.typing) parts.push("typing:1");
  return parts.join("|");
}

export function parseScopeKey(key: string | null | undefined): Scope {
  const scope: Scope = { view: "unread" };
  if (!key) return scope;
  for (const [i, part] of key.split("|").entries()) {
    if (i === 0) {
      if (part === "unread" || part === "all" || part === "starred" || part === "muted") scope.view = part;
      continue;
    }
    const idx = part.indexOf(":");
    const k = part.slice(0, idx);
    const v = part.slice(idx + 1);
    if (k === "feed") scope.feed = v;
    else if (k === "folder") scope.folder = v;
    else if (k === "q") scope.q = decodeURIComponent(v);
    else if (k === "order" && (v === "oldest" || v === "rank")) scope.order = v;
    else if (k === "typing" && v === "1") scope.typing = true;
  }
  return scope;
}

export function itemsParams(scope: Scope, cursor?: string, limit = PAGE_SIZE) {
  return {
    view: scope.view,
    feed: scope.feed,
    folder: scope.folder,
    q: scope.q,
    // Newest first is the server default, so it is not sent. The UI is embedded in the server binary,
    // so `order=oldest` is always understood (docs/design.md 7.1: cursor `a<sort_at>.<id>`).
    order: scope.order,
    // Only while the user is typing (docs/design.md 7.1): a submitted or saved search never sends it.
    typing: scope.typing && scope.q ? 1 : undefined,
    cursor,
    limit,
  };
}

export function useBootstrap(enabled = true) {
  return useQuery({
    queryKey: keys.bootstrap,
    queryFn: ({ signal }) => api<Bootstrap>("/api/bootstrap", { signal }),
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
    mutationFn: ({ id, via }: { id: string; via: "tap" | "key" | "nav" }) =>
      api<OpenResponse>(`/api/items/${id}/open`, { method: "POST", body: { via } }),
    onMutate: ({ id }) => {
      const cached = findCached(qc, id);
      if (!cached || cached.read) return { bumped: null as string | null };
      patchItems(qc, [id], { read: true });
      bumpUnread(qc, cached.feed_id, -1);
      return { bumped: cached.feed_id as string | null };
    },
    onError: (_e, { id }, ctx) => {
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
    mutationFn: ({ id, starred }: { id: string; starred: boolean }) =>
      api<{ starred: boolean; restored: boolean }>(`/api/items/${id}/star`, { method: "PUT", body: { starred } }),
    onMutate: ({ id, starred }) => {
      const prev = qc.getQueryData<ItemDetail>(keys.item(id))?.starred;
      patchItems(qc, [id], { starred });
      return { prev };
    },
    onError: (e, { id }, ctx) => {
      if (ctx?.prev !== undefined) patchItems(qc, [id], { starred: ctx.prev });
      toast(errorMessage(e), "error");
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

/** Mark ids read or unread with an optimistic patch; reverts and toasts on failure. */
export async function applyRead(
  qc: QueryClient,
  ids: string[],
  read: boolean,
  reason: "swipe" | "key" | "scroll" | "bulk",
): Promise<MarkReadResponse | undefined> {
  patchItems(qc, ids, { read });
  try {
    return await api<MarkReadResponse>("/api/items/mark-read", { method: "POST", body: { ids, read, reason } });
  } catch (e) {
    patchItems(qc, ids, { read: !read });
    toast(errorMessage(e), "error");
    return undefined;
  }
}

/** Set starred with an optimistic patch; reverts and toasts on failure. */
export async function applyStar(qc: QueryClient, id: string, starred: boolean): Promise<boolean> {
  patchItems(qc, [id], { starred: starred });
  try {
    await api(`/api/items/${id}/star`, { method: "PUT", body: { starred } });
    return true;
  } catch (e) {
    patchItems(qc, [id], { starred: !starred });
    toast(errorMessage(e), "error");
    return false;
  }
}
