import { afterEach, describe, expect, it, vi } from "vitest";
import { DEFAULT_PREFS, applyPrefs, parsePrefs, prefsStore, touchFirst, updatePrefs } from "./prefs";

/** Pretend to be a device with these media features. */
function media(features: Record<string, boolean>) {
  vi.stubGlobal(
    "matchMedia",
    (q: string) => ({ matches: features[q.replace(/^\(|\)$/g, "")] ?? false, addEventListener() {}, removeEventListener() {} }),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("single-key shortcuts default", () => {
  it("is off on a touch-first device (coarse pointer, no mouse or trackpad), on elsewhere", () => {
    media({ "pointer: coarse": true, "any-pointer: fine": false });
    expect(touchFirst()).toBe(true);
    expect(parsePrefs(null).shortcuts).toBe(false);
    media({ "pointer: fine": true, "any-pointer: fine": true });
    expect(touchFirst()).toBe(false);
    expect(parsePrefs(null).shortcuts).toBe(true);
  });

  it("an iPad with a trackpad or mouse attached (a fine secondary pointer) keeps them on", () => {
    media({ "pointer: coarse": true, "any-pointer: fine": true });
    expect(touchFirst()).toBe(false);
    expect(parsePrefs(null).shortcuts).toBe(true);
  });

  it("an old stored 'on' that was only the default follows the device; an explicit choice is kept", () => {
    media({ "pointer: coarse": true, "any-pointer: fine": false });
    // Saved by an earlier build, which stored shortcuts:true for everyone.
    expect(parsePrefs(JSON.stringify({ textSize: 1.25, shortcuts: true })).shortcuts).toBe(false);
    // Turned off on purpose (old or new builds).
    expect(parsePrefs(JSON.stringify({ shortcuts: false })).shortcuts).toBe(false);
    // Turned on on purpose on a phone.
    expect(parsePrefs(JSON.stringify({ shortcuts: true, shortcutsChosen: true })).shortcuts).toBe(true);
    media({ "pointer: fine": true, "any-pointer: fine": true });
    expect(parsePrefs(JSON.stringify({ shortcuts: false })).shortcuts).toBe(false);
  });

  it("a device default saved back (off, flagged not chosen) is not read as a choice; a pre-flag 'off' still is", () => {
    media({ "pointer: coarse": true, "any-pointer: fine": false });
    const saved = JSON.stringify({ ...parsePrefs(null) });
    expect(parsePrefs(saved).shortcutsChosen).toBe(false);
    media({ "pointer: fine": true, "any-pointer: fine": true });
    expect(parsePrefs(saved).shortcuts).toBe(true);
    expect(parsePrefs(JSON.stringify({ shortcuts: false })).shortcutsChosen).toBe(true);
  });

  it("phone first load, another pref changes, reload: a hardware keyboard still turns shortcuts on", async () => {
    media({ "pointer: coarse": true, "any-pointer: fine": false });
    localStorage.clear();
    vi.resetModules();
    const m1 = await import("./prefs");
    m1.initPrefs();
    expect(m1.prefsStore.get().shortcuts).toBe(false);
    m1.updatePrefs({ textSize: 1.25 });
    // The whole store was saved, including the device-default "off".
    vi.resetModules();
    const m2 = await import("./prefs");
    expect(m2.prefsStore.get().shortcutsChosen).toBe(false);
    m2.initPrefs();
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
    expect(m2.prefsStore.get().shortcuts).toBe(true);
    localStorage.clear();
  });

  it("changing the setting marks it as chosen", () => {
    const before = prefsStore.get();
    updatePrefs({ shortcuts: false });
    expect(prefsStore.get().shortcutsChosen).toBe(true);
    prefsStore.set(before);
  });
});

describe("the reading font", () => {
  it("styles articles only unless Use it everywhere is on, and Default clears both", () => {
    const root = document.createElement("div");
    applyPrefs({ ...DEFAULT_PREFS, font: "inter" }, root);
    expect(root.style.getPropertyValue("--kp-reading-font")).toContain("Inter Variable");
    expect(root.style.getPropertyValue("--kp-app-font")).toBe("");
    applyPrefs({ ...DEFAULT_PREFS, font: "inter", fontEverywhere: true }, root);
    expect(root.style.getPropertyValue("--kp-app-font")).toContain("Inter Variable");
    expect(root.style.getPropertyValue("--kp-reading-font")).toBe(root.style.getPropertyValue("--kp-app-font"));
    applyPrefs({ ...DEFAULT_PREFS, font: "default" }, root);
    expect(root.style.getPropertyValue("--kp-app-font")).toBe("");
    expect(root.style.getPropertyValue("--kp-reading-font")).toBe("");
  });
});
