import { describe, expect, it } from "vitest";
import { interpretKey, isTypingTarget } from "./keys";

const on = { typing: false, singleKeys: true };

describe("interpretKey", () => {
  it("maps the single keys", () => {
    const map: Record<string, string> = { j: "next", k: "prev", s: "star", m: "toggleRead", o: "original", r: "refresh", f: "fulltext", u: "up", Escape: "up", "/": "search", "?": "help", G: "bottom" };
    for (const [key, action] of Object.entries(map)) expect(interpretKey({ key }, false, on).action, key).toBe(action);
  });

  it("never binds Ctrl, Cmd or Alt combinations", () => {
    for (const mod of ["ctrlKey", "metaKey", "altKey"] as const) {
      expect(interpretKey({ key: "s", [mod]: true }, false, on).action).toBeNull();
    }
  });

  it("does nothing while typing or composing", () => {
    expect(interpretKey({ key: "j" }, false, { typing: true, singleKeys: true }).action).toBeNull();
    expect(interpretKey({ key: "j", isComposing: true }, false, on).action).toBeNull();
  });

  it("the setting off disables letter keys but leaves Escape and ?", () => {
    const off = { typing: false, singleKeys: false };
    expect(interpretKey({ key: "j" }, false, off).action).toBeNull();
    expect(interpretKey({ key: "s" }, false, off).action).toBeNull();
    expect(interpretKey({ key: "Escape" }, false, off).action).toBe("up");
    expect(interpretKey({ key: "?" }, false, off).action).toBe("help");
  });

  it("g starts a chord that resolves on the next key and then clears", () => {
    const first = interpretKey({ key: "g" }, false, on);
    expect(first).toEqual({ action: null, pendingG: true });
    expect(interpretKey({ key: "g" }, true, on)).toEqual({ action: "top", pendingG: false });
    expect(interpretKey({ key: "i" }, true, on).action).toBe("goUnread");
    expect(interpretKey({ key: "s" }, true, on).action).toBe("goStarred");
    expect(interpretKey({ key: "," }, true, on).action).toBe("goSettings");
    // An unbound second key cancels the chord without acting.
    expect(interpretKey({ key: "x" }, true, on)).toEqual({ action: null, pendingG: false });
  });

  it("leaves arrows and Space to the browser", () => {
    for (const key of ["ArrowDown", "ArrowUp", " "]) expect(interpretKey({ key }, false, on).action).toBeNull();
  });
});

describe("isTypingTarget", () => {
  it("detects form controls and textbox roles", () => {
    expect(isTypingTarget(document.createElement("input"))).toBe(true);
    expect(isTypingTarget(document.createElement("textarea"))).toBe(true);
    const div = document.createElement("div");
    div.setAttribute("role", "searchbox");
    expect(isTypingTarget(div)).toBe(true);
    expect(isTypingTarget(document.createElement("button"))).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });
});
