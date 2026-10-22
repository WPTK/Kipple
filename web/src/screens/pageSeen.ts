/**
 * Mark as read while scrolling, in a page layout (the Gazette): which stories the reader has seen, and which of those
 * a scroll carried above the top. A page has no virtual range, so each story is judged by its own box, and a story
 * beside one still on screen, in another column, is judged alone.
 */

/**
 * One measurement of a page layout: the list's box, the pages' width, and each story in DOM order with its box (`top`
 * and `bottom` on screen, `at` and `end` its top and bottom within the pages). Read once per look.
 */
export interface PageMeasure {
  listTop: number;
  listBottom: number;
  width: number;
  stories: { id: string; top: number; bottom: number; at: number; end: number }[];
}

export function measurePage(container: HTMLElement, box: HTMLElement | null): PageMeasure {
  const list = container.getBoundingClientRect();
  const pages = box ? box.getBoundingClientRect() : list;
  const stories: PageMeasure["stories"] = [];
  for (const a of container.querySelectorAll<HTMLElement>("article[data-item-id]")) {
    const r = a.getBoundingClientRect();
    stories.push({ id: a.dataset.itemId ?? "", top: r.top, bottom: r.bottom, at: r.top - pages.top, end: r.bottom - pages.top });
  }
  return { listTop: list.top, listBottom: list.bottom, width: pages.width, stories };
}

/** Where each story's box was within the pages when its seen and passed state was last seeded, and the pages' width. */
export interface PageGeometry {
  width: number;
  boxes: ReadonlyMap<string, { at: number; end: number }>;
}

/**
 * Two lengths are the same place on the page. Positions are fractional (device pixel ratio, sub-pixel scroll offsets),
 * and a box's position within the pages can come back a fraction apart between frames without anything moving; a real
 * reflow moves a story by at least a pixel.
 */
const samePx = (a: number, b: number) => Math.abs(a - b) < 1;

/**
 * Look at the stories (after a scroll, or when the pages change size) and bring `seen` and `passed` up to date, story
 * by story. A story whose box is where it was when its state was seeded (`was`) keeps its state and is judged where it
 * is now: one in view is seen, one seen earlier whose box now ends above the list's top was scrolled past. A story that
 * moved under the reader (another plan, a story inserted above it, a text size change), or is new, starts again from
 * what is in view: seen if it is in view now, never passed. So more pages appended below keep every story above them,
 * and a story that only ends up above the top because the page reflowed is never taken for one the reader scrolled
 * past. Another width (or no `was`) starts every story again; a story no longer on the page is forgotten.
 *
 * Returns the geometry for the next look. A kept story keeps the position it was seeded at, not this frame's, so
 * positions that differ by a fraction from frame to frame never add up to a missed reflow.
 */
export function lookAgain(was: PageGeometry | null, m: PageMeasure, seen: Set<string>, passed: Set<string>): PageGeometry {
  const prior = was && samePx(was.width, m.width) ? was.boxes : null;
  const boxes = new Map<string, { at: number; end: number }>();
  for (const s of m.stories) {
    if (!s.id) continue;
    const then = prior?.get(s.id);
    const kept = !!then && samePx(then.at, s.at) && samePx(then.end, s.end);
    boxes.set(s.id, kept && then ? then : { at: s.at, end: s.end });
    if (!kept) {
      seen.delete(s.id);
      passed.delete(s.id);
    }
    if (s.bottom > m.listTop && s.top < m.listBottom) {
      // On screen again (pulled back before scrolling settled): not passed after all.
      seen.add(s.id);
      passed.delete(s.id);
    } else if (kept && s.bottom <= m.listTop && seen.has(s.id)) passed.add(s.id);
  }
  for (const id of seen) if (!boxes.has(id)) seen.delete(id);
  for (const id of passed) if (!boxes.has(id)) passed.delete(id);
  return { width: m.width, boxes };
}
