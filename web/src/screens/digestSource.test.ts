import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearDigestPlaces, digestPageLeftAt, holdPage, oldestAsOf, planDigest, type DigestInput } from "./digestSource";

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
