import { describe, expect, it } from "vitest";
import { clickRow, groupState, rangeBetween, toggleGroup, toggleIn } from "./selection";

const order = ["a", "b", "c", "d", "e"];

describe("feed multi-select", () => {
  it("toggles one row and makes it the anchor", () => {
    const r = clickRow(order, new Set(), null, "b", false);
    expect([...r.sel]).toEqual(["b"]);
    expect(r.anchor).toBe("b");
    expect([...toggleIn(r.sel, "b")]).toEqual([]);
  });

  it("shift-click ticks the range from the anchor, in either direction", () => {
    const first = clickRow(order, new Set(), null, "b", false);
    const down = clickRow(order, first.sel, first.anchor, "d", true);
    expect([...down.sel].sort()).toEqual(["b", "c", "d"]);
    expect(down.anchor).toBe("d");
    const up = clickRow(order, first.sel, first.anchor, "a", true);
    expect([...up.sel].sort()).toEqual(["a", "b"]);
    expect(rangeBetween(order, "d", "b")).toEqual(["b", "c", "d"]);
    expect(rangeBetween(order, "d", "zzz")).toEqual([]);
  });

  it("shift-click after unticking the anchor clears the range", () => {
    const all = new Set(order);
    const cleared = clickRow(order, all, null, "b", false); // b off, anchor b
    const r = clickRow(order, cleared.sel, cleared.anchor, "d", true);
    expect([...r.sel].sort()).toEqual(["a", "e"]);
  });

  it("shift without an anchor is a plain click", () => {
    expect([...clickRow(order, new Set(), null, "c", true).sel]).toEqual(["c"]);
  });

  it("a folder checkbox is tri-state and selects or clears the whole folder", () => {
    const ids = ["a", "b", "c"];
    expect(groupState(ids, new Set())).toBe("none");
    expect(groupState(ids, new Set(["a"]))).toBe("some");
    expect(groupState(ids, new Set(ids))).toBe("all");
    expect(groupState([], new Set(["x"]))).toBe("none");
    expect([...toggleGroup(ids, new Set(["a", "z"]))].sort()).toEqual(["a", "b", "c", "z"]);
    expect([...toggleGroup(ids, new Set([...ids, "z"]))]).toEqual(["z"]);
  });
});
