import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearDigestPlaces, digestPageLeftAt, holdPage, oldestAsOf, planDigest, sectionViews, type DigestInput } from "./digestSource";

describe("oldestAsOf", () => {
  it("is the smallest as_of, compared as numbers", () => {
    expect(oldestAsOf(["900", "1000", "95"])).toBe("95");
  });
  it("ignores sections without one, and one that is not a number", () => {
    expect(oldestAsOf([undefined, "12", "x", "30"])).toBe("12");
  });
  it("is undefined when no section has one", () => {
    expect(oldestAsOf([undefined, undefined])).toBeUndefined();
    expect(oldestAsOf([])).toBeUndefined();
  });
});

describe("the page a reader left", () => {
  const folders = [
    { id: "10", name: "Alpha", parent_id: null },
    { id: "11", name: "Beta", parent_id: null },
  ];
  const feed = (id: string, folder_id: string, unread: number) => ({ id, title: `F${id}`, folder_id, unread, starred_count: 0, is_archive: false, pace: null });
  const input = (unreadBeta: number): DigestInput => ({
    scope: { view: "unread" },
    feeds: [feed("1", "10", 2), feed("2", "11", unreadBeta)],
    folders,
    sections: "folder",
    order: "sidebar",
    rows: 3,
    place: { steps: 0, presses: new Map() },
  });
  const sections = (unreadBeta: number) => planDigest(input(unreadBeta), () => undefined).defs.map((d) => d.title);
  let now = 1_000_000;

  beforeEach(() => {
    clearDigestPlaces();
    now = 1_000_000;
    vi.spyOn(Date, "now").mockImplementation(() => now);
  });
  afterEach(() => vi.restoreAllMocks());

  it("is away only when no hook uses it, and back when one does", () => {
    sections(0);
    const a = holdPage("unread");
    const b = holdPage("unread"); // the reader's previous and next, mounted beside the list
    expect(digestPageLeftAt("unread")).toBeNull();
    a();
    expect(digestPageLeftAt("unread")).toBeNull(); // the other still uses it: the list closing does not start the clock
    b();
    expect(digestPageLeftAt("unread")).toBe(now);
    const c = holdPage("unread");
    expect(digestPageLeftAt("unread")).toBeNull();
    c();
  });

  it("is kept as it was for five minutes away, and rebuilt after", () => {
    expect(sections(0)).toEqual(["Alpha"]);
    holdPage("unread")();
    now += 4 * 60_000;
    expect(sections(3)).toEqual(["Alpha"]);
    now += 2 * 60_000;
    expect(sections(3)).toEqual(["Alpha", "Beta"]);
  });

  it("never counts time while a hook uses it, however long the reader reads", () => {
    expect(sections(0)).toEqual(["Alpha"]);
    const leave = holdPage("unread");
    // The list closed (a hook still holds the page) and the reader reads for an hour.
    holdPage("unread")();
    now += 60 * 60_000;
    expect(sections(3)).toEqual(["Alpha"]);
    leave();
  });

  it("starts the clock afresh each time it is left: a return clears it", () => {
    expect(sections(0)).toEqual(["Alpha"]);
    holdPage("unread")();
    now += 4 * 60_000;
    const back = holdPage("unread"); // returns within the time: the page is the same, and no longer away
    now += 30 * 60_000;
    expect(sections(3)).toEqual(["Alpha"]);
    back();
    now += 60_000;
    expect(sections(3)).toEqual(["Alpha"]);
  });
});

describe("a section's bound", () => {
  const folders = [{ id: "10", name: "Alpha", parent_id: null }];
  const feeds = [{ id: "1", title: "F1", folder_id: "10", unread: 2, starred_count: 0, is_archive: false, pace: null }];
  const input = (): DigestInput => ({ scope: { view: "unread" }, feeds, folders, sections: "folder", order: "sidebar", rows: 3, place: { steps: 0, presses: new Map() } });
  const read = (asOf: string) => () => ({ data: { cards: [], more: false, asOf }, error: null, fetching: false, fetchingMore: false, moreFailed: false });

  beforeEach(() => clearDigestPlaces());

  it("stays the as_of the section first came back with when it is fetched again", () => {
    const first = planDigest(input(), read("500"));
    expect([...first.results.values()][0]?.data?.asOf).toBe("500");
    const again = planDigest(input(), read("900")); // every section refetched: its list now has a newer as_of
    expect([...again.results.values()][0]?.data?.asOf).toBe("500");
  });

  it("is taken afresh when the page is rebuilt", () => {
    planDigest(input(), read("500"));
    clearDigestPlaces();
    expect([...planDigest(input(), read("900")).results.values()][0]?.data?.asOf).toBe("900");
  });
});

describe("the Digest's reading-time sections", () => {
  const input = (scope: DigestInput["scope"]): DigestInput => ({
    scope,
    feeds: [{ id: "1", title: "F1", folder_id: "10", unread: 4, starred_count: 0, is_archive: false, pace: null }],
    folders: [{ id: "10", name: "Alpha", parent_id: null }],
    sections: "length",
    order: "sidebar",
    rows: 3,
    place: { steps: 0, presses: new Map() },
  });

  it("are the four reading lengths of the same list, in order, whatever the folders hold", () => {
    clearDigestPlaces();
    const plan = planDigest(input({ view: "unread", folder: "10" }), () => undefined);
    expect(plan.defs.map((d) => [d.key, d.title, d.scope, d.count])).toEqual([
      ["len:under3", "Under 3 minutes", { folder: "10", length: "under3" }, null],
      ["len:3to7", "3 to 7 minutes", { folder: "10", length: "3to7" }, null],
      ["len:8up", "8 minutes and up", { folder: "10", length: "8up" }, null],
      ["len:unknown", "Unknown length", { folder: "10", length: "unknown" }, null],
    ]);
    // All four are asked for at once, each as the scope with its length.
    expect(plan.askedFor.size).toBe(4);
    expect(plan.waiting).toBe(false);
  });

  it("have no count until they load, then say exactly how many more", () => {
    clearDigestPlaces();
    const seen: string[] = [];
    const cards = (n: number) => Array.from({ length: n }, (_, i) => ({ id: String(i), read: false }) as never);
    const plan = planDigest(input({ view: "unread" }), (sc) => {
      seen.push(`${sc.length}|${sc.folder ?? ""}`);
      return sc.length === "3to7" ? { data: { cards: cards(5), more: false, asOf: "9" }, error: null, fetching: false, fetchingMore: false, moreFailed: false } : undefined;
    });
    expect(seen).toEqual(["under3|", "3to7|", "8up|", "unknown|"]);
    const by = Object.fromEntries(plan.layout.sections.map((s) => [s.key, s]));
    expect(by["len:under3"]).toMatchObject({ state: "loading", moreCount: null, total: null });
    expect(by["len:3to7"]).toMatchObject({ state: "ready", moreCount: 2, total: 5 });
    expect(plan.layout.cards).toHaveLength(3);
  });

  it("draws a section that came back empty only if it once showed cards on this page", () => {
    clearDigestPlaces();
    const cards = (n: number) => Array.from({ length: n }, (_, i) => ({ id: String(i), read: false }) as never);
    const result = (n: number) => ({ data: { cards: cards(n), more: false, asOf: "9" }, error: null, fetching: false, fetchingMore: false, moreFailed: false });
    const titles = (n3to7: number) => sectionViews(planDigest(input({ view: "unread" }), (sc) => (sc.length === "3to7" ? result(n3to7) : result(0)))).map((s) => s.key);
    // Never had a card: dropped. 3 to 7 shows its cards, is refetched empty (all read elsewhere) and stays.
    expect(titles(2)).toEqual(["len:3to7"]);
    expect(titles(0)).toEqual(["len:3to7"]);
    // A page built afresh has no memory of it.
    clearDigestPlaces();
    expect(titles(0)).toEqual([]);
  });
});
