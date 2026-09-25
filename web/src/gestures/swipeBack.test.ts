import { afterEach, describe, expect, it, vi } from "vitest";
import { swipeBackBlocked } from "./useSwipeBack";

afterEach(() => vi.unstubAllGlobals());

describe("swipeBackBlocked", () => {
  const root = document.createElement("div");
  const p = document.createElement("p");
  root.appendChild(p);

  it("allows a plain paragraph", () => {
    expect(swipeBackBlocked(p, root)).toBe(false);
  });

  it("is blocked while pinch-zoomed in (a drag pans the page)", () => {
    vi.stubGlobal("visualViewport", { scale: 2 });
    expect(swipeBackBlocked(p, root)).toBe(true);
    vi.stubGlobal("visualViewport", { scale: 1 });
    expect(swipeBackBlocked(p, root)).toBe(false);
  });
});
