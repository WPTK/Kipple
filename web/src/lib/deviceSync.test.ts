import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DeviceView } from "@/api/types";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { themeStore, updateTheme } from "@/theme/theme";
import { json, mockFetch } from "@/test/mockApi";
import {
  SYNC_DIRTY_KEY,
  SYNC_FLAG_KEY,
  copySettingsFrom,
  deriveLocal,
  flush,
  hydrateDevice,
  normalize,
  pendingChanges,
  profileOf,
  resetDeviceSync,
  discardRefused,
  retrySave,
  startDeviceSync,
  syncStore,
  type LocalState,
} from "./deviceSync";
import { DEVICE_PREFS_KEY, devicePrefsStore, resetDevicePrefs, updateDevicePrefs } from "./devicePrefs";
import { DEFAULT_PREFS, PREFS_KEY, prefsStore, updatePrefs } from "./prefs";

const local = (): LocalState => ({ theme: themeStore.get(), prefs: prefsStore.get(), dp: devicePrefsStore.get() });

// What a fresh server sends for a device with no overrides: every key at its default.
const DEFAULTS: Record<string, unknown> = {
  "ui.theme": "system",
  "ui.theme_day": "paper",
  "ui.theme_night": "midnight",
  "ui.theme_schedule": false,
  "ui.theme_night_start": "21:00",
  "ui.theme_day_start": "07:00",
  "ui.font_body": "",
  "ui.list_density": "standard",
  "ui.reading_density": "comfortable",
  "ui.mark_read_on_scroll": false,
  "client.layout": "magazine",
  "client.layout_overrides": { feed: {}, folder: {} },
  "client.order": "newest",
  "client.search_order": "relevance",
  "client.inbox_thumbs": "auto",
  "client.peek_seen": false,
  "client.article_width": "medium",
  "client.sidebar_width": 240,
  "client.unread_badge": "count",
  "client.highlight_keywords": true,
  "client.text_size": 1,
  "client.adjust_separately": false,
  "client.spacing": "normal",
  "client.motion": "system",
  "client.large_targets": false,
  "client.listen": false,
  "client.voice": "",
  "client.rate": 1,
  "client.collapsed_folders": [],
};

const device = (over: Partial<DeviceView> = {}): DeviceView => ({ id: "dev1", name: "", profile: {}, merged: { ...DEFAULTS }, ...over });

/** A server that stores overrides and answers with the merged view. */
function server(initial: Record<string, unknown> = {}, account: Record<string, unknown> = {}) {
  const profile: Record<string, unknown> = { ...initial };
  const patches: Record<string, unknown>[] = [];
  const view = (): DeviceView => {
    const merged = { ...DEFAULTS, ...account, ...profile };
    return { id: "dev1", name: "", profile: { ...profile }, merged };
  };
  const m = mockFetch({
    "PATCH /api/device": (_u, init) => {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      patches.push(body);
      for (const [k, v] of Object.entries(body)) if (v === null) delete profile[k];
      else profile[k] = v;
      return json(view());
    },
    "POST /api/device/copy-from/defaults": () => {
      for (const k of Object.keys(profile)) delete profile[k];
      return json(view());
    },
  });
  return { ...m, patches, profile, view };
}

beforeEach(() => {
  vi.useFakeTimers();
  // Sync off first: resetting the stores must not queue changes for the last test's server.
  resetDeviceSync();
  themeStore.set({ ...DEFAULT_THEME_SETTINGS });
  prefsStore.set({ ...DEFAULT_PREFS });
  resetDevicePrefs();
  localStorage.clear();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("mapping between the local stores and the profile", () => {
  it("round-trips the defaults without inventing overrides", () => {
    const l = local();
    const back = deriveLocal(DEFAULTS, l);
    expect(back.theme).toEqual(DEFAULT_THEME_SETTINGS);
    expect(back.prefs.font).toBe("default");
    expect(back.prefs.readingDensity).toBe("standard"); // the server's "comfortable"
    expect(back.dp.layout).toBe("magazine");
    expect(normalize(DEFAULTS, l)).toEqual(profileOf(back));
  });

  it("uses ui.theme:system plus day and night for follow-system, and the id for a fixed theme", () => {
    expect(profileOf(local())).toMatchObject({ "ui.theme": "system", "ui.theme_day": "paper", "ui.theme_night": "midnight" });
    updateTheme({ mode: "fixed", fixed: "graphite", night: "carbon" });
    expect(profileOf(local())).toMatchObject({ "ui.theme": "graphite", "ui.theme_night": "carbon" });
    const back = deriveLocal({ ...DEFAULTS, "ui.theme": "linen", "ui.theme_day": "airmail" }, local());
    expect(back.theme).toMatchObject({ mode: "fixed", fixed: "linen", day: "airmail" });
    // An id this build has no colors for is left alone rather than breaking the theme.
    expect(deriveLocal({ ...DEFAULTS, "ui.theme": "brand-new" }, { ...local(), theme: { ...DEFAULT_THEME_SETTINGS } }).theme).toEqual(DEFAULT_THEME_SETTINGS);
  });

  it("uses ui.theme:system plus ui.theme_schedule and the two start times for the schedule", () => {
    updateTheme({ mode: "follow", schedule: true, nightStart: "22:30", dayStart: "06:15" });
    expect(profileOf(local())).toMatchObject({ "ui.theme": "system", "ui.theme_schedule": true, "ui.theme_night_start": "22:30", "ui.theme_day_start": "06:15" });
    themeStore.set({ ...DEFAULT_THEME_SETTINGS });
    const back = deriveLocal({ ...DEFAULTS, "ui.theme_schedule": true, "ui.theme_night_start": "20:00", "ui.theme_day_start": "05:30" }, local());
    expect(back.theme).toMatchObject({ mode: "follow", schedule: true, nightStart: "20:00", dayStart: "05:30", day: "paper", night: "midnight" });
    // A time this build cannot read keeps the local one.
    const odd = deriveLocal({ ...DEFAULTS, "ui.theme_schedule": "yes", "ui.theme_night_start": "9pm", "ui.theme_day_start": null }, local());
    expect(odd.theme).toMatchObject({ schedule: false, nightStart: "21:00", dayStart: "07:00" });
    // A fixed theme on the server with the flag still set (an older client chose it) keeps the flag.
    expect(deriveLocal({ ...DEFAULTS, "ui.theme": "graphite", "ui.theme_schedule": true }, local()).theme).toMatchObject({ mode: "fixed", schedule: true });
  });

  it("maps the reading density names and the font names", () => {
    expect(deriveLocal({ ...DEFAULTS, "ui.reading_density": "compact" }, local()).prefs.readingDensity).toBe("snug");
    expect(deriveLocal({ ...DEFAULTS, "ui.reading_density": "relaxed" }, local()).prefs.readingDensity).toBe("relaxed");
    expect(deriveLocal({ ...DEFAULTS, "ui.reading_density": "airy" }, local()).prefs.readingDensity).toBe("airy");
    expect(deriveLocal({ ...DEFAULTS, "ui.font_body": "Atkinson Hyperlegible Next" }, local()).prefs.font).toBe("easy");
    expect(deriveLocal({ ...DEFAULTS, "ui.font_body": "Source Serif 4" }, local()).prefs.font).toBe("source-serif");
    updatePrefs({ font: "easy" });
    expect(profileOf(local())["ui.font_body"]).toBe("Atkinson Hyperlegible Next");
  });

  it("leaves keys without a device value unset: shortcuts, list width, link target", () => {
    const p = profileOf(local());
    expect(p["client.shortcuts"]).toBeNull();
    expect(p["client.list_width"]).toBeNull();
    expect(p["client.link_target"]).toBeNull();
    updatePrefs({ shortcuts: false });
    updateDevicePrefs({ listWidth: 400, linkTarget: "same" });
    expect(profileOf(local())).toMatchObject({ "client.shortcuts": false, "client.list_width": 400, "client.link_target": "same" });
    const back = deriveLocal({ ...DEFAULTS, "client.shortcuts": true, "client.list_width": 500 }, local());
    expect(back.prefs).toMatchObject({ shortcuts: true, shortcutsChosen: true });
    expect(back.dp.listWidth).toBe(500);
  });
});

describe("hydrating from the bootstrap", () => {
  it("the server wins: its effective values replace the local cache", () => {
    updatePrefs({ font: "inter", textSize: 1.5 });
    updateTheme({ mode: "fixed", fixed: "graphite" });
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    server();
    hydrateDevice(device({ profile: { "ui.theme": "linen" }, merged: { ...DEFAULTS, "ui.theme": "linen", "client.layout": "compact" } }));
    expect(themeStore.get()).toMatchObject({ mode: "fixed", fixed: "linen" });
    expect(prefsStore.get().font).toBe("default");
    expect(prefsStore.get().textSize).toBe(1);
    expect(devicePrefsStore.get().layout).toBe("compact");
    expect(syncStore.get().status).toBe("idle");
  });

  it("migrates the old localStorage values once, when the profile is empty: only what differs from the defaults", async () => {
    updatePrefs({ font: "literata", textSize: 1.25, listDensity: "airy", readingDensity: "airy" });
    updateTheme({ mode: "fixed", fixed: "cocoa-kraft" });
    updateDevicePrefs({ layout: "cards", order: "oldest" });
    // initPrefs and initTheme persist the other two stores in the app; the tests do it by hand.
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefsStore.get()));
    localStorage.setItem("kipple.theme.v1", JSON.stringify(themeStore.get()));
    expect(localStorage.getItem(DEVICE_PREFS_KEY)).not.toBeNull();
    const s = server();
    hydrateDevice(device());
    await flush();
    expect(s.patches).toHaveLength(1);
    expect(s.patches[0]).toEqual({
      "ui.theme": "cocoa-kraft",
      "ui.font_body": "Literata",
      "ui.list_density": "airy",
      "ui.reading_density": "airy",
      "client.text_size": 1.25,
      "client.layout": "cards",
      "client.order": "oldest",
    });
    // Local values were not touched by the migration.
    expect(prefsStore.get().font).toBe("literata");
    expect(localStorage.getItem(SYNC_FLAG_KEY)).toBe("1");
    // A second load, or the profile emptied later, does not migrate again: the server wins.
    resetDeviceSync();
    hydrateDevice(device());
    await flush();
    expect(s.patches).toHaveLength(1);
    expect(prefsStore.get().font).toBe("default");
  });

  it("does not migrate over a profile that already has values", async () => {
    updatePrefs({ font: "literata" });
    const s = server();
    hydrateDevice(device({ profile: { "client.order": "oldest" }, merged: { ...DEFAULTS, "client.order": "oldest" } }));
    await flush();
    expect(s.patches).toHaveLength(0);
    expect(prefsStore.get().font).toBe("default");
    expect(devicePrefsStore.get().order).toBe("oldest");
  });

  it("does not write anything for a device with nothing of its own and no old values", async () => {
    const s = server();
    hydrateDevice(device());
    await vi.advanceTimersByTimeAsync(2000);
    expect(s.patches).toHaveLength(0);
    expect(pendingChanges()).toEqual({});
  });

  it("hydrates only once per device", () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    server();
    hydrateDevice(device({ merged: { ...DEFAULTS, "client.layout": "inbox" } }));
    updateDevicePrefs({ layout: "cards" });
    hydrateDevice(device({ merged: { ...DEFAULTS, "client.layout": "inbox" } })); // a bootstrap refetch
    expect(devicePrefsStore.get().layout).toBe("cards");
  });

  it("a change made before the bootstrap arrives is kept as unsent and wins over the profile", async () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    startDeviceSync(); // app start
    updatePrefs({ textSize: 1.5 }); // while the bootstrap is still loading (or only a stored copy answered)
    expect(JSON.parse(localStorage.getItem(SYNC_DIRTY_KEY) ?? "{}")).toEqual({ "client.text_size": 1.5 });
    const s = server({ "client.layout": "compact" });
    hydrateDevice(s.view());
    expect(prefsStore.get().textSize).toBe(1.5);
    expect(devicePrefsStore.get().layout).toBe("compact"); // everything else is the server's
    await flush();
    expect(s.patches).toEqual([{ "client.text_size": 1.5 }]);
  });

  it("an unsent change from the last visit survives an unrelated change made before hydration", () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    localStorage.setItem(SYNC_DIRTY_KEY, JSON.stringify({ "client.order": "oldest" }));
    updateDevicePrefs({ order: "oldest" }); // the local cache already holds it
    startDeviceSync();
    updateDevicePrefs({ layout: "cards" });
    expect(JSON.parse(localStorage.getItem(SYNC_DIRTY_KEY) ?? "{}")).toEqual({ "client.order": "oldest", "client.layout": "cards" });
    server();
    hydrateDevice(device());
    expect(devicePrefsStore.get()).toMatchObject({ order: "oldest", layout: "cards" });
  });

  it("a server with no device profile leaves everything local", () => {
    hydrateDevice(undefined);
    expect(syncStore.get().status).toBe("off");
    updatePrefs({ font: "inter" });
    expect(pendingChanges()).toBeTruthy();
  });
});

describe("saving", () => {
  const ready = async (initial: Record<string, unknown> = {}) => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    const s = server(initial);
    hydrateDevice(s.view());
    return s;
  };

  it("batches a burst of changes into one PATCH 500 ms after the last, latest value winning", async () => {
    const s = await ready();
    updatePrefs({ font: "inter" });
    await vi.advanceTimersByTimeAsync(300);
    updatePrefs({ font: "manrope", textSize: 1.25 });
    await vi.advanceTimersByTimeAsync(300);
    expect(s.patches).toHaveLength(0); // the second change restarted the clock
    updateDevicePrefs({ order: "oldest" });
    await vi.advanceTimersByTimeAsync(499);
    expect(s.patches).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(2);
    expect(s.patches).toEqual([{ "ui.font_body": "Manrope", "client.text_size": 1.25, "client.order": "oldest" }]);
    expect(syncStore.get().status).toBe("idle");
    // Nothing more is sent once the server has confirmed it.
    await vi.advanceTimersByTimeAsync(2000);
    expect(s.patches).toHaveLength(1);
  });

  it("sends the theme pair as ui.theme:system plus day and night", async () => {
    const s = await ready();
    updateTheme({ day: "linen", night: "carbon" });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches).toEqual([{ "ui.theme_day": "linen", "ui.theme_night": "carbon" }]);
    updateTheme({ mode: "fixed", fixed: "tracing" });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches[1]).toEqual({ "ui.theme": "tracing" });
  });

  it("sends the schedule as ui.theme_schedule and only the times that changed", async () => {
    const s = await ready();
    updateTheme({ mode: "follow", schedule: true });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches).toEqual([{ "ui.theme_schedule": true }]);
    updateTheme({ nightStart: "22:00" });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches[1]).toEqual({ "ui.theme_night_start": "22:00" });
  });

  it("a change made back to the confirmed value is not sent", async () => {
    const s = await ready();
    updatePrefs({ font: "inter" });
    updatePrefs({ font: "default" });
    await vi.advanceTimersByTimeAsync(2000);
    expect(s.patches).toHaveLength(0);
  });

  it("clears an override with null when the device goes back to having no value", async () => {
    const s = await ready({ "client.list_width": 400 });
    expect(devicePrefsStore.get().listWidth).toBe(400);
    updateDevicePrefs({ listWidth: null });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches).toEqual([{ "client.list_width": null }]);
  });

  it("keeps the local value on failure, shows the error, and Retry sends it again", async () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    let fail = true;
    const patches: unknown[] = [];
    mockFetch({
      "PATCH /api/device": (_u, init) => {
        patches.push(JSON.parse(String(init?.body)));
        return fail ? new Response("nope", { status: 500 }) : json(device({ profile: { "ui.font_body": "Inter" }, merged: { ...DEFAULTS, "ui.font_body": "Inter" } }));
      },
    });
    hydrateDevice(device());
    updatePrefs({ font: "inter" });
    await vi.advanceTimersByTimeAsync(600);
    expect(syncStore.get().status).toBe("error");
    expect(prefsStore.get().font).toBe("inter"); // never rolled back
    expect(patches).toHaveLength(1);
    fail = false;
    await retrySave();
    expect(patches).toHaveLength(2);
    expect(syncStore.get().status).toBe("idle");
    expect(pendingChanges()).toEqual({});
  });

  it("a rejected key keeps its local value, is not sent again, and the rest still is", async () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    const patches: Record<string, unknown>[] = [];
    mockFetch({
      "PATCH /api/device": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as Record<string, unknown>;
        patches.push(b);
        if ("client.voice" in b) return json({ error: "invalid_settings", keys: ["client.voice"], issues: [{ key: "client.voice", message: "bad" }] }, 400);
        return json(device({ merged: { ...DEFAULTS, ...b } }));
      },
    });
    hydrateDevice(device());
    updatePrefs({ voice: "some voice", rate: 1.5 });
    await vi.advanceTimersByTimeAsync(600);
    expect(patches).toHaveLength(2);
    expect(patches[1]).toEqual({ "client.rate": 1.5 });
    expect(prefsStore.get().voice).toBe("some voice");
    expect(syncStore.get()).toMatchObject({ refused: 1 });
    updatePrefs({ textSize: 1.25 });
    await vi.advanceTimersByTimeAsync(600);
    expect(patches[2]).toEqual({ "client.text_size": 1.25 }); // the refused voice is not re-sent
  });

  it("puts unsent changes back after a reload, on top of the server values, and sends them", async () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    localStorage.setItem(SYNC_DIRTY_KEY, JSON.stringify({ "ui.font_body": "Inter", "client.order": "oldest" }));
    const s = server();
    hydrateDevice(s.view());
    expect(prefsStore.get().font).toBe("inter");
    expect(devicePrefsStore.get().order).toBe("oldest");
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches).toEqual([{ "ui.font_body": "Inter", "client.order": "oldest" }]);
    expect(localStorage.getItem(SYNC_DIRTY_KEY)).toBeNull();
  });

  it("reset to defaults asks the server to clear the overrides and adopts what it answers", async () => {
    const s = await ready({ "ui.font_body": "Inter", "client.layout": "cards" });
    expect(prefsStore.get().font).toBe("inter");
    await copySettingsFrom("defaults");
    expect(s.calls.some((c) => c.method === "POST" && c.url.pathname === "/api/device/copy-from/defaults")).toBe(true);
    expect(prefsStore.get().font).toBe("default");
    expect(devicePrefsStore.get().layout).toBe("magazine");
    await vi.advanceTimersByTimeAsync(2000);
    expect(s.patches).toHaveLength(0); // adopting the answer is not a change to send
  });
});

// Every key the server accepts (internal/api/devices.go clientDefs plus the device-scoped ui.* keys this client mirrors).
const SERVER_CLIENT_KEYS = [
  "client.layout", "client.layout_overrides", "client.order", "client.search_order", "client.inbox_thumbs", "client.peek_seen", "client.article_width",
  "client.list_width", "client.sidebar_width", "client.link_target", "client.unread_badge", "client.text_size",
  "client.adjust_separately", "client.shortcuts", "client.spacing", "client.motion", "client.large_targets",
  "client.listen", "client.voice", "client.rate", "client.collapsed_folders", "client.highlight_keywords",
];

describe("every device pref is carried both ways (review finding 2)", () => {
  it("profileOf has a key for every server client.* key", () => {
    const keys = Object.keys(profileOf(local()));
    for (const k of SERVER_CLIENT_KEYS) expect(keys, k).toContain(k);
  });

  it("round-trips a non-default value of every mapped key through the profile and back", () => {
    const changed: Record<string, unknown> = {
      "ui.theme": "graphite", "ui.theme_day": "linen", "ui.theme_night": "carbon", "ui.font_body": "Inter",
      "ui.list_density": "airy", "ui.reading_density": "airy", "ui.mark_read_on_scroll": true,
      "client.layout": "cards", "client.layout_overrides": { feed: { "5": "compact" }, folder: { "7": "inbox" } },
      "client.order": "oldest", "client.search_order": "newest", "client.inbox_thumbs": "off", "client.peek_seen": true, "client.article_width": "wide",
      "client.list_width": 400, "client.sidebar_width": 300, "client.link_target": "same", "client.unread_badge": "dot",
      "client.text_size": 1.25, "client.adjust_separately": true, "client.shortcuts": false, "client.spacing": "roomy",
      "client.motion": "off", "client.large_targets": true, "client.listen": true, "client.voice": "Samantha",
      "client.rate": 1.2, "client.collapsed_folders": ["3", "9"], "client.highlight_keywords": false,
    };
    const back = profileOf(deriveLocal({ ...DEFAULTS, ...changed }, local()));
    for (const [k, v] of Object.entries(changed)) expect(back[k], k).toEqual(v);
  });

  it("hydrating keeps a Dot unread badge and turned-off highlights the server holds, and sends them back untouched", async () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    const s = server({ "client.unread_badge": "dot", "client.highlight_keywords": false });
    hydrateDevice(s.view());
    expect(devicePrefsStore.get().unreadBadge).toBe("dot");
    expect(devicePrefsStore.get().highlightKeywords).toBe(false);
    await vi.advanceTimersByTimeAsync(2000);
    expect(s.patches).toHaveLength(0);
    updateDevicePrefs({ unreadBadge: "off", highlightKeywords: true });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches).toEqual([{ "client.unread_badge": "off", "client.highlight_keywords": true }]);
  });
});

describe("another tab or device changed a different key (review finding 1)", () => {
  const ready = () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    const s = server();
    hydrateDevice(s.view());
    return s;
  };

  it("does not send this tab's stale value back over it; the server value is applied locally", async () => {
    const s = ready();
    s.profile["ui.theme"] = "graphite"; // tab A / another browser, unseen by this tab
    updatePrefs({ textSize: 1.25 });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches).toEqual([{ "client.text_size": 1.25 }]);
    await vi.advanceTimersByTimeAsync(3000);
    expect(s.patches).toHaveLength(1); // no {ui.theme:"system"} undoing the other change
    expect(s.profile["ui.theme"]).toBe("graphite");
    expect(themeStore.get()).toMatchObject({ mode: "fixed", fixed: "graphite" });
    expect(localStorage.getItem(SYNC_DIRTY_KEY)).toBeNull();
    expect(pendingChanges()).toEqual({});
  });

  it("keeps a local change made after the patch was computed", async () => {
    const s = ready();
    s.profile["ui.theme"] = "graphite";
    updatePrefs({ textSize: 1.25 });
    const p = flush();
    updateTheme({ mode: "fixed", fixed: "linen" }); // the user changes the same key while the request is out
    await p;
    await vi.advanceTimersByTimeAsync(600);
    expect(themeStore.get()).toMatchObject({ fixed: "linen" });
    expect(s.profile["ui.theme"]).toBe("linen");
  });

  it("follows the local cache when another tab writes it (storage event)", () => {
    ready();
    const next = { ...themeStore.get(), mode: "fixed", fixed: "graphite" };
    window.dispatchEvent(new StorageEvent("storage", { key: "kipple.theme.v1", newValue: JSON.stringify(next) }));
    expect(themeStore.get()).toMatchObject({ mode: "fixed", fixed: "graphite" });
    const prefs = { ...prefsStore.get(), textSize: 1.5 };
    window.dispatchEvent(new StorageEvent("storage", { key: PREFS_KEY, newValue: JSON.stringify(prefs) }));
    expect(prefsStore.get().textSize).toBe(1.5);
    const dp = { ...devicePrefsStore.get(), order: "oldest" };
    window.dispatchEvent(new StorageEvent("storage", { key: DEVICE_PREFS_KEY, newValue: JSON.stringify(dp) }));
    expect(devicePrefsStore.get().order).toBe("oldest");
  });

  it("a cache written by a tab on an older build (no schedule times) does not reset this tab's times", async () => {
    const s = await ready();
    updateTheme({ mode: "follow", schedule: true, nightStart: "22:00", dayStart: "06:30" });
    await vi.advanceTimersByTimeAsync(600);
    const old = { mode: "fixed", fixed: "graphite", day: "paper", night: "midnight" };
    window.dispatchEvent(new StorageEvent("storage", { key: "kipple.theme.v1", newValue: JSON.stringify(old) }));
    expect(themeStore.get()).toMatchObject({ mode: "fixed", fixed: "graphite", nightStart: "22:00", dayStart: "06:30" });
    // That build reads the schedule as follow-system; its whole-object write has no schedule fields: kept here.
    updateTheme({ mode: "follow", schedule: true });
    await vi.advanceTimersByTimeAsync(600);
    const before = s.patches.length;
    const echo = { mode: "follow", fixed: "graphite", day: "paper", night: "midnight" };
    window.dispatchEvent(new StorageEvent("storage", { key: "kipple.theme.v1", newValue: JSON.stringify(echo) }));
    expect(themeStore.get()).toMatchObject({ mode: "follow", schedule: true, nightStart: "22:00" });
    // ...and the cache the next load paints from has them again.
    expect(JSON.parse(localStorage.getItem("kipple.theme.v1")!)).toMatchObject({ schedule: true, nightStart: "22:00", dayStart: "06:30" });
    await vi.advanceTimersByTimeAsync(600);
    expect(s.patches.slice(before)).toEqual([]);
    // A build that knows the schedule turning it off is followed.
    window.dispatchEvent(new StorageEvent("storage", { key: "kipple.theme.v1", newValue: JSON.stringify({ ...themeStore.get(), schedule: false }) }));
    expect(themeStore.get().schedule).toBe(false);
    await vi.advanceTimersByTimeAsync(600);
    for (const p of s.patches) {
      expect(p).not.toHaveProperty("ui.theme_night_start", "21:00");
      expect(p).not.toHaveProperty("ui.theme_day_start", "07:00");
    }
  });
});

describe("migration only carries what the old caches held (review finding 3)", () => {
  it("does not send untouched keys as device overrides: the account-wide mark-read-on-scroll survives", async () => {
    updatePrefs({ font: "literata" });
    localStorage.setItem(PREFS_KEY, JSON.stringify({ ...prefsStore.get(), markReadOnScroll: false })); // F4 wrote the whole object
    const s = server({}, { "ui.mark_read_on_scroll": true });
    hydrateDevice(s.view());
    await flush();
    expect(s.patches).toEqual([{ "ui.font_body": "Literata" }]);
    expect(prefsStore.get().markReadOnScroll).toBe(true);
  });

  it("does not send keys the legacy stores never held, even if the local default differs from the server's", async () => {
    localStorage.setItem(PREFS_KEY, JSON.stringify({ font: "literata" }));
    prefsStore.set({ ...prefsStore.get(), font: "literata" });
    const s = server({}, { "client.sidebar_width": 300, "client.unread_badge": "dot" });
    hydrateDevice(s.view());
    await flush();
    expect(s.patches).toEqual([{ "ui.font_body": "Literata" }]);
    expect(devicePrefsStore.get().sidebarWidth).toBe(300);
    expect(devicePrefsStore.get().unreadBadge).toBe("dot");
  });
});

describe("migration ignores untouched defaults in a fully written store", () => {
  it("does not push a default value over a different server default", async () => {
    localStorage.setItem(PREFS_KEY, JSON.stringify({ ...prefsStore.get(), font: "literata" })); // textSize etc. are the defaults
    prefsStore.set({ ...prefsStore.get(), font: "literata" });
    const s = server({}, { "client.text_size": 1.25 });
    hydrateDevice(s.view());
    await flush();
    expect(s.patches).toEqual([{ "ui.font_body": "Literata" }]);
    expect(prefsStore.get().textSize).toBe(1.25);
  });
});

describe("refused settings (review finding 4)", () => {
  function refusing() {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    const patches: Record<string, unknown>[] = [];
    mockFetch({
      "PATCH /api/device": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as Record<string, unknown>;
        patches.push(b);
        if (b["client.voice"] === "bad") return json({ error: "invalid_settings", keys: ["client.voice"] }, 400);
        return json(device({ merged: { ...DEFAULTS, ...b } }));
      },
    });
    hydrateDevice(device());
    return patches;
  }

  it("a later successful save of a new value clears the notice", async () => {
    const patches = refusing();
    updatePrefs({ voice: "bad" });
    await vi.advanceTimersByTimeAsync(600);
    expect(syncStore.get().refused).toBe(1);
    updatePrefs({ voice: "good" });
    await vi.advanceTimersByTimeAsync(600);
    expect(patches.at(-1)).toEqual({ "client.voice": "good" });
    expect(syncStore.get()).toMatchObject({ refused: 0, status: "idle" });
  });

  it("changing the value back to the confirmed one also clears it", async () => {
    refusing();
    updatePrefs({ voice: "bad" });
    await vi.advanceTimersByTimeAsync(600);
    updatePrefs({ voice: "" });
    await vi.advanceTimersByTimeAsync(600);
    expect(syncStore.get().refused).toBe(0);
  });

  it("Retry does not re-send the known-bad value; Discard puts the server's value back", async () => {
    const patches = refusing();
    updatePrefs({ voice: "bad" });
    await vi.advanceTimersByTimeAsync(600);
    const n = patches.length;
    await retrySave();
    expect(patches).toHaveLength(n);
    expect(syncStore.get().refused).toBe(1);
    discardRefused();
    expect(prefsStore.get().voice).toBe("");
    expect(syncStore.get()).toMatchObject({ refused: 0 });
    await vi.advanceTimersByTimeAsync(2000);
    expect(patches).toHaveLength(n);
  });
});
