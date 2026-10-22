/**
 * Mark as read while scrolling, in a page layout (the Gazette): which stories the reader has seen, and which of those
 * a scroll carried above the top. A page has no virtual range, so each story is judged by its own box, and a story
 * beside one still on screen, in another column, is judged alone.
 */

/**
 * One measurement of a page layout: the list's box, the pages' width, and each story in DOM order with its box (`top`
 * and `bottom` on screen, `at` and `end` its top and bottom within the list's scrolled content). Read once per look.
 *
 * `at` and `end` are measured from the top of the scrolled content, not from the pages' own box: anything above the
 * pages (the failing-feed note) that changes height moves the stories on screen without a scroll, and measured this way
 * that is a move like any other.
 */
export interface PageMeasure {
  listTop: number;
  listBottom: number;
  width: number;
  stories: { id: string; top: number; bottom: number; at: number; end: number }[];
}

export function measurePage(container: HTMLElement, box: HTMLElement | null): PageMeasure {
  const list = container.getBoundingClientRect();
  const origin = list.top - container.scrollTop;
  const stories: PageMeasure["stories"] = [];
  for (const a of container.querySelectorAll<HTMLElement>("article[data-item-id]")) {
    const r = a.getBoundingClientRect();
    stories.push({ id: a.dataset.itemId ?? "", top: r.top, bottom: r.bottom, at: r.top - origin, end: r.bottom - origin });
  }
  return { listTop: list.top, listBottom: list.bottom, width: (box ? box.getBoundingClientRect() : list).width, stories };
}

/** Where each story's box was in the content when its seen and passed state was last seeded, and the pages' width. */
export interface PageGeometry {
  width: number;
  boxes: ReadonlyMap<string, { at: number; end: number }>;
}

/**
 * Two lengths are the same place on the page. Positions are fractional (device pixel ratio, sub-pixel scroll offsets),
 * and a box's position within the content can come back a fraction apart between frames without anything moving; a
 * real reflow moves a story by at least a pixel.
 */
const samePx = (a: number, b: number) => Math.abs(a - b) < 1;

/**
 * Look at the stories (after a scroll, or when the pages change size) and bring `seen` and `passed` up to date, story
 * by story. A story whose box is where it was when its state was seeded (`was`) keeps its state; a story that moved
 * under the reader (another plan, a story inserted above it, a text size change), or is new, starts again from what is
 * in view: seen if it is in view now, never passed. So more pages appended below keep every story above them, and a
 * story that only ends up above the top because the page reflowed is never taken for one the reader scrolled past.
 * Another width (or no `was`) starts every story again; a story no longer on the page is forgotten.
 *
 * In a look where nothing moved, a story that stayed is judged where it is: one in view is seen, one seen earlier whose
 * box now ends above the list's top was scrolled past. In a look where some story moved, the scroll position may have
 * moved with it (the browser's scroll anchoring keeps the content in view still when something above it grows), so a
 * story that stayed in the content can still have moved on screen without the reader scrolling: none is taken as
 * passed in that look. One already passed stays passed (unless it is in view again), and the others' seen state starts
 * again from what is in view.
 *
 * Returns the geometry for the next look. A kept story keeps the position it was seeded at, not this frame's, so
 * positions that differ by a fraction from frame to frame never add up to a missed reflow.
 */
export function lookAgain(was: PageGeometry | null, m: PageMeasure, seen: Set<string>, passed: Set<string>): PageGeometry {
  const prior = was && samePx(was.width, m.width) ? was.boxes : null;
  const looked = m.stories.flatMap((s) => {
    if (!s.id) return []; // a story without an id cannot be marked, and is not tracked
    const then = prior?.get(s.id);
    return [{ s, then, kept: !!then && samePx(then.at, s.at) && samePx(then.end, s.end) }];
  });
  // A story that was there before and is somewhere else now: the page moved under the reader.
  const moved = !prior || looked.some((l) => l.then && !l.kept);
  const boxes = new Map<string, { at: number; end: number }>();
  for (const { s, then, kept } of looked) {
    boxes.set(s.id, kept && then ? then : { at: s.at, end: s.end });
    const inView = s.bottom > m.listTop && s.top < m.listBottom;
    if (!kept) passed.delete(s.id);
    if (!kept || (moved && !passed.has(s.id))) seen.delete(s.id);
    if (inView) {
      // On screen again (pulled back before scrolling settled): not passed after all.
      seen.add(s.id);
      passed.delete(s.id);
    } else if (kept && !moved && s.bottom <= m.listTop && seen.has(s.id)) passed.add(s.id);
  }
  for (const id of seen) if (!boxes.has(id)) seen.delete(id);
  for (const id of passed) if (!boxes.has(id)) passed.delete(id);
  return { width: m.width, boxes };
}
