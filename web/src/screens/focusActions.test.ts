import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { focusActionsButton, useFocusActions } from "./focusActions";

// The retry loop re-arms itself every 50 ms for a second; after teardown a pending tick reads `document` (#406).

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("focusActionsButton", () => {
  it("focuses the button once it is there, and keeps no timer after it is cancelled", () => {
    const stop = focusActionsButton("late");
    vi.advanceTimersByTime(60);
    const b = document.body.appendChild(document.createElement("button"));
    b.id = "late";
    vi.advanceTimersByTime(60);
    expect(document.activeElement).toBe(b);
    expect(vi.getTimerCount()).toBe(1);
    stop();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("stops by itself after about a second", () => {
    focusActionsButton("never");
    vi.advanceTimersByTime(2000);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("useFocusActions", () => {
  it("clears the retry timer when the component unmounts", () => {
    const { result, unmount } = renderHook(() => useFocusActions());
    result.current("x");
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("lets a newer call replace the one still running", () => {
    const { result } = renderHook(() => useFocusActions());
    result.current("a");
    result.current("b");
    expect(vi.getTimerCount()).toBe(1);
  });
});
