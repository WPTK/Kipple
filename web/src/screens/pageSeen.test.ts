import { describe, expect, it } from "vitest";
import { firstLook, lookAgain, type PageMeasure } from "./pageSeen";

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

  it("scroll anchoring and a reader's scroll look the same, which is why the page turns anchoring off", () => {
    // The browser's scroll anchoring: "a" grows above the top by 150 px and the browser adds 150 px to the scroll
    // position to keep "b" still. The reader scrolled nothing, but "k" (right column, unmoved) ends above the top.
    const anchored = [measure(100, page()), measure(100 + 150 /* the browser's adjustment */, page(250))];
    // Without anchoring (a scroller that turns it off): the reader scrolls 150 px in the frame "a" grows.
    const scrolled = [measure(100, page()), measure(100 + 150 /* the reader's scroll */, page(250))];
    expect(anchored).toEqual(scrolled);
    // The look cannot tell them apart; the rule takes it as the reader's scroll.
    expect(looks(scrolled[0]!, scrolled[1]!).has("k")).toBe(true);
  });

  it("a story with no id is never passed", () => {
    expect(looks(measure(0, column(["a", "", "b"])), measure(250, column(["a", "", "b"])))).toEqual(new Set(["a"]));
  });

  it("sub-pixel positions that differ between frames change nothing", () => {
    const at = (d: number) => column(["a", "b", "c"]).map(([id, top, h]): Box => [id, top + d, h]);
    expect(looks(measure(0, at(0.499)), measure(150, at(0.501)), measure(160, at(0.499)))).toEqual(new Set(["a"]));
  });
});
