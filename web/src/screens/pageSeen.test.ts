import { describe, expect, it } from "vitest";
import { lookAgain, type PageGeometry, type PageMeasure } from "./pageSeen";

// The list is 300 px tall from y = 0; `scroll` is how far the pages are scrolled. Each story is [id, top, height]
// within the pages.
const measure = (scroll: number, stories: [string, number, number][], width = 375): PageMeasure => ({
  listTop: 0,
  listBottom: 300,
  width,
  stories: stories.map(([id, at, h]) => ({ id, at, end: at + h, top: at - scroll, bottom: at + h - scroll })),
});
const column = (ids: string[], h = 100, from = 0): [string, number, number][] => ids.map((id, i) => [id, from + i * h, h]);

/** The seen and passed sets after a series of looks. */
function looks(...ms: PageMeasure[]) {
  const seen = new Set<string>();
  const passed = new Set<string>();
  let g: PageGeometry | null = null;
  for (const m of ms) g = lookAgain(g, m, seen, passed);
  return { seen, passed };
}

describe("judging a page's stories story by story", () => {
  it("a story scrolled past is passed", () => {
    const { seen, passed } = looks(measure(0, column(["a", "b", "c", "d", "e"])), measure(150, column(["a", "b", "c", "d", "e"])));
    expect([...passed]).toEqual(["a"]);
    expect(seen.has("e")).toBe(true);
  });

  it("a story that was never in view is not passed when a jump carries it above the top", () => {
    const ids = ["a", "b", "c", "d", "e", "f", "g", "h"];
    const { passed } = looks(measure(0, column(ids)), measure(600, column(ids)));
    expect(new Set(passed)).toEqual(new Set(["a", "b", "c"]));
  });

  it("more stories appended below keep the state of every story above them", () => {
    const before = column(["a", "b", "c", "d"]);
    const after = column(["a", "b", "c", "d", "e", "f"]);
    const { seen, passed } = looks(measure(0, before), measure(150, before), measure(160, after));
    expect([...passed]).toEqual(["a"]);
    expect(seen.has("a")).toBe(true);
  });

  it("a story inserted above moves the stories under it, which start again from what is in view", () => {
    const before = column(["a", "b", "c", "d"]);
    // A new story at the top pushes every other one down by its height.
    const after = column(["new", "a", "b", "c", "d"]);
    const { seen, passed } = looks(measure(0, before), measure(150, before), measure(150, after));
    // "a" was passed, but it moved: it is judged again where it is now (in view), not taken as scrolled past. The new
    // story is above the top and was never in view.
    expect(passed.size).toBe(0);
    expect(new Set(seen)).toEqual(new Set(["a", "b", "c", "d"]));
  });

  it("a moved story that lands above the top is not seen, and a later scroll does not pass it", () => {
    const before = column(["a", "b", "c", "d", "e", "f"]);
    const after = column(["a", "b", "c", "d", "e", "f"], 50); // the text got smaller: every story but the first moved
    const { seen, passed } = looks(measure(0, before), measure(250, after), measure(260, after));
    expect(passed.size).toBe(0);
    expect(seen.has("b")).toBe(false);
  });

  it("a story that stayed put keeps its state while the stories that moved start again", () => {
    // Two columns side by side: "a" and "b" in the first, "x" and "y" in the second.
    const before: [string, number, number][] = [["a", 0, 100], ["x", 0, 100], ["b", 100, 400], ["y", 100, 400]];
    // A re-plan makes the second column's stories taller; the first column stays where it was.
    const after: [string, number, number][] = [["a", 0, 100], ["x", 0, 180], ["b", 100, 400], ["y", 180, 400]];
    const { seen, passed } = looks(measure(0, before), measure(150, before), measure(150, after));
    expect(passed.has("a")).toBe(true);
    // "x" was passed too, but its box changed and it is in view again.
    expect(passed.has("x")).toBe(false);
    expect(seen.has("x")).toBe(true);
  });

  it("a story above that grows, with the scroll moved by as much in the same look, passes nothing that stayed", () => {
    // Two columns: "a" (0-100) and "b" (100-400) on the left, "k" (0-400) on the right.
    const before: [string, number, number][] = [["a", 0, 100], ["k", 0, 400], ["b", 100, 300]];
    // "a" grows by 150 and the browser's scroll anchoring adds 150 to the scroll position: "k" stayed in the content,
    // but on screen it now ends above the top, though the reader never scrolled past it.
    const after: [string, number, number][] = [["a", 0, 250], ["k", 0, 400], ["b", 250, 300]];
    const { seen, passed } = looks(measure(0, before), measure(300, before), measure(450, after));
    expect(passed.size).toBe(0);
    expect(seen.has("k")).toBe(false);
    // A story the reader did scroll past before the move stays passed.
    const earlier = looks(measure(0, before), measure(450, before), measure(600, after));
    expect(earlier.passed.has("k")).toBe(true);
  });

  it("after a look in which the page moved, the next scroll judges the stories that stayed again", () => {
    const before: [string, number, number][] = [["a", 0, 100], ["k", 0, 400], ["b", 100, 300]];
    const after: [string, number, number][] = [["a", 0, 250], ["k", 0, 400], ["b", 250, 300]];
    // Seen again in view after the move ("k" ends at 400, the list shows 350 to 650), then scrolled past.
    const { passed } = looks(measure(0, before), measure(300, before), measure(350, after), measure(450, after));
    expect(passed.has("k")).toBe(true);
  });

  it("a story with no id is never tracked, and does not count as a move", () => {
    const ids = column(["a", "", "b", "c"]);
    const later = column(["a", "", "b", "c"]).map(([id, at, h]): [string, number, number] => (id ? [id, at, h] : [id, at, h + 50]));
    const { seen, passed } = looks(measure(0, ids), measure(150, later));
    expect(seen.has("")).toBe(false);
    expect(passed.has("")).toBe(false);
    expect([...passed]).toEqual(["a"]);
  });

  it("another width starts every story again", () => {
    const ids = column(["a", "b", "c", "d"]);
    const { passed } = looks(measure(0, ids), measure(150, ids), measure(150, ids, 600));
    expect(passed.size).toBe(0);
  });

  it("a story gone from the page is forgotten", () => {
    const { seen, passed } = looks(measure(0, column(["a", "b", "c"])), measure(150, column(["a", "b", "c"])), measure(150, column(["a", "b"])));
    expect(seen.has("c")).toBe(false);
    expect([...passed]).toEqual(["a"]);
  });

  it("positions a fraction apart from frame to frame are the same place, and never add up to a missed move", () => {
    const shift = (d: number) => column(["a", "b", "c"]).map(([id, at, h]): [string, number, number] => [id, at + d, h]);
    // Each frame is within a pixel of the last, but the third is two pixels from where the state was seeded.
    const drift = looks(measure(0, shift(0)), measure(150, shift(0.9)), measure(150, shift(1.8)));
    expect(drift.passed.size).toBe(0);
    const jitter = looks(measure(0, shift(0.499)), measure(150, shift(0.501)), measure(160, shift(0.499)));
    expect([...jitter.passed]).toEqual(["a"]);
  });
});
