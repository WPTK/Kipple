import { describe, expect, it } from "vitest";
import type { Card, Feed, View } from "@/api/types";
import { feedsInFolder } from "@/lib/folderTree";
import { byFeed, byFolder } from "@/lib/sections";
import { EXPAND_BATCH, fixedSections, groupFeeds, layoutDigest, orderSections, type DigestOptions, type LoadedSection, type SectionDef } from "./digestPlan";

const folders = [
  { id: "tech", name: "Tech", parent_id: null },
  { id: "apple", name: "Apple", parent_id: "tech" },
  { id: "news", name: "News", parent_id: null },
  { id: "empty", name: "Empty", parent_id: null },
];

type F = Pick<Feed, "id" | "title" | "folder_id" | "unread" | "starred_count" | "is_archive"> & { pace?: number | null };
const feed = (id: string, title: string, folder_id: string, over: Partial<F> = {}): F => ({ id, title, folder_id, unread: 0, starred_count: 0, is_archive: false, pace: null, ...over });

const feeds: F[] = [
  feed("f1", "Daily Wire", "news", { unread: 4, starred_count: 1, pace: 12 }),
  feed("f2", "Chip Weekly", "tech", { unread: 2, pace: 0.5 }),
  feed("f3", "Orchard", "apple", { unread: 0, pace: 0.5 }),
  feed("f4", "Other Press", "news", { unread: 7, pace: 3 }),
  feed("f5", "Loose Feed", "gone", { unread: 1 }),
  feed("fa", "Unsubscribed (starred)", "news", { is_archive: true, starred_count: 3 }),
];

const keys = (d: readonly { key: string }[]) => d.map((s) => s.key);
const rootByFolder = (view: View = "unread", list: F[] = feeds) => groupFeeds(list, folders, byFolder(folders, list, {}), view);

const card = (id: string, feed_id: string, read = false) => ({ id, feed_id, title: id, read }) as unknown as Card;
const opts = (over: Partial<DigestOptions> = {}): DigestOptions => ({ view: "unread", rows: 3, expanded: new Map(), activated: new Set(), ...over });
const ids = (cards: readonly Card[]) => cards.map((c) => c.id);
const loadedOf = (o: Record<string, [string[], boolean?]>) => new Map<string, LoadedSection>(Object.entries(o).map(([k, [list, more]]) => [k, { cards: list.map((id) => card(id, "f1")), more: more ?? false }]));

describe("groupFeeds", () => {
  it("root by folder: sidebar order, subfolder feeds roll up, a feed outside any folder is its own section", () => {
    const defs = rootByFolder();
    // Tech (with Apple inside), News, then the feed whose folder is unknown; the archive feed is Starred only.
    expect(keys(defs)).toEqual(["folder:tech", "folder:news", "feed:f5"]);
    expect(defs.map((d) => [d.title, d.feedIds, d.count])).toEqual([
      ["Tech", ["f3", "f2"], 2],
      ["News", ["f1", "f4"], 11],
      ["Loose Feed", ["f5"], 1],
    ]);
  });

  it("folder scope: subfolders plus the folder's own feeds, each feed its own section", () => {
    const inTech = feedsInFolder("tech", feeds, folders);
    const defs = groupFeeds(inTech, folders, byFolder(folders, feeds, { folder: "tech" }));
    expect(defs.map((d) => [d.key, d.feedIds])).toEqual([
      ["folder:apple", ["f3"]],
      ["feed:f2", ["f2"]],
    ]);
  });

  it("feed scope: one section", () => {
    const defs = groupFeeds([feeds[3]!], folders, byFolder(folders, feeds, { feed: "f4" }));
    expect(defs.map((d) => [d.key, d.title, d.scope, d.count])).toEqual([["feed:f4", "Other Press", { feed: "f4" }, 7]]);
  });

  it("root by feed: every feed a section, in sidebar order", () => {
    const defs = groupFeeds(feeds, folders, byFeed(feeds));
    expect(keys(defs)).toEqual(["feed:f3", "feed:f2", "feed:f1", "feed:f4", "feed:f5"]);
  });

  it("All and Muted have no count", () => {
    for (const view of ["all", "muted"] as const) expect(rootByFolder(view).map((d) => d.count)).toEqual([null, null, null]);
  });

  it("Starred counts starred articles and ends with the archive feed as its own section", () => {
    const defs = rootByFolder("starred");
    expect(defs.map((d) => [d.key, d.count])).toEqual([
      ["folder:tech", 0],
      ["folder:news", 1],
      ["feed:f5", 0],
      ["feed:fa", 3],
    ]);
    expect(defs[3]!.title).toBe("Unsubscribed (starred)");
  });

  it("a deleted feed (gone from the bootstrap) is dropped", () => {
    const without = feeds.filter((f) => f.id !== "f4");
    const defs = rootByFolder("unread", without);
    expect(defs.find((d) => d.key === "folder:news")!.feedIds).toEqual(["f1"]);
    expect(defs.flatMap((d) => d.feedIds)).not.toContain("f4");
  });

  it("shuffled input gives identical output", () => {
    const base = JSON.stringify(rootByFolder());
    const shuffled = [feeds[4]!, feeds[0]!, feeds[5]!, feeds[2]!, feeds[3]!, feeds[1]!];
    expect(JSON.stringify(rootByFolder("unread", shuffled))).toBe(base);
  });

  it("a folder's pace is the sum of its feeds, null when any of them has none", () => {
    const defs = rootByFolder();
    expect(defs.map((d) => [d.key, d.pace])).toEqual([
      ["folder:tech", 1],
      ["folder:news", 15],
      ["feed:f5", null],
    ]);
  });

  it("a folder with a new feed and a paced one has no pace, so Quiet Feeds First does not take the new feed for a quiet one", () => {
    const mixed = [feed("m1", "Old", "tech", { unread: 1, pace: 4 }), feed("m2", "New", "tech", { unread: 1 }), feed("m3", "Busy", "news", { unread: 1, pace: 30 })];
    const defs = groupFeeds(mixed, folders, byFolder(folders, mixed, {}));
    expect(defs.map((d) => [d.key, d.pace])).toEqual([["folder:tech", null], ["folder:news", 30]]);
    expect(keys(orderSections(defs, "quiet"))).toEqual(["folder:news", "folder:tech"]);
  });
});

describe("orderSections", () => {
  const defs = (list: [string, number | null][]): SectionDef[] => list.map(([k, pace]) => ({ key: k, title: k, scope: { feed: k }, feedIds: [k], count: 1, pace }));

  it("sidebar keeps the order it was given", () => {
    const d = defs([["a", 5], ["b", 1]]);
    expect(keys(orderSections(d, "sidebar"))).toEqual(["a", "b"]);
  });

  it("quiet: slowest first, ties in sidebar order, feeds without a pace after, in sidebar order", () => {
    const d = defs([["n1", null], ["busy", 9], ["t1", 0.5], ["n2", null], ["slow", 0.1], ["t2", 0.5]]);
    expect(keys(orderSections(d, "quiet"))).toEqual(["slow", "t1", "t2", "busy", "n1", "n2"]);
  });

  it("quiet: a folder of only new feeds goes after, and a folder ranks by its summed pace", () => {
    const o = orderSections(rootByFolder(), "quiet");
    expect(keys(o)).toEqual(["folder:tech", "folder:news", "feed:f5"]);
    const two = [feed("a", "A", "news", { pace: 0.2 }), feed("b", "B", "tech", { pace: 0.3 })];
    const quietNews = groupFeeds(two, folders, byFolder(folders, two, {}));
    expect(keys(orderSections(quietNews, "quiet"))).toEqual(["folder:news", "folder:tech"]);
  });

  it("does not change its input", () => {
    const d = defs([["a", 5], ["b", 1]]);
    orderSections(d, "quiet");
    expect(keys(d)).toEqual(["a", "b"]);
  });
});

describe("layoutDigest", () => {
  const defs = rootByFolder();

  it("Unread drops a section with nothing unread, and the count is the bootstrap's", () => {
    const withZero = rootByFolder("unread", feeds.map((f) => (f.id === "f5" ? { ...f, unread: 0 } : f)));
    const out = layoutDigest(withZero, opts(), new Map());
    expect(out.sections.map((s) => [s.key, s.moreCount])).toEqual([["folder:tech", 2], ["folder:news", 11]]);
  });

  it("keeps a zero-count section that holds cards already loaded", () => {
    const withZero = rootByFolder("unread", feeds.map((f) => (f.id === "f5" ? { ...f, unread: 0 } : f)));
    const out = layoutDigest(withZero, opts(), loadedOf({ "feed:f5": [["x1"]] }));
    expect(keys(out.sections)).toContain("feed:f5");
  });

  it("All and Muted: moreCount is null", () => {
    for (const view of ["all", "muted"] as const) {
      const out = layoutDigest(rootByFolder(view), opts({ view }), loadedOf({ "folder:tech": [["a", "b", "c", "d"], true] }));
      expect(out.sections.map((s) => s.moreCount)).toEqual([null, null, null]);
      expect(out.sections[0]!.hasMore).toBe(true);
    }
  });

  it("shows `rows` cards and counts the rest; expanding adds a batch and clamps at zero", () => {
    const five = ["a", "b", "c", "d", "e"];
    const loaded = loadedOf({ "folder:tech": [five] });
    const s = layoutDigest(defs, opts({ rows: 3 }), loaded).sections[0]!;
    expect(ids(s.cards)).toEqual(["a", "b", "c"]);
    expect(s.moreCount).toBe(2); // the whole section is loaded, so the count is what it holds, not the bootstrap's
    const wide = layoutDigest(defs, opts({ rows: 3, expanded: new Map([["folder:tech", 1]]) }), loaded).sections[0]!;
    expect(ids(wide.cards)).toEqual(five);
    expect(wide.moreCount).toBe(0);
    const news = layoutDigest(defs, opts({ rows: 3 }), loadedOf({ "folder:news": [five, true] })).sections[1]!;
    expect(news.moreCount).toBe(11 - 3); // the server has more: the count the page opened with
    expect(EXPAND_BATCH).toBe(20);
  });

  it("states: waiting, loading, ready", () => {
    const out = layoutDigest(defs, opts({ activated: new Set(["folder:tech", "folder:news"]) }), loadedOf({ "folder:tech": [["a"]] }));
    expect(out.sections.map((s) => s.state)).toEqual(["ready", "loading", "waiting"]);
    expect(out.more).toBe(true);
  });

  it("prefix stability: loading section B never changes section A", () => {
    const a = loadedOf({ "folder:tech": [["a1", "a2"]] });
    const both = loadedOf({ "folder:tech": [["a1", "a2"]], "folder:news": [["b1", "b2", "b3"]] });
    const before = layoutDigest(defs, opts(), a).sections[0];
    const after = layoutDigest(defs, opts({ activated: new Set(["folder:news"]) }), both).sections[0];
    expect(after).toEqual(before);
  });

  it("reading order is section by section, and read cards stay", () => {
    const loaded = new Map<string, LoadedSection>([
      ["folder:tech", { cards: [card("a1", "f2", true), card("a2", "f2")], more: false }],
      ["folder:news", { cards: [card("b1", "f1")], more: false }],
    ]);
    const out = layoutDigest(defs, opts(), loaded);
    expect(ids(out.cards)).toEqual(["a1", "a2", "b1"]);
    expect(out.cards[0]).toMatchObject({ read: true });
  });

  it("more is false once every section is loaded and fully shown", () => {
    const loaded = loadedOf({ "folder:tech": [["a"]], "folder:news": [["b"]], "feed:f5": [["c"]] });
    expect(layoutDigest(defs, opts(), loaded).more).toBe(false);
  });
});

describe("fixedSections", () => {
  const defs = fixedSections([
    { key: "a", title: "A", scope: { length: "under3" } },
    { key: "b", title: "B", scope: { length: "unknown" } },
  ]);

  it("are sections of no feed, with no count and no pace", () => {
    expect(defs.map((d) => [d.key, d.feedIds, d.count, d.pace])).toEqual([
      ["a", [], null, null],
      ["b", [], null, null],
    ]);
  });

  it("are all kept once asked for: a count of null never drops one, the page drops those that load empty", () => {
    const l = layoutDigest(defs, opts({ activated: new Set(["a", "b"]) }), loadedOf({ a: [["1", "2", "3", "4"], true], b: [[]] }));
    expect(l.sections.map((s) => [s.key, s.state, s.cards.length, s.moreCount, s.hasMore])).toEqual([
      ["a", "ready", 3, null, true],
      ["b", "ready", 0, 0, false],
    ]);
  });

  it("keep a section whose cards were marked read, since reading never removes a section", () => {
    const read = new Map<string, LoadedSection>([["a", { cards: [{ ...card("1", "f1"), read: true }], more: false }]]);
    const l = layoutDigest(defs, opts({ activated: new Set(["a"]) }), read);
    expect(l.sections[0]!.cards.map((c) => c.id)).toEqual(["1"]);
  });
});
