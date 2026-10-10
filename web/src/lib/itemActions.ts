import { useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { applyRead, applyStar, bumpMuted, dropFromLists, keys, patchItems } from "@/api/queries";
import { api, errorMessage } from "@/api/client";
import { markAllRead, markRange, type RangeParams } from "@/api/bulk";
import type { Card, ItemsPage, Scope } from "@/api/types";
import { announce, toast } from "@/shell/toasts";
import { createStore } from "./store";
import { pushUndo, undoable } from "./undo";

/**
 * Articles the user marked read on purpose, with a button, a key or the menu (not by opening them, and not by a
 * swipe, which removes its row itself). The Unread list lets those rows leave after a moment; marking one
 * unread again, or undoing, takes it back out of this set and the row stays.
 */
export const readIntent = createStore<ReadonlySet<string>>(new Set());
function addIntent(ids: string[]): void {
  readIntent.set((s) => new Set([...s, ...ids]));
}
function dropIntent(ids: string[]): void {
  readIntent.set((s) => (ids.some((i) => s.has(i)) ? new Set([...s].filter((x) => !ids.includes(x))) : s));
}

const unreadListeners = new Set<(ids: string[]) => void>();
/**
 * Called with ids that became unread again (an undo of a mark-read, or a mark-unread). A list that is not on screen
 * (a phone shows the article in a new screen) keeps remembered hidden rows; this is how it hears they are back.
 */
export function onBecameUnread(fn: (ids: string[]) => void): () => void {
  unreadListeners.add(fn);
  return () => unreadListeners.delete(fn);
}
function notifyUnread(ids: string[]): void {
  for (const fn of unreadListeners) fn(ids);
}

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

/**
 * An Unread list is a snapshot, so an article that becomes unread while it is not in that snapshot (marked
 * unread from All, from Starred, from the reader) would be missing when the list is opened. Mark those lists
 * stale, without refetching the one on screen: they reload when next shown.
 */
export function invalidateUnreadLists(qc: QueryClient): void {
  void qc.invalidateQueries({
    queryKey: keys.itemsAll,
    predicate: (q) => String(q.queryKey[1]).startsWith("unread"),
    refetchType: "none",
  });
}

/**
 * Whether article `id` arrived after a list's `as_of`, so a bulk mark bounded by it left the article alone. Ids are
 * decimal and rise with arrival. With no bound nothing was left out for arriving late; an id that does not parse
 * counts as late, since treating a row as untouched only puts it back on screen, which is the safe mistake.
 */
export function arrivedAfter(id: string, asOf: string | undefined): boolean {
  if (!asOf) return false;
  try {
    return BigInt(id) > BigInt(asOf);
  } catch {
    return true;
  }
}

/** The most ids GET /api/items?ids= takes in one request (the server's card limit). */
const IDS_PER_QUERY = 100;

export function itemActions(qc: QueryClient) {
  /** Set read state for ids and record an undo (mark-read undone = mark-unread through the API). */
  async function setRead(ids: string[], read: boolean, reason: "swipe" | "key", restore?: () => void): Promise<void> {
    if (ids.length === 0) return;
    const res = await applyRead(qc, ids, read, reason);
    if (!res) {
      restore?.();
      return;
    }
    if (read && reason === "key") addIntent(ids);
    else dropIntent(ids);
    if (!read) {
      invalidateUnreadLists(qc);
      notifyUnread(ids);
    }
    pushUndo({
      kind: read ? "read" : "unread",
      ids,
      restore,
      undo: async (undoIds) => {
        dropIntent(undoIds);
        const ok = (await applyRead(qc, undoIds, !read, "key")) !== undefined;
        if (ok && read) {
          invalidateUnreadLists(qc);
          notifyUnread(undoIds);
        }
        return ok;
      },
    });
  }

  function toggleRead(item: Card, reason: "swipe" | "key" = "key", restore?: () => void): Promise<void> {
    return setRead([item.id], !item.read, reason, restore);
  }

  /** Resolves to whether the server took it. */
  async function toggleStar(item: Card, undoable = false): Promise<boolean> {
    const starred = !item.starred;
    const ok = await applyStar(qc, item.id, starred);
    if (!ok) return false;
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
    return true;
  }

  /**
   * The rows a bulk mark left alone at or below the list's `as_of`. That is usually because they were read already
   * (on another device, say), but the server's scope can also differ from the list on screen (full text replaced
   * an article's content since, a search's fallback, a query with no usable terms), so whether they are still
   * unread is asked, not guessed: the unread ones go back to unread and back on screen, the read ones stay hidden.
   * When the server cannot be asked, the lists are reloaded instead and the rows put back on screen.
   */
  async function recheckUnchanged(ids: string[], unhide?: (ids: string[]) => void): Promise<void> {
    try {
      const unread: string[] = [];
      for (let i = 0; i < ids.length; i += IDS_PER_QUERY) {
        const page = await api<ItemsPage>("/api/items", { params: { ids: ids.slice(i, i + IDS_PER_QUERY).join(",") }, quiet: true });
        for (const c of page.items) if (!c.read) unread.push(c.id);
      }
      if (unread.length) {
        patchItems(qc, unread, { read: false });
        unhide?.(unread);
      }
    } catch {
      await qc.invalidateQueries({ queryKey: keys.itemsAll });
      unhide?.(ids);
    }
  }

  /**
   * After a bulk call: patch the ids the server changed and offer undo through them. `local` are the rows the
   * caller optimistically marked (and possibly hid). Of the ones the server did not change, those above the list's
   * `as_of` (`asOf`) were left alone because of the bound: they go back to unread and `unhide` brings them back on
   * screen. For the rest the server is asked what they are now (recheckUnchanged).
   */
  async function finishBulk(
    res: { changed: string[]; count?: number; undoable?: boolean; ledger_ids?: string[] },
    local: string[],
    asOf: string | undefined,
    restore?: () => void,
    unhide?: (ids: string[]) => void,
  ): Promise<void> {
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
    const skipped = local.filter((id) => !kept.has(id) && arrivedAfter(id, asOf));
    if (skipped.length) {
      patchItems(qc, skipped, { read: false });
      unhide?.(skipped);
    }
    const unsure = local.filter((id) => !kept.has(id) && !arrivedAfter(id, asOf));
    if (unsure.length) await recheckUnchanged(unsure, unhide);
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
      await finishBulk(await markRange(p), local, p.maxId, restore, unhide);
    } catch {
      patchItems(qc, local, { read: false });
      restore?.();
      announce("Couldn't mark those as read");
    }
  }

  async function markAll(scope: Scope, maxId: string | undefined, local: string[], restore?: () => void, unhide?: (ids: string[]) => void): Promise<void> {
    patchItems(qc, local, { read: true });
    try {
      await finishBulk(await markAllRead(scope, maxId), local, maxId, restore, unhide);
    } catch {
      patchItems(qc, local, { read: false });
      restore?.();
      announce("Couldn't mark those as read");
    }
  }

  /**
   * Restore muted articles: mark them unread, which is what un-mutes them (the server clears the mute in the same
   * write). There is no undo entry: putting one back under its rule is not something a mark-read can do, so the
   * announcement says where they went instead. Resolves to the ids the server accepted.
   */
  async function restoreMuted(items: Pick<Card, "id">[], restore?: () => void): Promise<boolean> {
    const ids = items.map((i) => i.id);
    if (ids.length === 0) return false;
    const res = await applyRead(qc, ids, false, "key");
    if (!res) {
      restore?.();
      return false;
    }
    patchItems(qc, ids, { muted_by: null, muted_by_name: null });
    dropFromLists(qc, ids, (k) => k.startsWith("muted"));
    invalidateUnreadLists(qc);
    notifyUnread(ids);
    bumpMuted(qc, -ids.length);
    announce(ids.length === 1 ? "Restored. It is unread again." : `Restored ${ids.length} articles. They are unread again.`);
    return true;
  }

  // Each of these changes the screen at once and pushes its undo group only when the server has answered.
  const tracked =
    <A extends unknown[], R>(fn: (...a: A) => Promise<R>) =>
    (...a: A): Promise<R> =>
      undoable(fn(...a));
  return {
    setRead: tracked(setRead),
    toggleRead: tracked(toggleRead),
    toggleStar: tracked(toggleStar),
    markSide: tracked(markSide),
    markAll: tracked(markAll),
    restoreMuted,
  };
}
