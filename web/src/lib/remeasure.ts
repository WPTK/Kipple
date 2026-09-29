/** The parts of a (vertical) virtualizer that remeasureMounted uses. */
interface Remeasurable {
  measure: () => void;
  getVirtualItems: () => unknown;
  indexFromElement: (el: Element) => number;
  resizeItem: (index: number, size: number) => void;
}

/**
 * Drops a virtualizer's measured sizes, then measures the rows that are mounted right now.
 *
 * measure() only clears the cache. Rows already on screen do not report again (a ResizeObserver fires on a
 * change), and a measurement taken before the offsets are rebuilt is compared with the old sizes, finds no
 * change, and is thrown away, so the row would keep its estimate and overlap the ones below. Rebuilding
 * first (getVirtualItems) makes the comparison against the estimate, which is what gets corrected.
 *
 * Each row's height goes straight to resizeItem, not through measureElement: measureElement skips the
 * measurement while the virtualizer thinks the list is scrolling (for 150 ms after any scroll event), and a list
 * that has just restored its offset has just scrolled, so the rows kept their estimates and sat apart (#94).
 */
export function remeasureMounted(v: Remeasurable, scroller: HTMLElement | null) {
  v.measure();
  v.getVirtualItems();
  scroller?.querySelectorAll<HTMLElement>("[data-index]").forEach((el) => {
    const index = v.indexFromElement(el);
    if (index >= 0) v.resizeItem(index, el.offsetHeight);
  });
}
