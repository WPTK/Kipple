import type { InfiniteData, QueryClient } from "@tanstack/react-query";
import type { Card, ItemDetail, ItemsPage, Scope } from "./types";
import { READING_LENGTH_MINUTES, isReadingLength } from "@/lib/readingLength";

/**
 * Query keys, scope keys and list parameters. A leaf module (it imports only types) so that lib/offline.ts can use
 * them without importing api/queries.ts, which imports lib/offline.ts. queries.ts re-exports everything here.
 */

export const PAGE_SIZE = 50;

export const keys = {
  bootstrap: ["bootstrap"] as const,
  /** GET /api/auth/me, live (not "auth": the sign-out sweep must drop it). */
  me: ["me"] as const,
  items: (scope: Scope) => ["items", scopeKey(scope)] as const,
  itemsAll: ["items"] as const,
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
