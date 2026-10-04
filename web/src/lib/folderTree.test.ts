import { describe, expect, it } from "vitest";
import { MAX_FOLDER_DEPTH, chainOf, childrenOf, depthOf, feedOrder, folderPath, folderTree, heightOf, parentChoices, parentPath, rollUp, subtreeOf } from "./folderTree";

// Uncategorized (default); Tech > (Apple > Mac, News); Sports > News. Listed in pre-order, as the bootstrap does.
const folders = [
  { id: "1", name: "Uncategorized", parent_id: null, is_default: true },
  { id: "2", name: "Tech", parent_id: null },
  { id: "3", name: "Apple", parent_id: "2" },
  { id: "4", name: "Mac", parent_id: "3" },
  { id: "5", name: "News", parent_id: "2" },
  { id: "6", name: "Sports", parent_id: null },
  { id: "7", name: "News", parent_id: "6" },
];
const t = folderTree(folders);

describe("folderTree", () => {
  it("keeps the listed order as pre-order and knows each folder's children", () => {
    expect(t.preorder).toEqual(["1", "2", "3", "4", "5", "6", "7"]);
    expect(childrenOf(t, null)).toEqual(["1", "2", "6"]);
    expect(childrenOf(t, "2")).toEqual(["3", "5"]);
    expect(childrenOf(t, "4")).toEqual([]);
  });

  it("builds pre-order from parents even when the list is not in it", () => {
    const shuffled = folderTree([folders[3]!, folders[6]!, folders[1]!, folders[5]!, folders[2]!]);
    expect(shuffled.preorder).toEqual(["2", "3", "4", "6", "7"]);
  });

  it("puts a folder whose parent is missing (or absent) at the top level", () => {
    const old = folderTree([{ id: "1", name: "A" }, { id: "2", name: "B", parent_id: "99" }]);
    expect(childrenOf(old, null)).toEqual(["1", "2"]);
  });

  it("chains, depth, subtree, height and paths", () => {
    expect(chainOf(t, "4")).toEqual(["4", "3", "2"]);
    expect(depthOf(t, "4")).toBe(3);
    expect(depthOf(t, "6")).toBe(1);
    expect([...subtreeOf(t, "2")]).toEqual(["2", "3", "4", "5"]);
    expect(heightOf(t, "2")).toBe(3);
    expect(heightOf(t, "4")).toBe(1);
    expect(folderPath(t, "4")).toBe("Tech › Apple › Mac");
    expect(parentPath(t, "4")).toBe("Tech › Apple");
    expect(parentPath(t, "2")).toBe("");
    // Same name, different parents: the paths tell them apart.
    expect(folderPath(t, "5")).not.toBe(folderPath(t, "7"));
  });

  it("rolls counts up over each subtree", () => {
    const own = new Map([
      ["2", 1],
      ["3", 2],
      ["4", 4],
      ["7", 8],
    ]);
    const up = rollUp(t, own);
    expect(up.get("2")).toBe(7);
    expect(up.get("3")).toBe(6);
    expect(up.get("6")).toBe(8);
    expect(up.get("1")).toBe(0);
  });

  it("parentChoices leaves out the default folder, the folder's own subtree and too-deep places", () => {
    expect(parentChoices(t, null)).toEqual(["2", "3", "4", "5", "6", "7"]);
    expect(parentChoices(t, "2")).toEqual(["6", "7"]);
    expect(parentChoices(t, "3")).toEqual(["2", "5", "6", "7"]);
    // A chain eight deep: nothing more fits below its last folder.
    const deep = folderTree(Array.from({ length: MAX_FOLDER_DEPTH }, (_, i) => ({ id: `d${i}`, name: `L${i}`, parent_id: i ? `d${i - 1}` : null })));
    expect(parentChoices(deep, null)).not.toContain(`d${MAX_FOLDER_DEPTH - 1}`);
    expect(parentChoices(deep, null)).toContain(`d${MAX_FOLDER_DEPTH - 2}`);
  });

  it("feedOrder lists each folder's subfolders before its own feeds", () => {
    const feeds: Record<string, string[]> = { "1": ["u"], "2": ["t"], "3": ["a"], "4": ["m"], "6": ["s"] };
    expect(feedOrder(t, (id) => feeds[id] ?? [])).toEqual(["u", "m", "a", "t", "s"]);
  });
});
