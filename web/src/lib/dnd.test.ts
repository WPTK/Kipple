import { describe, expect, it } from "vitest";
import { arrayMove, dropSlot, insertBefore, planFeedDrop, planFolderDrop, reorderBody, type Tree } from "./dnd";

const tree: Tree = { folders: ["1", "2", "3"], feeds: { "1": ["a", "b", "c"], "2": ["d"], "3": [] } };

describe("drag reorder planning", () => {
  it("arrayMove and insertBefore", () => {
    expect(arrayMove(["a", "b", "c"], 2, 0)).toEqual(["c", "a", "b"]);
    expect(arrayMove(["a", "b", "c"], 0, 9)).toEqual(["b", "c", "a"]);
    expect(arrayMove(["a"], 5, 0)).toEqual(["a"]);
    expect(insertBefore(["a", "b", "c"], "c", "a")).toEqual(["c", "a", "b"]);
    expect(insertBefore(["a", "b", "c"], "a", null)).toEqual(["b", "c", "a"]);
    expect(insertBefore(["a", "b"], "z", "b")).toEqual(["a", "z", "b"]);
  });

  it("a folder dropped before another reorders the folders only", () => {
    const next = planFolderDrop(tree, "3", "1");
    expect(next.folders).toEqual(["3", "1", "2"]);
    expect(reorderBody(tree, next)).toEqual({ folders: ["3", "1", "2"] });
  });

  it("a feed dropped inside its folder sends that folder's list alone", () => {
    const next = planFeedDrop(tree, "c", { folder: "1", before: "a" });
    expect(reorderBody(tree, next)).toEqual({ feeds: [{ folder_id: "1", ids: ["c", "a", "b"] }] });
  });

  it("a feed dropped into another folder moves there: both lists are sent", () => {
    const next = planFeedDrop(tree, "b", { folder: "2", before: "d" });
    expect(next.feeds).toEqual({ "1": ["a", "c"], "2": ["b", "d"], "3": [] });
    expect(reorderBody(tree, next)).toEqual({
      feeds: [
        { folder_id: "1", ids: ["a", "c"] },
        { folder_id: "2", ids: ["b", "d"] },
      ],
    });
  });

  it("a feed dropped into an empty folder, or at the end of one", () => {
    expect(planFeedDrop(tree, "a", { folder: "3", before: null }).feeds["3"]).toEqual(["a"]);
    expect(planFeedDrop(tree, "a", { folder: "2", before: null }).feeds["2"]).toEqual(["d", "a"]);
  });

  it("dropping where it already is changes nothing (no request)", () => {
    expect(reorderBody(tree, planFeedDrop(tree, "b", { folder: "1", before: "c" }))).toBeNull();
    expect(reorderBody(tree, planFolderDrop(tree, "2", "3"))).toBeNull();
  });

  it("dropSlot puts the pointer before the first row whose middle is below it", () => {
    const rows = [
      { id: "a", top: 0, bottom: 40 },
      { id: "b", top: 40, bottom: 80 },
    ];
    expect(dropSlot(rows, 10)).toEqual({ before: "a" });
    expect(dropSlot(rows, 30)).toEqual({ before: "b" });
    expect(dropSlot(rows, 70)).toEqual({ before: null });
    expect(dropSlot([], 5)).toEqual({ before: null });
  });
});
