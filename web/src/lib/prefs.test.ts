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

  it("changing the setting marks it as chosen", () => {
    const before = prefsStore.get();
    updatePrefs({ shortcuts: false });
    expect(prefsStore.get().shortcutsChosen).toBe(true);
    prefsStore.set(before);
  });
});

describe("one font for everything but Settings and menus", () => {
  it("sets the app font and the article font together, and clears both for Default", () => {
    const root = document.createElement("div");
    applyPrefs({ ...DEFAULT_PREFS, font: "inter" }, root);
    expect(root.style.getPropertyValue("--kp-app-font")).toContain("Inter Variable");
    expect(root.style.getPropertyValue("--kp-reading-font")).toBe(root.style.getPropertyValue("--kp-app-font"));
    applyPrefs({ ...DEFAULT_PREFS, font: "default" }, root);
    expect(root.style.getPropertyValue("--kp-app-font")).toBe("");
    expect(root.style.getPropertyValue("--kp-reading-font")).toBe("");
  });
});
