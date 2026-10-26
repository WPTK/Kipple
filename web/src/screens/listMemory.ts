import type { VirtualItem } from "@tanstack/react-virtual";
import type { Scope } from "@/api/types";
import { scopeKey } from "@/api/queries";
import { onBecameUnread } from "@/lib/itemActions";

// Scroll and selection memory per list, so "back" lands where you were
// (design 3.4: one restore path). Module scope: survives route changes.
interface ListMemory {
  offset: number;
  /** The list's `as_of` when `offset` was recorded: a mismatch (checked once the query settles, not only at
   * mount) means the list was refetched since (new arrivals, a resync, mark-all) and `offset` no longer
   * points at the rows it used to. */
  offsetAsOf?: string;
  /** The failing-feed note's height above the rows when `offset` was recorded: the note may be gone or another height
   * when the list comes back, and the rows must land where they were. */
  offsetNoteH?: number;
  /** What the rows' heights were measured for when `offset` was taken (`sizes.sig` below). */
  offsetSig?: string;
  /** Row heights measured on the last visit, and what they were measured for (layout, columns, width bucket, text
   * size). `offset` was taken over these heights: without them the rows above it come back at their estimates, so
   * the same offset lands rows away from where the reader was (#94). */
  sizes?: { sig: string; items: VirtualItem[] };
  /** The article at the top of the list when `offset` was recorded: where the list goes back to when the saved heights
   * no longer apply (the list came back at another width or text size), since `offset` then points at other rows. */
  topId?: string | null;
  selectedId?: string;
  /** A row the list must scroll to when it next mounts (the article reached by paging Next or Previous). */
  reveal?: string;
  /** Rows swiped or marked away in Unread, so leaving the list and coming back keeps them gone. */
  hidden: ReadonlySet<string>;
  /** Ids already sent by mark-read-on-scroll. */
  sentByScroll: Set<string>;
  /** Rows actually rendered in the visible window at some point (not just overscan), so a mark-on-scroll
   * settle after a remount still knows what was genuinely seen before the list was last unmounted. */
  seenByScroll: Set<string>;
}
const MEMORY_CAP = 100;

/** The article whose row is at the top of the scrolled list (rows are absolutely placed, so the highest one in view). */
export function topItemId(scroller: HTMLElement): string | null {
  const top = scroller.getBoundingClientRect().top + 1;
  let best: { id: string; y: number } | null = null;
  for (const row of scroller.querySelectorAll<HTMLElement>("[data-item-id]")) {
    const r = row.getBoundingClientRect();
    if (r.bottom <= top) continue;
    const id = row.dataset.itemId;
    if (id && (!best || r.top < best.y)) best = { id, y: r.top };
  }
  return best?.id ?? null;
}
export const memory = new Map<string, ListMemory>();
/** The page load has shown its first list, which is the only one that goes back to a remembered place. */
export const placeState = { restored: false };
export const memoryFor = (key: string): ListMemory => {
  let m = memory.get(key);
  if (!m) {
    m = { offset: 0, hidden: new Set(), sentByScroll: new Set(), seenByScroll: new Set() };
    memory.set(key, m);
    // Bounded: every scope (each partial search string too) adds an entry. Map order is insertion order, so the
    // oldest go first; a touched entry is re-inserted below to stay recent.
    while (memory.size > MEMORY_CAP) {
      const oldest = memory.keys().next().value;
      if (oldest === undefined) break;
      memory.delete(oldest);
    }
  } else {
    memory.delete(key);
    memory.set(key, m);
  }
  return m;
};

// An article that becomes unread again (undo, mark unread) while its list is not mounted must not stay hidden.
onBecameUnread((ids) => {
  for (const m of memory.values()) if (ids.some((id) => m.hidden.has(id))) m.hidden = new Set([...m.hidden].filter((x) => !ids.includes(x)));
});

/** The article reached by Next or Previous is where the list should be when the reader goes back to it. */
export function rememberListPlace(scope: Scope, id: string): void {
  const m = memoryFor(scopeKey(scope));
  m.selectedId = id;
  m.reveal = id;
}

/** Forget remembered scroll and selection (tests). */
export function clearListMemory(): void {
  memory.clear();
  placeState.restored = false;
}
