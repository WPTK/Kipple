import { describe, expect, it } from "vitest";
import { editorialRowHeight, magazine } from "./magazine";
import { card } from "@/test/mockApi";

describe("Editorial row height estimate", () => {
  it("is about 400 px stacked at phone width, as before, and text-only rows stay 190", () => {
    expect(editorialRowHeight(true, 375)).toBeGreaterThan(380);
    expect(editorialRowHeight(true, 375)).toBeLessThan(420);
    expect(editorialRowHeight(true, 0)).toBe(editorialRowHeight(true, 375));
    expect(editorialRowHeight(false, 900)).toBe(190);
  });

  it("drops to about 200 to 260 px once the image sits beside the text (container 34rem)", () => {
    for (const w of [560, 700, 900]) {
      const h = editorialRowHeight(true, w);
      expect(h).toBeGreaterThanOrEqual(200);
      expect(h).toBeLessThanOrEqual(280);
    }
    // Just under the breakpoint it is still stacked, so it is tall.
    expect(editorialRowHeight(true, 540)).toBeGreaterThan(400);
  });

  it("follows the root font size, because the breakpoint is in rem", () => {
    expect(editorialRowHeight(true, 600, 16)).toBeLessThan(300);
    expect(editorialRowHeight(true, 600, 20)).toBeGreaterThan(400);
  });

  it("is what the layout returns for the list width it is given", () => {
    const item = card(1, { image: "https://e.com/i.jpg" });
    expect(magazine.estimateRow(item, { width: 800, rem: 16 })).toBe(editorialRowHeight(true, 800));
    expect(magazine.estimateRow(item)).toBe(editorialRowHeight(true, 0));
  });
});
