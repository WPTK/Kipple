import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { swipeBackBlocked, useSwipeBack } from "./useSwipeBack";

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("useSwipeBack", () => {
  /** A touch pointer event (jsdom has no PointerEvent). */
  const pe = (type: string, x: number, y = 100) => Object.assign(new Event(type, { bubbles: true }), { pointerType: "touch", pointerId: 1, clientX: x, clientY: y });
  /** A right-swipe across most of the element, starting clear of the left edge. */
  function swipe(el: HTMLElement) {
    const p = document.createElement("p");
    el.append(p);
    p.dispatchEvent(pe("pointerdown", 100));
    p.dispatchEvent(pe("pointermove", 150));
    p.dispatchEvent(pe("pointermove", 900));
    p.dispatchEvent(pe("pointerup", 900));
  }

  it("follows the element: after Try again the new frame swipes back and the old one no longer does", () => {
    const onBack = vi.fn();
    const first = document.createElement("div");
    const second = document.createElement("div");
    document.body.append(first, second);
    const { rerender } = renderHook((p: { el: HTMLElement | null }) => useSwipeBack(p.el, { enabled: true, onBack }), {
      initialProps: { el: first as HTMLElement | null },
    });
    swipe(first);
    expect(onBack).toHaveBeenCalledTimes(1);
    rerender({ el: null }); // the error screen: no frame
    rerender({ el: second }); // Try again: a new frame
    swipe(second);
    expect(onBack).toHaveBeenCalledTimes(2);
    swipe(first); // the detached old frame is not listened to
    expect(onBack).toHaveBeenCalledTimes(2);
  });
});

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
