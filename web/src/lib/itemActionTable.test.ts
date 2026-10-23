import { describe, expect, it } from "vitest";
import { actionLabel, actionShort, markAllLabel } from "./itemActionTable";

describe("the item action table", () => {
  it("names a toggle by what it does next", () => {
    expect(actionLabel("read", { on: false })).toBe("Mark as read");
    expect(actionLabel("read", { on: true })).toBe("Mark as unread");
    expect(actionLabel("star", { on: true })).toBe("Unstar");
    expect(actionLabel("star")).toBe("Star");
  });

  it("gives the swipe panel's More its short word", () => {
    expect(actionLabel("more")).toBe("More actions");
    expect(actionShort("more")).toBe("More");
  });
});

describe("Mark all words name the reach", () => {
  it("counts a feed, a folder or the library", () => {
    expect(markAllLabel({ view: "all", feed: "f1" }, 214)).toBe("Mark all 214 as read");
    expect(markAllLabel({ view: "all" }, 7)).toBe("Mark all 7 as read");
  });
  it("says everything unread in the Unread view of the library", () => {
    expect(markAllLabel({ view: "unread" }, 214)).toBe("Mark everything unread as read");
  });
  it("names a feed's count in Unread, results in a search, and nothing false where the count is unknown or zero", () => {
    expect(markAllLabel({ view: "unread", feed: "f1" }, 3)).toBe("Mark all 3 as read");
    expect(markAllLabel({ view: "all", q: "rust" }, null)).toBe("Mark all results as read");
    expect(markAllLabel({ view: "starred" }, null)).toBe("Mark all as read");
    expect(markAllLabel({ view: "all", feed: "f1" }, 0)).toBe("Mark all as read");
  });
});
