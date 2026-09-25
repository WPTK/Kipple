import { useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { applyRead, applyStar, patchItems } from "@/api/queries";
import { markAllRead, markRange, type RangeParams } from "@/api/bulk";
import type { Card, Scope } from "@/api/types";
import { announce } from "@/shell/toasts";
import { pushUndo } from "./undo";

/** Read, unread and star changes with their undo entries. One place, so every gesture agrees. */
export function useItemActions() {
  const qc = useQueryClient();
  return useMemo(() => itemActions(qc), [qc]);
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
      undo: async (undoIds) => {
        await applyRead(qc, undoIds, !read, "key");
      },
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
      pushUndo({ kind: starred ? "star" : "unstar", ids: [item.id], undo: async () => void (await applyStar(qc, item.id, !starred)) });
    }
  }

  /** After a bulk call: patch the ids the server changed and offer undo through them. */
  function finishBulk(res: { changed: string[]; count?: number; undoable?: boolean }, fallbackIds: string[], restore?: () => void): void {
    const changed = res.changed.length ? res.changed : res.undoable === false ? [] : fallbackIds;
    if (changed.length) patchItems(qc, changed, { read: true });
    const n = res.count ?? changed.length;
    if (res.undoable === false || changed.length === 0) {
      announce(n === 0 ? "Nothing to mark" : `Marked ${n} as read`);
      return;
    }
    pushUndo({
      kind: "read",
      bulk: true,
      ids: changed,
      restore,
      undo: async (ids) => {
        await applyRead(qc, ids, false, "bulk");
      },
    });
  }

  async function markSide(p: RangeParams, local: string[], restore?: () => void): Promise<void> {
    patchItems(qc, local, { read: true });
    try {
      finishBulk(await markRange(p), local, restore);
    } catch {
      patchItems(qc, local, { read: false });
      restore?.();
      announce("Couldn't mark those as read");
    }
  }

  async function markAll(scope: Scope, maxId: string | undefined, local: string[], restore?: () => void): Promise<void> {
    patchItems(qc, local, { read: true });
    try {
      finishBulk(await markAllRead(scope, maxId), local, restore);
    } catch {
      patchItems(qc, local, { read: false });
      restore?.();
      announce("Couldn't mark those as read");
    }
  }

  return { setRead, toggleRead, toggleStar, markSide, markAll };
}
