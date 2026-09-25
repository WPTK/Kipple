import { describe, expect, it } from "vitest";
import { captureAnchor, compensate } from "./collapse";

function rect(top: number, h = 100): DOMRect {
  return { top, bottom: top + h, left: 0, right: 0, width: 0, height: h, x: 0, y: top, toJSON() {} } as DOMRect;
}

function list(tops: Record<string, number>) {
  const scroller = document.createElement("div");
  scroller.getBoundingClientRect = () => rect(0, 600);
  for (const id of Object.keys(tops)) {
    const el = document.createElement("div");
    el.dataset.itemId = id;
    el.getBoundingClientRect = () => rect(tops[id]!);
    scroller.append(el);
  }
  return { scroller, tops };
}

describe("scroll anchoring on row removal", () => {
  it("anchors on the first visible row that is not leaving", () => {
    const { scroller } = list({ a: -150, b: -20, c: 80, d: 180 });
    expect(captureAnchor(scroller, [])).toEqual({ id: "b", top: -20 });
    expect(captureAnchor(scroller, ["b"])).toEqual({ id: "c", top: 80 });
  });

  it("restores the anchor's screen position after rows above it are removed", () => {
    const { scroller, tops } = list({ c: 80, d: 180 });
    scroller.scrollTop = 500;
    const anchor = captureAnchor(scroller, ["a", "b"]);
    tops.c = -120; // two 100 px rows above vanished, content moved up
    const applied = compensate(scroller, anchor);
    expect(applied).toBe(-200);
    expect(scroller.scrollTop).toBe(300);
  });

  it("does nothing at the top of the list or when the anchor did not move", () => {
    const { scroller, tops } = list({ c: 80 });
    const anchor = captureAnchor(scroller, []);
    tops.c = -20;
    scroller.scrollTop = 0;
    expect(compensate(scroller, anchor)).toBe(0);
    scroller.scrollTop = 400;
    tops.c = 80;
    expect(compensate(scroller, anchor)).toBe(0);
  });
});
