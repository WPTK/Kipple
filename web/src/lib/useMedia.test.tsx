import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useMedia } from "./useMedia";

afterEach(() => vi.unstubAllGlobals());

describe("useMedia", () => {
  it("subscribes once across re-renders", () => {
    const add = vi.fn();
    vi.spyOn(window, "matchMedia").mockImplementation(
      (q) => ({ matches: true, media: q, addEventListener: add, removeEventListener() {} }) as unknown as MediaQueryList,
    );
    const { result, rerender } = renderHook(() => useMedia("(min-width: 900px)"));
    rerender();
    rerender();
    expect(result.current).toBe(true);
    expect(add).toHaveBeenCalledTimes(1);
  });
});
