import { useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { applyRead, applyStar, keys, patchItems } from "@/api/queries";
import { api, errorMessage } from "@/api/client";
import { markAllRead, markRange, type RangeParams } from "@/api/bulk";
import type { Card, Scope } from "@/api/types";
import { announce, toast } from "@/shell/toasts";
import { pushUndo } from "./undo";

/** Read, unread and star changes with their undo entries. One place, so every gesture agrees. */
export function useItemActions() {
  const qc = useQueryClient();
  return useMemo(() => itemActions(qc), [qc]);
}

/** Undo a scope mark: the changed ids plus the trimmed-ledger ids it flipped (design 7.1). */
async function undoBulk(qc: QueryClient, ids: string[], ledger: string[]): Promise<boolean> {
  patchItems(qc, ids, { read: false });
  try {
    await api("/api/items/mark-read", { method: "POST", body: { ids, ...(ledger.length ? { ledger_ids: ledger } : {}), read: false, reason: "bulk" } });
    return true;
  } catch (e) {
    patchItems(qc, ids, { read: true });
    toast(errorMessage(e), "error");
    return false;
  }
}

export function itemActions(qc: QueryClient) {
  /** Set read state for ids and record an undo (mark-read undone = mark-unread through the API). */
  async function setRead(ids: string[], read: boolean, reason: "swipe" | "key", restore?: () => void): Promise<void> {
    if (ids.length === 0) return;
    const res = await applyRead(qc, ids, read, reason);
    if (!res) {
      restore?.();
      return;
    }
    pushUndo({
      kind: read ? "read" : "unread",
      ids,
      restore,
      undo: async (undoIds) => (await applyRead(qc, undoIds, !read, "key")) !== undefined,
    });
  }

  function toggleRead(item: Card, reason: "swipe" | "key" = "key", restore?: () => void): Promise<void> {
    return setRead([item.id], !item.read, reason, restore);
  }

  async function toggleStar(item: Card, undoable = false): Promise<void> {
    const starred = !item.starred;
    const ok = await applyStar(qc, item.id, starred);
    if (!ok) return;
    announce(starred ? "Starred" : "Unstarred");
    if (undoable) {
      // Merged swipe-stars share the first closure, so it must act on the ids it is handed.
      pushUndo({
        kind: starred ? "star" : "unstar",
        ids: [item.id],
        undo: async (ids) => {
          const done = await Promise.all(ids.map((id) => applyStar(qc, id, !starred)));
          return done.every(Boolean);
        },
      });
    }
  }

  /**
   * After a bulk call: patch the ids the server changed and offer undo through them. `local` are the rows
   * the caller optimistically marked (and possibly hid); the ones the server did not change (above the
   * list's `as_of`, or read elsewhere already) go back to unread, and `unhide` brings them back on screen.
   */
  function finishBulk(
    res: { changed: string[]; count?: number; undoable?: boolean; ledger_ids?: string[] },
    local: string[],
    restore?: () => void,
    unhide?: (ids: string[]) => void,
  ): void {
    const overCap = res.undoable === false && res.changed.length === 0 && (res.count ?? 0) > 0;
    // Only what the server says it changed is undoable: local guesses would flip items another client read.
    const changed = res.undoable === false ? [] : res.changed;
    const ledger = res.ledger_ids ?? [];
    if (overCap) {
      // The server marked more than it will list, so which local rows it skipped is unknown: refetch the truth.
      announce(`Marked ${res.count} as read`);
      void qc.invalidateQueries({ queryKey: keys.itemsAll }).then(() => unhide?.(local));
      return;
    }
    if (changed.length) patchItems(qc, changed, { read: true });
    const kept = new Set(changed);
    const skipped = local.filter((id) => !kept.has(id));
    if (skipped.length) {
      patchItems(qc, skipped, { read: false });
      unhide?.(skipped);
    }
    const n = res.count ?? changed.length;
    if (changed.length === 0) {
      announce(n === 0 ? "Nothing to mark" : `Marked ${n} as read`);
      return;
    }
    pushUndo({
      kind: "read",
      bulk: true,
      ids: changed,
      restore,
      undo: (ids) => undoBulk(qc, ids, ledger),
    });
  }

  async function markSide(p: RangeParams, local: string[], restore?: () => void, unhide?: (ids: string[]) => void): Promise<void> {
    patchItems(qc, local, { read: true });
    try {
      finishBulk(await markRange(p), local, restore, unhide);
    } catch {
      patchItems(qc, local, { read: false });
      restore?.();
      announce("Couldn't mark those as read");
    }
  }

  async function markAll(scope: Scope, maxId: string | undefined, local: string[], restore?: () => void, unhide?: (ids: string[]) => void): Promise<void> {
    patchItems(qc, local, { read: true });
    try {
      finishBulk(await markAllRead(scope, maxId), local, restore, unhide);
    } catch {
      patchItems(qc, local, { read: false });
      restore?.();
      announce("Couldn't mark those as read");
    }
  }

  return { setRead, toggleRead, toggleStar, markSide, markAll };
}
