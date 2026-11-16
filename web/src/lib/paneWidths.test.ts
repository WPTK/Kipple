import { describe, expect, it } from "vitest";
import { columnNeeds, listLimit, READER_MIN_PX, wantedListWidth } from "@/lib/useWidth";
import { LIST_WIDTH_MAX, LIST_WIDTH_MIN } from "@/lib/devicePrefs";
import { RAIL_BELOW_PX } from "@/lib/useMedia";

// The shell's own arithmetic at the default text size: the main area keeps 24 px on its left (`pl-6`), the icon rail is 4rem
// wide and the open sidebar is 240 px. `total` is what the list and reader share.
const need = columnNeeds(16, false);
const RAIL_PX = 64;
const SIDEBAR_PX = 240;
const MAIN_PAD = 24;
const EDITORIAL_PX = 28 * 16;
const totalAt = (viewport: number, sidebar: number) => viewport - sidebar - MAIN_PAD;

/** The width the list column ends up with (what WidePane shows) and what is left for the reader beside it, less the strip before it. */
function split(saved: number | null, viewport: number, sidebar: number) {
  const total = totalAt(viewport, sidebar);
  const limit = listLimit(total, need, 16, Math.max(LIST_WIDTH_MIN, need.list), LIST_WIDTH_MIN, LIST_WIDTH_MAX);
  const shown = Math.min(limit, Math.max(LIST_WIDTH_MIN, wantedListWidth(saved, EDITORIAL_PX, total, viewport, need)));
  return { shown, reader: total - shown - need.gutter };
}

describe("the default list width keeps the reader a readable pane", () => {
  it("at 1024 px, with the sidebar as a rail, the reader keeps about 480 px", () => {
    const { shown, reader } = split(null, 1024, RAIL_PX);
    expect(reader).toBeGreaterThanOrEqual(READER_MIN_PX);
    expect(shown).toBeLessThan(EDITORIAL_PX);
    expect(shown).toBeGreaterThanOrEqual(need.list);
  });

  it("at 1440 px the layout's own width stands", () => {
    expect(split(null, 1440, SIDEBAR_PX).shown).toBe(EDITORIAL_PX);
  });

  it("from 1100 px up the layout's own width stands, as before, whatever the room", () => {
    expect(RAIL_BELOW_PX).toBe(1100);
    expect(split(null, 1100, SIDEBAR_PX).shown).toBe(EDITORIAL_PX);
    expect(split(null, 1099, SIDEBAR_PX).shown).toBeLessThan(EDITORIAL_PX);
  });

  it("never takes the list below what its header needs, with Large targets either", () => {
    const large = columnNeeds(16, true);
    const total = totalAt(960, SIDEBAR_PX);
    expect(wantedListWidth(null, EDITORIAL_PX, total, 960, need)).toBe(need.list);
    expect(wantedListWidth(null, EDITORIAL_PX, total, 960, large)).toBe(large.list);
  });

  it("keeps a width the person chose, wider or narrower than the default", () => {
    expect(split(300, 1024, RAIL_PX).shown).toBe(300);
    expect(split(560, 1024, RAIL_PX).shown).toBe(560);
    expect(wantedListWidth(300, EDITORIAL_PX, totalAt(1024, RAIL_PX), 1024, need)).toBe(300);
  });

  it("uses the layout's width until the pane is measured", () => {
    expect(wantedListWidth(null, EDITORIAL_PX, 0, 1024, need)).toBe(EDITORIAL_PX);
  });
});
