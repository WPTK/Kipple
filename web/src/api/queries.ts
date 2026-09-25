import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
} from "@tanstack/react-query";
import { api } from "./client";
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
  return parts.join("|");
}

export function parseScopeKey(key: string | null | undefined): Scope {
  const scope: Scope = { view: "unread" };
  if (!key) return scope;
  for (const [i, part] of key.split("|").entries()) {
    if (i === 0) {
      if (part === "unread" || part === "all" || part === "starred") scope.view = part;
      continue;
    }
    const idx = part.indexOf(":");
    const k = part.slice(0, idx);
    const v = part.slice(idx + 1);
    if (k === "feed") scope.feed = v;
    else if (k === "folder") scope.folder = v;
    else if (k === "q") scope.q = decodeURIComponent(v);
  }
  return scope;
}

export function itemsParams(scope: Scope, cursor?: string, limit = PAGE_SIZE) {
  return {
    view: scope.view,
    feed: scope.feed,
    folder: scope.folder,
    q: scope.q,
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

type Patch = Partial<Pick<Card, "read" | "starred">>;

/** Apply a state patch to every cached list and detail that holds these ids. */
export function patchItems(qc: QueryClient, ids: string[], patch: Patch): void {
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

/** Adjust unread counts locally so badges move before the counts event lands. */
export function bumpUnread(qc: QueryClient, feedId: string, delta: number): void {
  qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => {
    if (!old) return old;
    return {
      ...old,
      counts: { ...old.counts, unread: Math.max(0, old.counts.unread + delta) },
      feeds: old.feeds.map((f) => (f.id === feedId ? { ...f, unread: Math.max(0, f.unread + delta) } : f)),
    };
  });
}

export function useOpenItem() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, via }: { id: string; via: "tap" | "key" | "nav" }) =>
      api<OpenResponse>(`/api/items/${id}/open`, { method: "POST", body: { via } }),
    onSuccess: (res, { id }) => {
      const before = qc.getQueryData<ItemDetail>(keys.item(id));
      qc.setQueryData(keys.item(id), (old: ItemDetail | undefined) => ({ ...(old ?? res.item), ...res.item }));
      patchItems(qc, [id], { read: true });
      if (before && !before.read) bumpUnread(qc, before.feed_id, -1);
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

export function useMarkRead() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ ids, read, reason }: { ids: string[]; read: boolean; reason: "swipe" | "key" | "scroll" | "bulk" }) =>
      api<MarkReadResponse>("/api/items/mark-read", { method: "POST", body: { ids, read, reason } }),
    onMutate: ({ ids, read }) => {
      patchItems(qc, ids, { read });
    },
    onError: (e, { ids, read }) => {
      patchItems(qc, ids, { read: !read });
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
