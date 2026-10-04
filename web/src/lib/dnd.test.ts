import { describe, expect, it } from "vitest";
import { arrayMove, dropSlot, folderSlot, insertBefore, parentChanges, planFeedDrop, planFolderDrop, reorderBody, stepFolder, type Tree } from "./dnd";

const tree: Tree = { folders: ["1", "2", "3"], parents: { "1": null, "2": null, "3": null }, feeds: { "1": ["a", "b", "c"], "2": ["d"], "3": [] } };

// T > (A > (M), B), then S, all in tree order.
const nested: Tree = {
  folders: ["T", "A", "M", "B", "S"],
  parents: { T: null, A: "T", M: "A", B: "T", S: null },
  feeds: { T: [], A: [], M: [], B: [], S: [] },
};

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
    const next = planFolderDrop(tree, "3", { parent: null, before: "1" });
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
    expect(reorderBody(tree, planFolderDrop(tree, "2", { parent: null, before: "3" }))).toBeNull();
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

describe("nested folder moves", () => {
  it("a folder dropped inside another moves with its subtree to the end of that folder", () => {
    const next = planFolderDrop(nested, "A", { parent: "S", before: null });
    expect(next.folders).toEqual(["T", "B", "S", "A", "M"]);
    expect(next.parents.A).toBe("S");
    expect(next.parents.M).toBe("A");
    expect(parentChanges(nested, next)).toEqual([{ id: "A", parent: "S" }]);
    expect(reorderBody(nested, next)).toEqual({ folders: ["T", "B", "S", "A", "M"] });
  });

  it("a folder dropped at the end of a parent goes after the parent's last descendant", () => {
    const next = planFolderDrop(nested, "S", { parent: "T", before: null });
    expect(next.folders).toEqual(["T", "A", "M", "B", "S"]);
    expect(next.parents.S).toBe("T");
    const top = planFolderDrop(nested, "M", { parent: null, before: "T" });
    expect(top.folders).toEqual(["M", "T", "A", "B", "S"]);
    expect(top.parents.M).toBeNull();
  });

  it("never into itself or one of its subfolders, and never before a folder of another parent", () => {
    expect(planFolderDrop(nested, "T", { parent: "M", before: null })).toBe(nested);
    expect(planFolderDrop(nested, "T", { parent: "T", before: null })).toBe(nested);
    expect(planFolderDrop(nested, "S", { parent: "T", before: "M" })).toBe(nested);
  });

  it("steps among its siblings only", () => {
    expect(stepFolder(nested, "B", -1).folders).toEqual(["T", "B", "A", "M", "S"]);
    expect(stepFolder(nested, "A", 1).folders).toEqual(["T", "B", "A", "M", "S"]);
    expect(stepFolder(nested, "A", -1)).toBe(nested); // first child
    expect(stepFolder(nested, "T", 1).folders).toEqual(["S", "T", "A", "M", "B"]);
    expect(parentChanges(nested, stepFolder(nested, "T", 1))).toEqual([]);
  });

  it("folderSlot: the middle of a row is inside it, its edges are before or after it", () => {
    // T (0-40) > A (40-80), then S (80-120).
    const rows = [
      { id: "T", group: "", top: 0, bottom: 40, into: true },
      { id: "A", group: "T", top: 40, bottom: 80, into: true },
      { id: "S", group: "", top: 80, bottom: 120, into: false },
    ];
    expect(folderSlot(rows, 5)).toEqual({ group: "", before: "T" });
    expect(folderSlot(rows, 20)).toEqual({ group: "T", before: null });
    expect(folderSlot(rows, 35)).toEqual({ group: "", before: "S" }); // after T, past its subtree
    expect(folderSlot(rows, 75)).toEqual({ group: "T", before: null }); // after A: the end of T
    expect(folderSlot(rows, 100)).toEqual({ group: "", before: null }); // S takes no subfolders: after it
    expect(folderSlot(rows, 90)).toEqual({ group: "", before: "S" });
    expect(folderSlot(rows, 500)).toEqual({ group: "", before: null });
  });
});
