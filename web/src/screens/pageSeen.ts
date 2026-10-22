/**
 * Mark as read while scrolling, in a page layout (the Gazette): which stories a scroll carried above the top of the
 * list. A page has no virtual range, so each story is judged by its own box, and a story beside one still on screen, in
 * another column, is judged alone.
 *
 * The rule compares each look with the one before it. A story is scrolled past in a look when, in the look before, it
 * was in view with its bottom no further below the list's top than the list has scrolled since, and it is above the top
 * now. So the scroll alone carried it from in view to above the top, whatever else moved in between: a look in which
 * nothing scrolled passes nothing, and a story moved above the top by the page changing under it (a re-plan, a text
 * size change, a story or the failing-feed note above it growing or shrinking, the pull-to-refresh offset) is never
 * taken for one the reader scrolled past. What moved elsewhere on the page does not matter: a story is judged by its own
 * box. A story passed stays passed until it is in view again, whatever moves after.
 *
 * This holds only while the scroll position changes for no reason but a scroll. The browser's scroll anchoring (Chromium,
 * Firefox and current WebKit all have it) also changes it when content above the anchor changes height, and nothing a look can measure tells that
 * apart from the reader scrolling by as much in the same frame (pageSeen.test.ts). So the page scroller sets
 * `overflow-anchor: none`, and the browser leaves the scroll position alone when the page changes under it.
 */

/** One measurement of a page layout: the list's box, how far it is scrolled, and each story's box on screen. */
export interface PageMeasure {
  listTop: number;
  listBottom: number;
  scrollTop: number;
  stories: { id: string; top: number; bottom: number }[];
}

export function measurePage(container: HTMLElement): PageMeasure {
  const list = container.getBoundingClientRect();
  const stories: PageMeasure["stories"] = [];
  for (const a of container.querySelectorAll<HTMLElement>("article[data-item-id]")) {
    const r = a.getBoundingClientRect();
    stories.push({ id: a.dataset.itemId ?? "", top: r.top, bottom: r.bottom });
  }
  return { listTop: list.top, listBottom: list.bottom, scrollTop: container.scrollTop, stories };
}

/** What a look found: how far the list was scrolled, and the stories in view with their bottom's depth below its top. */
export interface PageLook {
  scrollTop: number;
  inView: ReadonlyMap<string, number>;
}

/** Look at the stories without judging them (the first look, or after a restore put the page somewhere new). */
export function firstLook(m: PageMeasure): PageLook {
  const inView = new Map<string, number>();
  for (const s of m.stories) if (s.id && s.bottom > m.listTop && s.top < m.listBottom) inView.set(s.id, s.bottom - m.listTop);
  return { scrollTop: m.scrollTop, inView };
}

/**
 * Look at the stories again (after a scroll, or when the pages change size), and bring `passed` up to date: a story in
 * view now is not passed (pulled back before scrolling settled), and one the scroll since `was` carried from in view
 * to above the top is. Returns this look, for the next.
 */
export function lookAgain(was: PageLook, m: PageMeasure, passed: Set<string>): PageLook {
  const now = firstLook(m);
  const scrolled = m.scrollTop - was.scrollTop;
  for (const s of m.stories) {
    if (!s.id) continue;
    if (now.inView.has(s.id)) passed.delete(s.id);
    else if (s.bottom <= m.listTop && (was.inView.get(s.id) ?? Infinity) <= scrolled) passed.add(s.id);
  }
  return now;
}
