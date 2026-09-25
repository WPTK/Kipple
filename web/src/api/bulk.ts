import { api } from "./client";
import type { BulkMarkResponse, Card, Scope } from "./types";

// Bulk read marking. Both calls go to POST /api/items/mark-read with a `scope` (the
// backend's shape, docs/research/backend-additions-round2.md section 3). Keeping them
// in this file means a change of endpoint or body is a one-place edit.

export interface ScopeBody {
  feed_id?: string;
  folder_id?: string;
  all?: true;
  view: Scope["view"];
  q?: string;
}

export function scopeBody(scope: Scope): ScopeBody {
  const b: ScopeBody = { view: scope.view };
  if (scope.feed) b.feed_id = scope.feed;
  else if (scope.folder) b.folder_id = scope.folder;
  else b.all = true;
  if (scope.q) b.q = scope.q;
  return b;
}

export interface RangeParams {
  scope: Scope;
  order: "date" | "oldest";
  side: "above" | "below";
  anchor: Pick<Card, "id" | "sort_at">;
  /** The list's `as_of` (GET /api/items): nothing committed after it is swept up. */
  maxId: string | undefined;
}

/** Mark everything above or below `anchor` (in the list's own order, anchor excluded) as read. */
export function markRange(p: RangeParams): Promise<BulkMarkResponse> {
  return api<BulkMarkResponse>("/api/items/mark-read", {
    method: "POST",
    body: {
      scope: scopeBody(p.scope),
      bound: { order: p.order, side: p.side, anchor: { sort_at: p.anchor.sort_at, id: p.anchor.id }, inclusive: false },
      ...(p.maxId ? { max_id: p.maxId } : {}),
      read: true,
      reason: "bulk",
    },
  });
}

/** Mark the whole list read, bounded by the ids the list already knew about. */
export function markAllRead(scope: Scope, maxId: string | undefined): Promise<BulkMarkResponse> {
  return api<BulkMarkResponse>("/api/items/mark-read", {
    method: "POST",
    body: { scope: scopeBody(scope), ...(maxId ? { max_id: maxId } : {}), read: true, reason: "bulk" },
  });
}
