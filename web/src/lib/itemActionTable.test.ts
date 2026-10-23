import { describe, expect, it } from "vitest";
import { actionLabel, actionShort, markAllLabel, surfaceActions, type SurfaceContext } from "./itemActionTable";

const ctx: SurfaceContext = { canShare: true, ranges: true, muted: false, hasRule: false };

describe("the item action table", () => {
  it("names a toggle by what it does next", () => {
    expect(actionLabel("read", { on: false })).toBe("Mark as read");
    expect(actionLabel("read", { on: true })).toBe("Mark as unread");
    expect(actionLabel("star", { on: true })).toBe("Unstar");
    expect(actionLabel("star")).toBe("Star");
  });

  it("gives the swipe panel the same words as the menu, with a short More", () => {
    expect(surfaceActions("swipeLeading", ctx).map((id) => actionLabel(id, { on: false }))).toEqual(["Mark as read"]);
    expect(surfaceActions("swipeTrailing", ctx).map((id) => actionLabel(id))).toEqual(["Star", "More actions"]);
    expect(actionShort("more")).toBe("More");
  });

  it("orders the row menu state, reach, filter, feed, links, and drops Share where the browser cannot share", () => {
    expect(surfaceActions("rowMenu", ctx)).toEqual(["star", "read", "markAbove", "markBelow", "muteSimilar", "editFeed", "openOriginal", "copyLink", "share"]);
    expect(surfaceActions("rowMenu", { ...ctx, canShare: false, ranges: false })).toEqual(["star", "read", "muteSimilar", "editFeed", "openOriginal", "copyLink"]);
  });

  it("offers a muted article Restore, and Edit rule only while the rule has a name", () => {
    const muted = { ...ctx, muted: true };
    expect(surfaceActions("rowMenu", muted).slice(0, 2)).toEqual(["star", "restore"]);
    expect(surfaceActions("rowMenu", { ...muted, hasRule: true }).slice(0, 3)).toEqual(["star", "restore", "editRule"]);
  });

  it("calls the feed editor one name everywhere", () => {
    expect(actionLabel("editFeed")).toBe("Edit feed");
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
  it("names a feed's count in Unread, results in a search, and nothing false where the count is unknown", () => {
    expect(markAllLabel({ view: "unread", feed: "f1" }, 3)).toBe("Mark all 3 as read");
    expect(markAllLabel({ view: "all", q: "rust" }, null)).toBe("Mark all results as read");
    expect(markAllLabel({ view: "starred" }, null)).toBe("Mark all as read");
  });
});
