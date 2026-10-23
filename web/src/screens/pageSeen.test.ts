import { describe, expect, it } from "vitest";
import { firstLook, keepStill, lookAgain, readerScrolling, SCROLLING_MS, type PageMeasure } from "./pageSeen";

// The list is 300 px tall from y = 0. A page is its stories' boxes within the scrolled content, [id, top, height], in
// any number of columns side by side (a box's column does not matter to the rule, only where it is). `scroll` is how
// far the list is scrolled; `shift` moves the whole content on screen without a scroll (the pull-to-refresh offset).
type Box = [string, number, number];
const measure = (scroll: number, boxes: Box[], shift = 0): PageMeasure => ({
  listTop: 0,
  listBottom: 300,
  scrollTop: scroll,
  stories: boxes.map(([id, at, h]) => ({ id, top: at - scroll + shift, bottom: at + h - scroll + shift })),
});
const column = (ids: string[], h = 100, from = 0): Box[] => ids.map((id, i) => [id, from + i * h, h]);

/** The stories passed after a series of looks, the first of which judges nothing. */
function looks(first: PageMeasure, ...then: PageMeasure[]): Set<string> {
  const passed = new Set<string>();
  let was = firstLook(first);
  for (const m of then) was = lookAgain(was, m, passed);
  return passed;
}

// Two columns. Left: "a" (0-100), then "b" (100-400), then "c". Right: "k" (0-250), then "m" (250-650).
const left = (aHeight = 100): Box[] => [["a", 0, aHeight], ["b", aHeight, 300], ["c", aHeight + 300, 300]];
const right: Box[] = [["k", 0, 250], ["m", 250, 400]];
const page = (aHeight = 100) => [...left(aHeight), ...right];

describe("judging a page's stories against the look before", () => {
  it("a story the scroll carries from in view to above the top is passed", () => {
    expect(looks(measure(0, column(["a", "b", "c", "d"])), measure(150, column(["a", "b", "c", "d"])))).toEqual(new Set(["a"]));
  });

  it("a jump passes the stories that were in view, not the ones it skipped over", () => {
    const ids = column(["a", "b", "c", "d", "e", "f", "g", "h"]);
    expect(looks(measure(0, ids), measure(600, ids))).toEqual(new Set(["a", "b", "c"]));
  });

  it("a story pushed above the top and pulled back before scrolling settles is not passed", () => {
    const ids = column(["a", "b", "c", "d"]);
    expect(looks(measure(0, ids), measure(150, ids), measure(0, ids)).size).toBe(0);
  });

  it("a look with no scroll passes nothing, however far the page moved under the reader", () => {
    // The text gets smaller, a re-plan, a story inserted above: every story moves up, two of them above the top.
    const before = column(["a", "b", "c", "d", "e"]);
    expect(looks(measure(100, before), measure(100, column(["a", "b", "c", "d", "e"], 40))).size).toBe(0);
    expect(looks(measure(100, before), measure(100, column(["b", "c", "d", "e"]))).size).toBe(0);
  });

  it("the failing-feed note above the pages going away moves every story up without a scroll: nothing is passed", () => {
    const withNote = column(["a", "b", "c", "d"], 100, 80);
    const without = column(["a", "b", "c", "d"]);
    expect(looks(measure(150, withNote), measure(150, without)).size).toBe(0);
    // The reader's next scroll is judged against where the stories are now.
    expect(looks(measure(150, withNote), measure(150, without), measure(220, without))).toEqual(new Set(["b"]));
  });

  it("the pull-to-refresh offset coming back is not a scroll", () => {
    const ids = column(["a", "b", "c"]);
    expect(looks(measure(0, ids, 120), measure(0, ids)).size).toBe(0);
  });

  it("a story in one column is passed in the look in which a story in another column opens below it", () => {
    // At 0, "k" (right, 0-250) is in view. The reader scrolls 260 px in the frame "b" (left) opens to 900 px: the left
    // column below "b" moves down, "k" did not move, and the scroll carried it above the top.
    const opened: Box[] = [["a", 0, 100], ["b", 100, 900], ["c", 1000, 300], ...right];
    expect(looks(measure(0, page()), measure(260, opened))).toEqual(new Set(["a", "k"]));
  });

  it("a story whose own box moved in the look the scroll carried it past is passed when it ends above the top", () => {
    // "b" (100-400) is in view at 200, its bottom 200 px below the top. The reader scrolls 250 px in the frame "a" above
    // it shrinks by 30 px: the scroll alone carried it past.
    expect(looks(measure(200, page()), measure(450, page(70))).has("b")).toBe(true);
    // A scroll of 150 px in the frame "a" shrinks by 60: the scroll alone would have left it in view, the move took it
    // above the top. Not passed.
    expect(looks(measure(200, page()), measure(350, page(40))).has("b")).toBe(false);
  });

  it("a story passed stays passed when the page moves after, until it is in view again", () => {
    const passed = new Set<string>();
    let was = firstLook(measure(0, page()));
    was = lookAgain(was, measure(260, page()), passed);
    expect(passed).toEqual(new Set(["a", "k"]));
    // "a" grows above the top, then "k" shrinks there: each moved, and the stories under it, but nothing scrolled.
    was = lookAgain(was, measure(260, page(220)), passed);
    expect(passed).toEqual(new Set(["a", "k"]));
    was = lookAgain(was, measure(260, [...left(220), ["k", 0, 240], ["m", 240, 400]]), passed);
    expect(passed).toEqual(new Set(["a", "k"]));
    // "a" grows on into view: only it is not passed any more.
    lookAgain(was, measure(260, page(400)), passed);
    expect(passed).toEqual(new Set(["k"]));
  });

  it("a scroll position the browser rounds still passes a story right at the top", () => {
    // The story's bottom is 2.4 px below the top; the reader scrolls 2.8 px, which reads back as 2.
    const was = firstLook({ listTop: 0, listBottom: 300, scrollTop: 0, stories: [{ id: "a", top: 0, bottom: 2.4 }, { id: "b", top: 2.4, bottom: 102.4 }] });
    const passed = new Set<string>();
    lookAgain(was, { listTop: 0, listBottom: 300, scrollTop: 2, stories: [{ id: "a", top: -2.8, bottom: -0.4 }, { id: "b", top: -0.4, bottom: 99.6 }] }, passed);
    expect(passed).toEqual(new Set(["a"]));
  });

  it("a scroll the look made itself to keep the top story still passes nothing; the same scroll by the browser would", () => {
    // Left: "a" (0-100), "b" (100-400). Right: "k" (110-250). At 100, "b" is the top story and "k" is in view under it.
    const before: Box[] = [["a", 0, 100], ["b", 100, 300], ["k", 110, 140]];
    const was = firstLook(measure(100, before));
    // "a" grows by 150 above the top with no scroll: "b" moved down 150, and the list scrolls 150 to keep it still,
    // which carries "k" (unmoved, in the other column) above the top.
    const grown: Box[] = [["a", 0, 250], ["b", 250, 300], ["k", 110, 140]];
    const by = keepStill(was, measure(100, grown));
    expect(by).toBe(150);
    const after = measure(100 + by, grown);
    const recorded = new Set<string>();
    lookAgain({ ...was, scrollTop: after.scrollTop }, after, recorded);
    expect(recorded.size).toBe(0);
    // The browser's scroll anchoring makes the same scroll without telling the look: it reads as the reader's.
    const anchored = new Set<string>();
    lookAgain(was, after, anchored);
    expect(anchored).toEqual(new Set(["k"]));
  });
  it("a story with no id is never passed", () => {
    expect(looks(measure(0, column(["a", "", "b"])), measure(250, column(["a", "", "b"])))).toEqual(new Set(["a"]));
  });

  it("sub-pixel positions that differ between frames change nothing", () => {
    const at = (d: number) => column(["a", "b", "c"]).map(([id, top, h]): Box => [id, top + d, h]);
    expect(looks(measure(0, at(0.499)), measure(150, at(0.501)), measure(160, at(0.499)))).toEqual(new Set(["a"]));
  });
});

describe("keeping the story at the top still", () => {
  const col = column(["a", "b", "c", "d", "e", "f", "g"]);

  it("the failing-feed note going away: the list scrolls back by its height, and the top story stays in view", () => {
    const withNote = column(["a", "b", "c", "d"], 100, 80);
    const without = column(["a", "b", "c", "d"]);
    const was = firstLook(measure(150, withNote));
    const by = keepStill(was, measure(150, without));
    expect(by).toBe(-80);
    const after = measure(150 + by, without);
    expect(after.stories[0]).toMatchObject({ top: -70, bottom: 30 });
  });

  it("a picture failing in the story cut off by the top keeps the story under it still", () => {
    // At 50, "a" (0-100) is cut off by the top. Its picture fails and it shrinks to 40: "b" moves up 60.
    const shrunk: Box[] = [["a", 0, 40], ...column(["b", "c", "d"], 100, 40)];
    expect(keepStill(firstLook(measure(50, col)), measure(50, shrunk))).toBe(-60);
  });

  it("a text size change, where every story changed height, keeps the first story whose top is on screen still", () => {
    // At 450, story "e" (400-500) is cut off by the top and "f" (500-600) starts 50 px below it. The stories halve: "f"
    // is at 250-300.
    expect(keepStill(firstLook(measure(450, col)), measure(450, column(["a", "b", "c", "d", "e", "f", "g"], 50)))).toBe(-250);
  });

  it("nothing to do when the stories did not move, however far the list scrolled", () => {
    expect(keepStill(firstLook(measure(150, col)), measure(150, col))).toBe(0);
    expect(keepStill(firstLook(measure(150, col)), measure(160, col))).toBe(0);
  });

  it("a change and a scroll in the same frame: the change is kept still, the scroll is left alone", () => {
    // The note (80 px) appears above the pages in the frame the reader scrolls 10 px.
    expect(keepStill(firstLook(measure(150, col)), measure(160, column(["a", "b", "c", "d", "e", "f", "g"], 100, 80)))).toBe(80);
  });

  it("a reader at the top stays at the top, and sees what appears above the stories", () => {
    const withNote = column(["a", "b", "c", "d", "e", "f", "g"], 100, 80);
    expect(keepStill(firstLook(measure(0, col)), measure(0, withNote))).toBe(0);
    expect(keepStill(firstLook(measure(0.5, col)), measure(0.5, withNote), 40)).toBe(0); // nothing owed is kept either
  });

  it("a story whose top is a rounding off the top is the one kept still", () => {
    // At 100.5, "b" starts half a pixel above the top. It grows by 50, which moves "c" under it and not "b" itself.
    const grown: Box[] = [["a", 0, 100], ["b", 100, 150], ...column(["c", "d", "e"], 100, 250)];
    expect(keepStill(firstLook(measure(100.5, col)), measure(100.5, grown))).toBe(0);
  });

  it("the story kept still removed from the page: nothing is held, and what is owed stays owed", () => {
    // At 150, "c" (200-300) is the first story whose top is in view. It is removed: "d" takes its place.
    const removed = column(["a", "b", "d", "e", "f", "g"]);
    expect(keepStill(firstLook(measure(150, col)), measure(150, removed))).toBe(0);
    expect(keepStill(firstLook(measure(150, col)), measure(150, removed), 30)).toBe(30);
  });

  it("what an earlier look owed is added to what this one finds", () => {
    expect(keepStill(firstLook(measure(150, col)), measure(150, col), 30)).toBe(30);
    expect(keepStill(firstLook(measure(150, col)), measure(150, column(["a", "b", "c", "d", "e", "f", "g"], 100, 80)), 30)).toBe(110);
  });
});

describe("the reader scrolling", () => {
  it("is a scroll in the last moments, or a finger or a button down", () => {
    const idle = { at: 0, touch: false, pointer: false };
    expect(readerScrolling(idle, 100)).toBe(true);
    expect(readerScrolling(idle, SCROLLING_MS)).toBe(false);
    expect(readerScrolling({ ...idle, touch: true }, 10_000)).toBe(true);
    expect(readerScrolling({ ...idle, pointer: true }, 10_000)).toBe(true);
  });
});