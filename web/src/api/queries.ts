import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
} from "@tanstack/react-query";
import { api, errorMessage, signingOutStore } from "./client";
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
import {
  countsForAnswer,
  isOffline,
  overlayPending,
  queueRead,
  queueStar,
  QueueWriteError,
  retryRead,
  supersede,
} from "@/lib/offline";
import { serverRebuilt } from "@/lib/buildInfo";
import { useStore } from "@/lib/store";
import { chainOf, folderTree, type FolderTree } from "@/lib/folderTree";
import { useMemo } from "react";
import { setUpdateReady } from "@/lib/offlineState";
import { toast } from "@/shell/toasts";
import { PAGE_SIZE, itemsParams, keys, patchItems, undoKept } from "./queryKeys";

export { PAGE_SIZE, SECTION_SUFFIX, keys, scopeKey, parseScopeKey, itemsParams, patchItems, dropFromLists, invalidateLists, undoKept, type ItemPatch } from "./queryKeys";

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

/**
 * The bootstrap. It is never asked for while a sign-out you chose is under way, whoever holds it: the sign-out clears
 * the cache, and a holder that renders again before the sign-in screen would otherwise ask the server, which has
 * ended the session, and get a 401.
 */
export function useBootstrap(enabled = true) {
  const qc = useQueryClient();
  const signingOut = useStore(signingOutStore);
  return useQuery({
    queryKey: keys.bootstrap,
    queryFn: async ({ signal }): Promise<BootstrapAnswer> => {
      const meta: { cached?: boolean } = {};
      const b = await api<Bootstrap>("/api/bootstrap", { signal, meta });
      // Only an answer from the network says anything about the server: the worker's stored copy is old by design.
      if (!meta.cached && serverRebuilt(b.web_build)) setUpdateReady();
      // While changes wait to be sent, the answer may not have them yet: the badges keep counting them (lib/offline.ts).
      const shown = await countsForAnswer(b, !meta.cached, { shown: qc.getQueryData<Bootstrap>(keys.bootstrap), busy: qc.isMutating() > 0 });
      return meta.cached ? { ...shown, fromCache: true } : shown;
    },
    enabled: enabled && !signingOut,
    retry: retryRead,
    staleTime: 60_000,
  });
}

/** The folder tree of the cached bootstrap (lib/folderTree). Its folders carry the unread counts of that bootstrap. */
export function useFolderTree(): FolderTree {
  const folders = useBootstrap().data?.folders;
  return useMemo(() => folderTree(folders ?? []), [folders]);
}

/**
 * The options of a cursor-paged item list (design 7.1: keyset cursor, limit at most 100): the list `scope` names, kept
 * under `queryKey` and read `limit` articles at a time. The one place a list's request and caching are decided, for
 * the list itself (useItems) and for a Digest section (keys.section), which is a list of its own.
 */
export function itemsQuery(scope: Scope, queryKey: readonly unknown[], limit = PAGE_SIZE) {
  return {
    queryKey,
    queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) => {
      const meta: { cached?: boolean } = {};
      const page = await api<ItemsPage>("/api/items", { params: itemsParams(scope, pageParam || undefined, limit), signal, meta });
      return meta.cached ? { ...page, items: await overlayPending(page.items) } : page;
    },
    initialPageParam: "",
    getNextPageParam: (last: ItemsPage) => last.next_cursor ?? undefined,
    // A search the server refuses (422 too broad) or a cursor it no longer takes (400) will not get better by asking again.
    retry: retryRead,
    // A list is a snapshot: rows read in place stay visible (dimmed) until the
    // user refreshes or leaves. SSE never refetches it behind the user's back.
    staleTime: Infinity,
    gcTime: 30 * 60_000,
  };
}

/** The list of a scope, a page at a time. */
export function useItems(scope: Scope, enabled = true) {
  return useInfiniteQuery({ ...itemsQuery(scope, keys.items(scope)), enabled });
}

export function useItem(id: string | undefined) {
  return useQuery({
    queryKey: keys.item(id ?? ""),
    queryFn: async ({ signal }) => {
      const meta: { cached?: boolean } = {};
      const d = await api<ItemDetail>(`/api/items/${id}`, { signal, meta });
      return meta.cached ? (await overlayPending([d]))[0]! : d;
    },
    enabled: !!id,
    // Asked even when the browser says it is offline (networkMode "always", the client default in App.tsx): the
    // service worker may hold this article, and when it does not the article shows its error screen (#95).
    staleTime: 5 * 60_000,
  });
}

export function flattenItems(data: InfiniteData<ItemsPage> | undefined): Card[] {
  return data?.pages.flatMap((p) => p.items) ?? [];
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

/**
 * Adjust unread counts locally (total, feed, its folder and the folders above it) so badges move before the counts
 * event lands.
 */
export function bumpUnread(qc: QueryClient, feedId: string, delta: number): void {
  lastBumpAt = Date.now();
  qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => {
    if (!old) return old;
    const feed = old.feeds.find((f) => f.id === feedId);
    const above = new Set(feed ? chainOf(folderTree(old.folders), feed.folder_id) : []);
    return {
      ...old,
      counts: { ...old.counts, unread: Math.max(0, old.counts.unread + delta) },
      feeds: old.feeds.map((f) => (f.id === feedId ? { ...f, unread: Math.max(0, f.unread + delta) } : f)),
      folders: old.folders.map((fo) => (above.has(fo.id) ? { ...fo, unread: Math.max(0, fo.unread + delta) } : fo)),
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

/** How marking `ids` read or unread moves each feed's unread count, from what the cached articles show now. */
function unreadShift(qc: QueryClient, ids: string[], read: boolean): Map<string, number> {
  const out = new Map<string, number>();
  for (const id of new Set(ids)) {
    const c = findCached(qc, id);
    if (c && c.read !== read) out.set(c.feed_id, (out.get(c.feed_id) ?? 0) + (read ? -1 : 1));
  }
  return out;
}

/**
 * Opening marks the item read. The badge moves optimistically in onMutate, before the request, so a
 * server `counts` event that arrives ahead of the response (absolute numbers) simply overwrites it
 * and nothing is applied twice; onSuccess never bumps. A failure puts everything back.
 */
export function useOpenItem() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, via }: { id: string; via: "tap" | "nav" }) => {
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
 * for a caller that decides itself what to say (mark-read-on-scroll tells a streak of failures once). `undo` (with
 * read false) is the undo of a mark read: the server leaves articles muted since as they are.
 */
export async function applyRead(
  qc: QueryClient,
  ids: string[],
  read: boolean,
  reason: "swipe" | "key" | "scroll" | "bulk",
  opts: { onError?: (e: unknown) => void; undo?: boolean } = {},
): Promise<MarkReadResponse | undefined> {
  const shift = unreadShift(qc, ids, read);
  const undo = opts.undo === true;
  patchItems(qc, ids, { read });
  await supersede({ read: ids });
  try {
    try {
      const res = await api<MarkReadResponse>("/api/items/mark-read", { method: "POST", body: { ids, read, reason, ...(undo ? { undo } : {}) } });
      undoKept(qc, res?.kept);
      return res;
    } catch (e) {
      // No network: keep the change on screen and send it when the connection returns (lib/offline.ts). A
      // change that could not be stored for later is a failure like any other.
      if (!isOffline(e)) throw e;
      const res = await queueRead(ids, read, undo);
      // Online the server's `counts` event moves the badges; offline none comes, so they move here.
      for (const [feed, d] of shift) bumpUnread(qc, feed, d);
      return res;
    }
  } catch (e) {
    patchItems(qc, ids, { read: !read });
    if (opts.onError) opts.onError(e);
    else toast(changeError(e), "error");
    return undefined;
  }
}

/**
 * Set starred with an optimistic patch; reverts and toasts on failure. `undo` (with starred true) is the undo of an
 * unstar: the server leaves an article muted since as it is. Resolves `false` when the change failed, `"kept"` when
 * an undo left the article as it was (muted or trimmed since).
 */
export async function applyStar(qc: QueryClient, id: string, starred: boolean, undo = false): Promise<boolean | "kept"> {
  patchItems(qc, [id], { starred: starred });
  await supersede({ star: id });
  try {
    try {
      const res = await api<{ kept?: boolean }>(`/api/items/${id}/star`, { method: "PUT", body: { starred, ...(undo ? { undo } : {}) } });
      if (res?.kept) {
        undoKept(qc, [id]);
        return "kept";
      }
    } catch (e) {
      if (!isOffline(e)) throw e;
      await queueStar(id, starred, undefined, undo);
    }
    return true;
  } catch (e) {
    patchItems(qc, [id], { starred: !starred });
    toast(changeError(e), "error");
    return false;
  }
}
