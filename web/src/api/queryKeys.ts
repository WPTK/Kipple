import type { InfiniteData, QueryClient } from "@tanstack/react-query";
import type { Card, ItemDetail, ItemsPage, Scope } from "./types";
import { READING_LENGTH_MINUTES, isReadingLength } from "@/lib/readingLength";

/**
 * Query keys, scope keys and list parameters. A leaf module (it imports only types) so that lib/offline.ts can use
 * them without importing api/queries.ts, which imports lib/offline.ts. queries.ts re-exports everything here.
 */

export const PAGE_SIZE = 50;
/** What follows a scope key in the key of a Digest section (parseScopeKey reads past it, so it names the same scope). */
export const SECTION_SUFFIX = "|digest:1";

export const keys = {
  bootstrap: ["bootstrap"] as const,
  /** GET /api/auth/me, live (not "auth": the sign-out sweep must drop it). */
  me: ["me"] as const,
  items: (scope: Scope) => ["items", scopeKey(scope)] as const,
  /**
   * One section of a Digest page: the list of a folder or a feed, kept apart from the list of the same scope (its pages
   * are cut differently). Under "items", so every patch, drop and invalidation of a list reaches it too.
   */
  section: (scope: Scope) => ["items", `${scopeKey(scope)}${SECTION_SUFFIX}`] as const,
  itemsAll: ["items"] as const,
  /** Each feed's newest articles, for the Feed index. Not under "items": it is a list of feeds, not of articles. */
  feedsLatest: ["feeds-latest"] as const,
  item: (id: string) => ["item", id] as const,
  stats: (range: string) => ["stats", range] as const,
};

/** Stable string for a scope; also used in the article route's `from` param. */
export function scopeKey(s: Scope): string {
  const parts: string[] = [s.view];
  if (s.feed) parts.push(`feed:${s.feed}`);
  if (s.folder) parts.push(`folder:${s.folder}`);
  if (s.q) parts.push(`q:${encodeURIComponent(s.q)}`);
  if (s.order === "oldest" || s.order === "rank") parts.push(`order:${s.order}`);
  if (s.typing) parts.push("typing:1");
  if (s.length) parts.push(`len:${s.length}`);
  if (s.after) parts.push(`after:${s.after}`);
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
    else if (k === "q") {
      try {
        scope.q = decodeURIComponent(v);
      } catch {
        // A hand-edited or truncated `?from=` (a stray `%`): the default scope, not a crash during render.
        return { view: "unread" };
      }
    } else if (k === "order" && (v === "oldest" || v === "rank")) scope.order = v;
    else if (k === "typing" && v === "1") scope.typing = true;
    else if (k === "len" && isReadingLength(v)) scope.length = v;
    else if (k === "after" && /^(0|[1-9]\d*)$/.test(v)) scope.after = v;
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
    ...(scope.length ? READING_LENGTH_MINUTES[scope.length] : {}),
    after: scope.after,
    cursor,
    limit,
  };
}

export type ItemPatch = Partial<Pick<Card, "read" | "starred" | "muted_by" | "muted_by_name">>;

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

/**
 * Articles an undo left as they were (the reply's `kept`, design §7.11): muted since, which makes an article read and
 * unstarred, or trimmed since, which the undo does not restore. The undo's optimistic patch is taken back by
 * reloading the open article, and it leaves every list but Muted, which loads it the next time it is shown.
 */
export function undoKept(qc: QueryClient, ids: string[] | undefined): void {
  if (!ids?.length) return;
  // The state the server holds (muted, read, unstarred; or gone) is not known here: the open article is read again.
  for (const id of ids) void qc.invalidateQueries({ queryKey: keys.item(id) });
  dropFromLists(qc, ids, (k) => !k.startsWith("muted"));
  invalidateLists(qc, (k) => k.startsWith("muted"));
}
