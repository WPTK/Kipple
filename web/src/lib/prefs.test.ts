import { describe, expect, it } from "vitest";
import { DEFAULT_PREFS, applyPrefs } from "./prefs";

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
