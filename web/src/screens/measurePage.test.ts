import { afterEach, describe, expect, it, vi } from "vitest";
import { measurePage } from "./pageSeen";

// A list 300 px tall from y = 0 holding sections of two stories each, 200 px apart, [section, story, story]. A box is
// stubbed by its top and height; the stubs count how many story boxes were read.

afterEach(() => document.body.replaceChildren());

const box = (top: number, height: number) => ({ top, bottom: top + height, left: 0, right: 0, width: 0, height, x: 0, y: top, toJSON: () => ({}) }) as DOMRect;

function build(sections: number, wrap = true) {
  const list = document.createElement("div");
  list.getBoundingClientRect = () => box(0, 300);
  const reads: string[] = [];
  const stories = new Map<string, HTMLElement>();
  for (let s = 0; s < sections; s++) {
    const host = wrap ? document.createElement("section") : list;
    if (wrap) {
      host.getBoundingClientRect = () => box(s * 200, 200);
      list.append(host);
    }
    for (let k = 0; k < 2; k++) {
      const id = `${s}-${k}`;
      const a = document.createElement("article");
      a.dataset.itemId = id;
      a.getBoundingClientRect = () => {
        reads.push(id);
        return box(s * 200 + k * 100, 100);
      };
      stories.set(id, a);
      host.append(a);
    }
  }
  document.body.append(list);
  return { list, reads };
}

describe("measurePage", () => {
  it("measures only the sections on screen when nothing is kept (the first look judges stories in view alone)", () => {
    const { list, reads } = build(4);
    const m = measurePage(list);
    expect(m.stories.map((s) => s.id)).toEqual(["0-0", "0-1", "1-0", "1-1"]);
    expect(reads).toHaveLength(4);
  });

  it("leaves the stories of a section off screen unmeasured, and reports the ones on screen as they are", () => {
    const { list, reads } = build(4);
    const m = measurePage(list, new Set());
    // Sections at 0-200 and 200-400 meet the list (0-300); 400-600 and 600-800 do not.
    expect(m.stories).toEqual([
      { id: "0-0", top: 0, bottom: 100 },
      { id: "0-1", top: 100, bottom: 200 },
      { id: "1-0", top: 200, bottom: 300 },
      { id: "1-1", top: 300, bottom: 400 },
    ]);
    expect(reads).toEqual(["0-0", "0-1", "1-0", "1-1"]);
    expect(m.listTop).toBe(0);
    expect(m.listBottom).toBe(300);
  });

  it("measures a kept story wherever it is", () => {
    const { list, reads } = build(4);
    const m = measurePage(list, new Set(["3-1"]));
    expect(m.stories.map((s) => s.id)).toEqual(["0-0", "0-1", "1-0", "1-1", "3-1"]);
    expect(reads).toContain("3-1");
    expect(reads).not.toContain("3-0");
  });

  it("measures a story with no section around it", () => {
    const { list, reads } = build(3, false);
    const m = measurePage(list, new Set());
    expect(m.stories).toHaveLength(6);
    expect(reads).toHaveLength(6);
  });

  it("reads a section's box once, not once per story", () => {
    const { list } = build(2);
    const section = list.querySelector("section")!;
    const read = vi.spyOn(section, "getBoundingClientRect");
    measurePage(list, new Set());
    expect(read).toHaveBeenCalledTimes(1);
  });
});
