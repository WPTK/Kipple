import { useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { applyRead, applyStar, bumpMuted, dropFromLists, invalidateLists, keys, undoKept, patchItems } from "@/api/queries";
import { api, failed } from "@/api/client";
import { markAllRead, markRange, type RangeParams } from "@/api/bulk";
import type { BulkMarkResponse, Card, ItemsPage, MarkReadResponse, Scope } from "@/api/types";
import { announce, toast } from "@/shell/toasts";
import { createStore } from "./store";
import { beginUndo, type UndoChange } from "./undo";

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

/** `"kept"` when the server left every id as it was, so the undo changed nothing. */
function allKept(ids: string[], kept: string[] | undefined): true | "kept" {
  return ids.length > 0 && kept !== undefined && ids.every((id) => kept.includes(id)) ? "kept" : true;
}

/** Undo a scope mark: the changed ids plus the trimmed-ledger ids it flipped (design 7.1). */
async function undoBulk(qc: QueryClient, ids: string[], ledger: string[]): Promise<boolean | "kept"> {
  const requests = undoRequests(ids, ledger);
  patchItems(qc, ids, { read: false });
  const sent = await Promise.allSettled(requests.map((r) => api<MarkReadResponse>("/api/items/mark-read", { method: "POST", body: { ids: r.ids, ...(r.ledger.length ? { ledger_ids: r.ledger } : {}), read: false, reason: "bulk", undo: true } })));
  const lost = requests.filter((_, i) => sent[i]!.status === "rejected");
  const kept: string[] = [];
  for (const s of sent) {
    if (s.status !== "fulfilled") continue;
    undoKept(qc, s.value?.kept);
    kept.push(...(s.value?.kept ?? []));
  }
  if (lost.length) {
    // What a failed request would have restored goes back to read; the rest was undone.
    patchItems(qc, lost.flatMap((r) => r.ids), { read: true });
    const first = sent.find((s) => s.status === "rejected") as PromiseRejectedResult;
    toast(failed("undo that", first.reason), "error");
    if (lost.length === requests.length) return false;
  }
  // Ledger rows the undo flipped back are a change even when every stored article was kept.
  return ledger.length > 0 ? true : allKept(ids, kept);
}

/** The most ids, and ledger ids, one mark-read request takes (the server's caps). */
export const UNDO_IDS_PER_REQUEST = 10000;
export const UNDO_LEDGER_PER_REQUEST = 20000;

/** Split an undo into requests the server takes: the ids and the ledger ids each within their cap. At least one request. */
export function undoRequests(ids: readonly string[], ledger: readonly string[]): { ids: string[]; ledger: string[] }[] {
  const n = Math.max(1, Math.ceil(ids.length / UNDO_IDS_PER_REQUEST), Math.ceil(ledger.length / UNDO_LEDGER_PER_REQUEST));
  return Array.from({ length: n }, (_, i) => ({
    ids: ids.slice(i * UNDO_IDS_PER_REQUEST, (i + 1) * UNDO_IDS_PER_REQUEST),
    ledger: ledger.slice(i * UNDO_LEDGER_PER_REQUEST, (i + 1) * UNDO_LEDGER_PER_REQUEST),
  }));
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

/** What a Restore in Muted says: n articles un-muted, `unread` of them unread again (the rest were read before the mute). */
export function restoredMessage(n: number, unread: number): string {
  if (n === 0) return "Nothing to restore";
  if (n === 1) return unread ? "Restored. It is unread again." : "Restored. It stays read, as it was before it was muted.";
  if (unread === n) return `Restored ${n} articles. They are unread again.`;
  if (unread === 0) return `Restored ${n} articles. They stay read, as they were before they were muted.`;
  return `Restored ${n} articles. ${unread} of them ${unread === 1 ? "is" : "are"} unread again; the others stay read, as they were before they were muted.`;
}

/** The most ids GET /api/items?ids= takes in one request (the server's card limit). */
const IDS_PER_QUERY = 100;

export function itemActions(qc: QueryClient) {
  /** Set read state for ids and record an undo (mark-read undone = mark-unread through the API). */
  async function setRead(ids: string[], read: boolean, reason: "swipe" | "key", restore?: () => void): Promise<void> {
    if (ids.length === 0) return;
    const ticket = beginUndo(read ? "read" : "unread");
    const res = await applyRead(qc, ids, read, reason).catch((e: unknown) => {
      ticket.settle(null);
      throw e;
    });
    if (!res) {
      ticket.settle(null);
      restore?.();
      return;
    }
    if (read && reason === "key") addIntent(ids);
    else dropIntent(ids);
    if (!read) {
      invalidateUnreadLists(qc);
      notifyUnread(ids);
    }
    ticket.settle({
      ids,
      restore,
      undo: async (undoIds) => {
        dropIntent(undoIds);
        // Undoing a mark read is a mark unread, which would also un-mute an article a filter muted since: `undo`
        // has the server leave those alone (an undo of a mark unread marks read, which never un-mutes).
        const res = await applyRead(qc, undoIds, !read, "key", { undo: read });
        if (res && read) {
          invalidateUnreadLists(qc);
          notifyUnread(undoIds.filter((id) => !res.kept?.includes(id)));
        }
        return res === undefined ? false : read ? allKept(undoIds, res.kept) : true;
      },
    });
  }

  function toggleRead(item: Card, reason: "swipe" | "key" = "key", restore?: () => void): Promise<void> {
    return setRead([item.id], !item.read, reason, restore);
  }

  /** Resolves to whether the server took it. */
  async function toggleStar(item: Card, undoable = false): Promise<boolean> {
    const starred = !item.starred;
    const ticket = undoable ? beginUndo(starred ? "star" : "unstar") : null;
    const ok = await applyStar(qc, item.id, starred);
    if (!ok) {
      ticket?.settle(null);
      return false;
    }
    announce(starred ? "Starred" : "Unstarred");
    // Merged swipe-stars share the first closure, so it must act on the ids it is handed.
    ticket?.settle({
      ids: [item.id],
      undo: async (ids) => {
        // Undoing an unstar is a star, which would also un-mute: `undo` has the server leave a muted article alone.
        const done = await Promise.all(ids.map((id) => applyStar(qc, id, !starred, !starred)));
        if (!done.every((d) => d !== false)) return false;
        return done.every((d) => d === "kept") ? "kept" : true;
      },
    });
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
   * After a bulk call: patch the ids the server changed. `local` are the rows the caller optimistically marked (and
   * possibly hid). Of the ones the server did not change, those above the list's `as_of` (`asOf`) were left alone because
   * of the bound: they go back to unread and `unhide` brings them back on screen. For the rest the server is asked what
   * they are now (recheckUnchanged). Says what the call came to; the caller announces and offers the undo.
   */
  async function settleBulk(
    res: { changed: string[]; count?: number; undoable?: boolean; ledger_ids?: string[] },
    local: string[],
    asOf: string | undefined,
    unhide?: (ids: string[]) => void,
  ): Promise<{ changed: string[]; ledger: string[]; count: number; overCap: boolean }> {
    const overCap = res.undoable === false && res.changed.length === 0 && (res.count ?? 0) > 0;
    if (overCap) {
      // The server marked more than it will list, so which local rows it skipped is unknown: refetch the truth.
      void qc.invalidateQueries({ queryKey: keys.itemsAll }).then(() => unhide?.(local));
      return { changed: [], ledger: [], count: res.count ?? 0, overCap: true };
    }
    // Only what the server says it changed is undoable: local guesses would flip items another client read.
    const changed = res.undoable === false ? [] : res.changed;
    if (changed.length) patchItems(qc, changed, { read: true });
    const kept = new Set(changed);
    const skipped = local.filter((id) => !kept.has(id) && arrivedAfter(id, asOf));
    if (skipped.length) {
      patchItems(qc, skipped, { read: false });
      unhide?.(skipped);
    }
    const unsure = local.filter((id) => !kept.has(id) && !arrivedAfter(id, asOf));
    if (unsure.length) await recheckUnchanged(unsure, unhide);
    return { changed, ledger: res.ledger_ids ?? [], count: res.count ?? changed.length, overCap: false };
  }

  /** After a bulk call: settle it and offer undo through the ids it changed. */
  async function finishBulk(
    res: { changed: string[]; count?: number; undoable?: boolean; ledger_ids?: string[] },
    local: string[],
    asOf: string | undefined,
    restore?: () => void,
    unhide?: (ids: string[]) => void,
  ): Promise<UndoChange | null> {
    const r = await settleBulk(res, local, asOf, unhide);
    if (r.overCap) {
      announce(`Marked ${r.count} as read`);
      return null;
    }
    if (r.changed.length === 0) {
      announce(r.count === 0 ? "Nothing to mark" : `Marked ${r.count} as read`);
      return null;
    }
    return { ids: r.changed, restore, undo: (ids) => undoBulk(qc, ids, r.ledger) };
  }

  /** A bulk mark: its undo group is on the stack from the moment the rows turn read, filled in by the answer. */
  async function bulkMark(send: () => Promise<BulkMarkResponse>, local: string[], asOf: string | undefined, restore?: () => void, unhide?: (ids: string[]) => void): Promise<void> {
    const ticket = beginUndo("read", { bulk: true });
    patchItems(qc, local, { read: true });
    try {
      ticket.settle(await finishBulk(await send(), local, asOf, restore, unhide));
    } catch (e) {
      ticket.settle(null);
      patchItems(qc, local, { read: false });
      restore?.();
      toast(failed("mark those as read", e), "error", undefined, "read");
    }
  }

  /**
   * Several bulk marks that are one action to the reader (Mark all on a list made of lists): one undo group and one
   * toast for all of them, one announcement, and when only some failed those rows alone go back to unread. `local` of each
   * request are the rows it covers (the caller hides all of them at once and hands `restore` for that).
   */
  async function bulkMarkMany(
    requests: { send: () => Promise<BulkMarkResponse>; local: string[]; asOf: string | undefined }[],
    restore?: () => void,
    unhide?: (ids: string[]) => void,
  ): Promise<void> {
    const ticket = beginUndo("read", { bulk: true });
    patchItems(qc, requests.flatMap((r) => r.local), { read: true });
    const sent = await Promise.allSettled(requests.map((r) => r.send()));
    const lost = requests.filter((_, i) => sent[i]!.status === "rejected");
    const failedLocal = lost.flatMap((r) => r.local);
    if (lost.length === requests.length) {
      ticket.settle(null);
      patchItems(qc, failedLocal, { read: false });
      restore?.();
      announce("Couldn't mark those as read");
      return;
    }
    if (failedLocal.length) {
      patchItems(qc, failedLocal, { read: false });
      unhide?.(failedLocal);
    }
    const done = await Promise.all(
      requests.flatMap((r, i) => {
        const s = sent[i]!;
        return s.status === "fulfilled" ? [settleBulk(s.value, r.local, r.asOf, unhide)] : [];
      }),
    );
    const count = done.reduce((n, d) => n + d.count, 0);
    const ids = [...new Set(done.flatMap((d) => d.changed))];
    const ledger = [...new Set(done.flatMap((d) => d.ledger))];
    // A request over the server's cap is marked but returns no ids to undo; the others still can be undone.
    const capped = done.some((d) => d.overCap);
    const note = (lost.length ? " Some of it could not be marked." : "") + (capped && ids.length ? " Part of it is too large to undo." : "");
    if (ids.length === 0) {
      ticket.settle(null);
      announce(count === 0 && !lost.length ? "Nothing to mark" : `Marked ${count} as read.${note}`.trim());
      return;
    }
    ticket.settle({ ids, restore, undo: (undoIds) => undoBulk(qc, undoIds, ledger) });
    if (note) announce(`Marked ${count} as read.${note}`);
  }

  function markSide(p: RangeParams, local: string[], restore?: () => void, unhide?: (ids: string[]) => void): Promise<void> {
    return bulkMark(() => markRange(p), local, p.maxId, restore, unhide);
  }

  function markAll(scope: Scope, maxId: string | undefined, local: string[], restore?: () => void, unhide?: (ids: string[]) => void): Promise<void> {
    return bulkMark(() => markAllRead(scope, maxId), local, maxId, restore, unhide);
  }

  /**
   * Restore muted articles: un-mute them and put back the read state each had before the mute (POST
   * /api/items/unmute), so an article read before it was muted stays read and an unread one is unread again. There is
   * no undo entry: putting one back under its rule is not something the client can do, so the announcement says where
   * they went instead. Resolves to whether the server took it.
   */
  async function restoreMuted(items: Pick<Card, "id">[], restore?: () => void): Promise<boolean> {
    const ids = items.map((i) => i.id);
    if (ids.length === 0) return false;
    let res: { changed: string[]; made_unread: string[] };
    try {
      res = await api("/api/items/unmute", { method: "POST", body: { ids } });
    } catch (e) {
      toast(failed("restore those articles", e), "error");
      restore?.();
      return false;
    }
    const { changed, made_unread: unread } = res;
    patchItems(qc, changed, { muted_by: null, muted_by_name: null });
    if (unread.length) patchItems(qc, unread, { read: false });
    dropFromLists(qc, ids, (k) => k.startsWith("muted"));
    // Back in All and search (and, the unread ones, in Unread) the next time those are shown.
    invalidateLists(qc, (k) => !k.startsWith("muted"));
    if (unread.length) notifyUnread(unread);
    bumpMuted(qc, -changed.length);
    announce(restoredMessage(changed.length, unread.length));
    return true;
  }

  /** Mark all on a list made of lists (the Digest): see bulkMarkMany. */
  function markAllIn(requests: { scope: Scope; maxId: string | undefined; local: string[] }[], restore?: () => void, unhide?: (ids: string[]) => void): Promise<void> {
    return bulkMarkMany(requests.map((r) => ({ send: () => markAllRead(r.scope, r.maxId), local: r.local, asOf: r.maxId })), restore, unhide);
  }

  return { setRead, toggleRead, toggleStar, markSide, markAll, markAllIn, restoreMuted };
}
