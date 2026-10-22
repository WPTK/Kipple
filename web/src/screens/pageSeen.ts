/**
 * A page layout (the Gazette), look by look: which stories a scroll carried above the top of the list (mark as read
 * while scrolling), and how far to scroll so the story at the top stays where it was when the page changes under the
 * reader (keepStill). A page has no virtual range, so each story is judged by its own box, and a story beside one still
 * on screen, in another column, is judged alone.
 *
 * Each look is compared with the one before it. A story is scrolled past in a look when, in the look before, it was in
 * view with its bottom no further below the list's top than the list has scrolled since, and it is above the top now.
 * So the scroll alone carried it from in view to above the top, whatever else moved in between: a look in which nothing
 * scrolled passes nothing, and a story moved above the top by the page changing under it (a re-plan, a text size
 * change, a story or the failing-feed note above it growing or shrinking, the pull-to-refresh offset) is never taken
 * for one the reader scrolled past. What moved elsewhere on the page does not matter: a story is judged by its own box.
 * A story passed stays passed until it is in view again, whatever moves after.
 *
 * This holds only while the scroll position changes for no reason the look does not know. The browser's scroll
 * anchoring (Chromium, Firefox and current WebKit) changes it when content above the anchor changes height, and nothing
 * a look can measure tells that apart from the reader scrolling by as much in the same frame. So the page scroller sets
 * `overflow-anchor: none` and keeps the place itself: keepStill says how far the page moved the story at the top, the
 * list scrolls by as much, and that look counts only the rest of the change in scroll position as the reader's. While
 * the reader is scrolling (or has a finger or button down on the list) the list does not scroll itself, which would
 * stop a fling: what is owed is scrolled once the scrolling stops.
 */

/** How long after the reader's last scroll they count as still scrolling (the frames of a fling come closer). */
export const SCROLLING_MS = 150;

/** The reader's hold on the list: when they last scrolled it, and whether a finger or a button is down on it. */
export interface ReaderInput {
  at: number;
  touch: boolean;
  pointer: boolean;
}

export const readerScrolling = (s: ReaderInput, now = performance.now()) => s.touch || s.pointer || now - s.at < SCROLLING_MS;

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

/** What a look found: how far the list was scrolled, and the stories in view, their boxes measured from its top. */
export interface PageLook {
  scrollTop: number;
  inView: ReadonlyMap<string, { top: number; bottom: number }>;
}

/** Look at the stories without judging them (the first look, or after a restore put the page somewhere new). */
export function firstLook(m: PageMeasure): PageLook {
  const inView = new Map<string, { top: number; bottom: number }>();
  for (const s of m.stories) {
    if (s.id && s.bottom > m.listTop && s.top < m.listBottom) inView.set(s.id, { top: s.top - m.listTop, bottom: s.bottom - m.listTop });
  }
  return { scrollTop: m.scrollTop, inView };
}

/**
 * Slack for a scroll position the browser rounds while boxes stay fractional (zoom, device pixel ratio): a story whose
 * bottom was 2.4 px below the top, scrolled past by a scroll of 2.8 px that reads back as 2, is still passed.
 */
const ROUNDING = 1;

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
    else if (scrolled > 0 && s.bottom <= m.listTop && (was.inView.get(s.id)?.bottom ?? Infinity) <= scrolled + ROUNDING) passed.add(s.id);
  }
  return now;
}

/**
 * How far to scroll so the story at the top stays where it was: how far the page moved it since `was`, plus `owed`,
 * what earlier looks found but could not scroll (the reader was scrolling then). A story is measured by its place on
 * the page (its top plus the scroll position), which a scroll does not change, so a change and a scroll in the same
 * frame are told apart. The story kept still is the highest one in `was` whose top was in view, where the reader's
 * eye is: keeping still the top of one cut off by the top would move the part of it the reader sees when it changes
 * height (its own picture failing). Only when no story's top was in view (one story fills the screen), the one cut off
 * by the top. A story's own height never moves its top, so the one kept still may have changed height too (a text size
 * change changes them all). Nothing is kept still when that story is gone (removed from the page; the reader is looking
 * at what took its place) or the list is at its top: a reader at the top stays there, and sees what appears above.
 */
export function keepStill(was: PageLook, m: PageMeasure, owed = 0): number {
  if (m.scrollTop < 1) return 0;
  const candidates = [...was.inView].sort(([, a], [, b]) => a.top - b.top);
  const pick = candidates.find(([, b]) => b.top >= -ROUNDING) ?? candidates[0];
  const story = pick && m.stories.find((s) => s.id === pick[0]);
  if (!pick || !story) return owed;
  return owed + story.top - m.listTop + m.scrollTop - (pick[1].top + was.scrollTop);
}
