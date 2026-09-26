// Rows that leave the Unread list collapse over COLLAPSE_MS. Rows scrolled out of view above the
// viewport cannot animate (the virtualizer has unmounted them), so when a batch of rows is removed
// the first still-visible row is pinned to where it was on screen: the list never jumps under the
// reader's thumb.

export const COLLAPSE_MS = 180;

export interface ScrollAnchor {
  id: string;
  top: number;
}

/** The first row fully or partly in view that is not among `leaving`, with its screen position. */
export function captureAnchor(scroller: HTMLElement, leaving: Iterable<string>): ScrollAnchor | null {
  const gone = new Set(leaving);
  const box = scroller.getBoundingClientRect();
  for (const el of scroller.querySelectorAll<HTMLElement>("[data-item-id]")) {
    const id = el.dataset.itemId;
    if (!id || gone.has(id)) continue;
    const r = el.getBoundingClientRect();
    if (r.bottom > box.top) return { id, top: r.top - box.top };
  }
  return null;
}

/** Scroll so `anchor` sits where it was; returns the applied delta (0 when nothing to do). */
export function compensate(scroller: HTMLElement, anchor: ScrollAnchor | null): number {
  if (!anchor || scroller.scrollTop <= 0) return 0;
  const el = [...scroller.querySelectorAll<HTMLElement>("[data-item-id]")].find((e) => e.dataset.itemId === anchor.id);
  if (!el) return 0;
  const now = el.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
  const delta = now - anchor.top;
  if (Math.abs(delta) < 1) return 0;
  const before = scroller.scrollTop;
  scroller.scrollTop = Math.max(0, before + delta);
  return scroller.scrollTop - before;
}
