import { beforeEach, describe, expect, it } from "vitest";
import { layoutContext } from "@/layouts";
import {
  DEVICE_PREFS_KEY,
  devicePrefsStore,
  overrideTarget,
  parseDevicePrefs,
  resetDevicePrefs,
  resolveLayout,
  setLayoutOverride,
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
];

describe("layout override resolution", () => {
  it("Magazine is the default", () => {
    expect(resolveLayout(devicePrefsStore.get(), {})).toBe("magazine");
  });

  it("resolves feed > folder > device default", () => {
    updateDevicePrefs({ layout: "compact" });
    setLayoutOverride("folder", "10", "inbox");
    setLayoutOverride("feed", "2", "cards");
    const p = devicePrefsStore.get();
    expect(resolveLayout(p, layoutContext({ view: "unread", feed: "2" }, feeds))).toBe("cards"); // feed wins
    expect(resolveLayout(p, layoutContext({ view: "unread", feed: "1" }, feeds))).toBe("inbox"); // folder of the feed
    expect(resolveLayout(p, layoutContext({ view: "unread", feed: "3" }, feeds))).toBe("compact"); // device default
    expect(resolveLayout(p, layoutContext({ view: "unread", folder: "10" }, feeds))).toBe("inbox");
    expect(resolveLayout(p, layoutContext({ view: "all" }, feeds))).toBe("compact"); // no override applies to All
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
    expect(overrideTarget({ feedId: "2", folderId: "10" })).toEqual({ kind: "feed", id: "2" });
    expect(overrideTarget({ folderId: "10" })).toEqual({ kind: "folder", id: "10" });
    expect(overrideTarget({})).toBeNull();
  });
});

describe("storage", () => {
  it("persists through the storage seam and survives a reload", () => {
    setLayoutOverride("feed", "7", "headlines");
    updateDevicePrefs({ order: "oldest" });
    const back = parseDevicePrefs(localStorage.getItem(DEVICE_PREFS_KEY));
    expect(back.overrides.feed).toEqual({ "7": "headlines" });
    expect(back.order).toBe("oldest");
  });

  it("ignores junk", () => {
    const p = parseDevicePrefs(JSON.stringify({ layout: "bogus", overrides: { feed: { "1": "nope", "2": "cards" }, folder: 5 }, order: "sideways" }));
    expect(p.layout).toBe("magazine");
    expect(p.overrides.feed).toEqual({ "2": "cards" });
    expect(p.overrides.folder).toEqual({});
    expect(p.order).toBe("newest");
    expect(parseDevicePrefs("{not json").layout).toBe("magazine");
  });
});
