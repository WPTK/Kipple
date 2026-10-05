import { beforeEach, describe, expect, it, vi } from "vitest";
import { layoutContext } from "@/layouts";
import { folderTree } from "./folderTree";
import {
  DEVICE_PREFS_KEY,
  devicePrefsStore,
  inheritedList,
  overrideTarget,
  parseDevicePrefs,
  resetDevicePrefs,
  resolveLayout,
  resolveList,
  setLayoutOverride,
  setListOverride,
  updateDevicePrefs,
} from "./devicePrefs";

beforeEach(() => {
  localStorage.clear();
  resetDevicePrefs();
});

const feeds = [
  { id: "1", folder_id: "10" },
  { id: "2", folder_id: "10" },
  { id: "3", folder_id: "11" },
  { id: "4", folder_id: "13" },
];
// 10 and 11 at the top level; 12 inside 10; 13 inside 12.
const tree = folderTree([
  { id: "10", name: "Ten", parent_id: null },
  { id: "12", name: "Twelve", parent_id: "10" },
  { id: "13", name: "Thirteen", parent_id: "12" },
  { id: "11", name: "Eleven", parent_id: null },
]);

describe("search order migration", () => {
  it("adopts the old localStorage key once, then removes it", async () => {
    localStorage.setItem("kipple.searchOrder.v1", "oldest");
    vi.resetModules();
    const m = await import("./devicePrefs");
    expect(m.devicePrefsStore.get().searchOrder).toBe("oldest");
    expect(localStorage.getItem("kipple.searchOrder.v1")).toBeNull();
    expect(JSON.parse(localStorage.getItem(m.DEVICE_PREFS_KEY) ?? "{}").searchOrder).toBe("oldest");
  });
  it("keeps a value already in the device cache over the old key", async () => {
    localStorage.setItem(DEVICE_PREFS_KEY, JSON.stringify({ searchOrder: "date" }));
    localStorage.setItem("kipple.searchOrder.v1", "oldest");
    vi.resetModules();
    const m = await import("./devicePrefs");
    expect(m.devicePrefsStore.get().searchOrder).toBe("date");
  });
});

describe("the v1 cache (overrides were a layout per id)", () => {
  it("the first paint after the upgrade keeps the layouts: v1 is read once, converted and left for older tabs", async () => {
    const v1 = JSON.stringify({ layout: "compact", order: "oldest", overrides: { feed: { "3": "cards", "4": "bogus" }, folder: { "10": "inbox" } } });
    localStorage.setItem("kipple.device.v1", v1);
    vi.resetModules();
    const m = await import("./devicePrefs");
    expect(m.DEVICE_PREFS_KEY).toBe("kipple.device.v2");
    expect(m.devicePrefsStore.get()).toMatchObject({ layout: "compact", order: "oldest", overrides: { feed: { "3": { layout: "cards" } }, folder: { "10": { layout: "inbox" } } } });
    expect(JSON.parse(localStorage.getItem("kipple.device.v2") ?? "{}").overrides.feed).toEqual({ "3": { layout: "cards" } });
    expect(localStorage.getItem("kipple.device.v1")).toBe(v1);
    // Once v2 exists, v1 is never read again.
    localStorage.setItem("kipple.device.v1", JSON.stringify({ layout: "inbox" }));
    vi.resetModules();
    expect((await import("./devicePrefs")).devicePrefsStore.get().layout).toBe("compact");
  });
});

describe("layout override resolution", () => {
  it("Magazine is the default", () => {
    expect(resolveLayout(devicePrefsStore.get(), {})).toBe("magazine");
  });

  it("resolves feed > folder > device default", () => {
    updateDevicePrefs({ layout: "compact" });
    setLayoutOverride("folder", "10", "inbox");
    setLayoutOverride("feed", "2", "cards");
    const p = devicePrefsStore.get();
    expect(resolveLayout(p, layoutContext({ view: "unread", feed: "2" }, feeds, tree))).toBe("cards"); // feed wins
    expect(resolveLayout(p, layoutContext({ view: "unread", feed: "1" }, feeds, tree))).toBe("inbox"); // folder of the feed
    expect(resolveLayout(p, layoutContext({ view: "unread", feed: "3" }, feeds, tree))).toBe("compact"); // device default
    expect(resolveLayout(p, layoutContext({ view: "unread", folder: "10" }, feeds, tree))).toBe("inbox");
    expect(resolveLayout(p, layoutContext({ view: "all" }, feeds, tree))).toBe("compact"); // no override applies to All
  });

  it("a subfolder and its feeds inherit the nearest folder override up the tree", () => {
    updateDevicePrefs({ layout: "compact" });
    setLayoutOverride("folder", "10", "inbox");
    const p = () => devicePrefsStore.get();
    expect(resolveLayout(p(), layoutContext({ view: "unread", folder: "13" }, feeds, tree))).toBe("inbox"); // two levels up
    expect(resolveLayout(p(), layoutContext({ view: "unread", feed: "4" }, feeds, tree))).toBe("inbox");
    setLayoutOverride("folder", "12", "headlines");
    expect(resolveLayout(p(), layoutContext({ view: "unread", folder: "13" }, feeds, tree))).toBe("headlines"); // the nearest wins
    expect(resolveLayout(p(), layoutContext({ view: "unread", folder: "10" }, feeds, tree))).toBe("inbox"); // never from below
    expect(overrideTarget(layoutContext({ view: "unread", folder: "13" }, feeds, tree))).toEqual({ kind: "folder", id: "13" });
  });

  it("the c toggle beats every override", () => {
    setLayoutOverride("feed", "2", "cards");
    expect(resolveLayout(devicePrefsStore.get(), { feedId: "2" }, "compact")).toBe("compact");
  });

  it("clearing an override falls back", () => {
    setLayoutOverride("feed", "2", "cards");
    setLayoutOverride("feed", "2", null);
    expect(resolveLayout(devicePrefsStore.get(), { feedId: "2" })).toBe("magazine");
  });

  it("knows which override a list can carry", () => {
    expect(overrideTarget({ feedId: "2", folderIds: ["10"] })).toEqual({ kind: "feed", id: "2" });
    expect(overrideTarget({ folderIds: ["10"] })).toEqual({ kind: "folder", id: "10" });
    expect(overrideTarget({})).toBeNull();
  });
});

describe("order and view overrides (#38)", () => {
  it("each field resolves on its own: feed, then the folders up the tree, then the device default", () => {
    updateDevicePrefs({ order: "oldest" });
    setListOverride("folder", "10", "view", "all");
    setListOverride("folder", "12", "order", "newest");
    setLayoutOverride("folder", "13", "cards");
    const p = () => devicePrefsStore.get();
    const ctx4 = layoutContext({ view: "unread", feed: "4" }, feeds, tree); // feed 4 is in 13, inside 12, inside 10
    expect(resolveList(p(), ctx4, "layout")).toEqual({ value: "cards", from: { kind: "folder", id: "13" } });
    expect(resolveList(p(), ctx4, "order")).toEqual({ value: "newest", from: { kind: "folder", id: "12" } });
    expect(resolveList(p(), ctx4, "view")).toEqual({ value: "all", from: { kind: "folder", id: "10" } });
    // No override anywhere: the device order, and Unread (the view has no device default).
    const ctx3 = layoutContext({ view: "unread", feed: "3" }, feeds, tree);
    expect(resolveList(p(), ctx3, "order")).toEqual({ value: "oldest", from: null });
    expect(resolveList(p(), ctx3, "view")).toEqual({ value: "unread", from: null });
    // The feed's own value wins; what it would inherit is one level up.
    setListOverride("feed", "4", "order", "oldest");
    expect(resolveList(p(), ctx4, "order")).toEqual({ value: "oldest", from: { kind: "feed", id: "4" } });
    expect(inheritedList(p(), ctx4, "order")).toEqual({ value: "newest", from: { kind: "folder", id: "12" } });
    // A folder list inherits from the folder above it, never from itself.
    const ctx12 = layoutContext({ view: "unread", folder: "12" }, feeds, tree);
    expect(inheritedList(p(), ctx12, "order")).toEqual({ value: "oldest", from: null });
  });

  it("clearing the last field removes the override, and the other fields stay", () => {
    setListOverride("feed", "2", "layout", "cards");
    setListOverride("feed", "2", "view", "all");
    setListOverride("feed", "2", "layout", null);
    expect(devicePrefsStore.get().overrides.feed).toEqual({ "2": { view: "all" } });
    setListOverride("feed", "2", "view", null);
    expect(devicePrefsStore.get().overrides.feed).toEqual({});
  });
});

describe("storage", () => {
  it("persists through the storage seam and survives a reload", () => {
    setLayoutOverride("feed", "7", "headlines");
    setListOverride("folder", "3", "order", "oldest");
    updateDevicePrefs({ order: "oldest" });
    const back = parseDevicePrefs(localStorage.getItem(DEVICE_PREFS_KEY));
    expect(back.overrides.feed).toEqual({ "7": { layout: "headlines" } });
    expect(back.overrides.folder).toEqual({ "3": { order: "oldest" } });
    expect(back.order).toBe("oldest");
  });

  it("ignores junk", () => {
    const p = parseDevicePrefs(
      JSON.stringify({
        layout: "bogus",
        overrides: { feed: { "1": "cards", "2": { layout: "cards", order: "sideways", view: "starred" }, "3": { view: "nope" } }, folder: 5 },
        order: "sideways",
      }),
    );
    expect(p.layout).toBe("magazine");
    expect(p.overrides.feed).toEqual({ "2": { layout: "cards" } });
    expect(p.overrides.folder).toEqual({});
    expect(p.order).toBe("newest");
    expect(parseDevicePrefs("{not json").layout).toBe("magazine");
  });
});
