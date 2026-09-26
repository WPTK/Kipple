/**
 * Drops a virtualizer's measured sizes, then measures the rows that are mounted right now.
 *
 * measure() only clears the cache. Rows already on screen do not report again (a ResizeObserver fires on a
 * change), and a measurement taken before the offsets are rebuilt is compared with the old sizes, finds no
 * change, and is thrown away, so the row would keep its estimate and overlap the ones below. Rebuilding
 * first (getVirtualItems) makes the comparison against the estimate, which is what gets corrected.
 */
export function remeasureMounted(
  v: { measure: () => void; getVirtualItems: () => unknown; measureElement: (el: Element | null) => void },
  scroller: HTMLElement | null,
) {
  v.measure();
  v.getVirtualItems();
  scroller?.querySelectorAll<HTMLElement>("[data-index]").forEach((el) => v.measureElement(el));
}
