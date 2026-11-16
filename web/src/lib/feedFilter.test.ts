import { describe, expect, it } from "vitest";
import type { Feed, Folder } from "@/api/types";
import { bootstrap } from "@/test/mockApi";
import { feedCountText, feedMatch } from "./feedFilter";
import { allCollapsed } from "./folderCollapse";
import { folderTree } from "./folderTree";

const folder = (id: string, name: string, parent: string | null = null): Folder => ({ id, name, parent_id: parent, position: Number(id), is_default: false, unread: 0 });
const feed = (id: string, title: string, folder_id: string, over: Partial<Feed> = {}): Feed => ({ ...bootstrap.feeds[0]!, id, title, folder_id, unread: 0, ...over });

describe("feedMatch", () => {
  const folders = [folder("1", "News"), folder("2", "Tech"), folder("3", "Gadgets", "2")];
  const tree = folderTree(folders);
  const feeds = [feed("10", "The Wire", "1"), feed("11", "Ars Technica", "2"), feed("12", "Phone Weekly", "3"), feed("13", "Same", "1", { address_hint: "alpha.example" })];

  it("is no filter for empty or blank text", () => {
    expect(feedMatch(tree, feeds, "")).toBeNull();
    expect(feedMatch(tree, feeds, "   ")).toBeNull();
  });

  it("keeps feeds whose title holds the text, ignoring case and the ends", () => {
    expect([...feedMatch(tree, feeds, "  WIRE ")!]).toEqual(["10"]);
  });

  it("matches the short address a feed shows beside a title another feed shares", () => {
    expect([...feedMatch(tree, feeds, "alpha.ex")!]).toEqual(["13"]);
  });

  it("keeps every feed under a folder whose name matches, subfolders included", () => {
    expect([...feedMatch(tree, feeds, "tech")!].sort()).toEqual(["11", "12"]); // Tech holds Gadgets; "Ars Technica" also matches by title
    expect([...feedMatch(tree, feeds, "gadget")!]).toEqual(["12"]);
  });

  it("matches a folder by its own name, not by two names run together", () => {
    // "Tech Gadgets" is how a path reads; no folder is named that, so it matches nothing.
    expect(feedMatch(tree, feeds, "tech gadgets")!.size).toBe(0);
  });

  it("keeps nothing when nothing matches", () => {
    expect(feedMatch(tree, feeds, "zzz")!.size).toBe(0);
  });

  it("says how many it shows in the Feed health search's words", () => {
    expect(feedCountText(3, 137)).toBe("Showing 3 of 137 feeds");
  });

  it("filters 500 feeds in 50 folders quickly, as a keystroke does it (nothing is asked of the server)", () => {
    const many = Array.from({ length: 50 }, (_, i) => folder(String(i + 1), `Folder ${i + 1}`, i % 5 === 0 || i === 0 ? null : String(i - (i % 5) + 1)));
    const t = folderTree(many);
    const fs = Array.from({ length: 500 }, (_, i) => feed(String(i + 1000), `Feed number ${i}`, String((i % 50) + 1)));
    const start = performance.now();
    for (let k = 0; k < 20; k++) for (const q of ["f", "fe", "feed n", "feed number 4", "folder 7", "zzz"]) feedMatch(t, fs, q);
    // 120 filters of 500 feeds: far under a second even on a loaded machine; a per-feed tree walk would not be.
    expect(performance.now() - start).toBeLessThan(1000);
    expect(feedMatch(t, fs, "feed number 4")!.size).toBe(111); // 4, 40-49, 400-499
  });
});

describe("allCollapsed", () => {
  it("is true only when every listed folder is collapsed", () => {
    expect(allCollapsed(["1", "2"], ["1", "2", "9"])).toBe(true);
    expect(allCollapsed(["1", "2"], ["1"])).toBe(false);
    expect(allCollapsed([], [])).toBe(false);
  });
});
