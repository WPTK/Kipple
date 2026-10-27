import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { SIDEBAR_WIDTH_MIN } from "./devicePrefs";
import { WIDE_MIN_PX, WIDE_QUERY, useMedia } from "./useMedia";
import { columnNeeds } from "./useWidth";

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

  it("keeps all three panes whole at the wide breakpoint, at the default text size", () => {
    const need = columnNeeds(16, false);
    expect(SIDEBAR_WIDTH_MIN + need.list + need.article + 2 * need.gutter).toBeLessThanOrEqual(WIDE_MIN_PX);
    expect(WIDE_QUERY).toBe(`(min-width: ${WIDE_MIN_PX}px)`);
  });
});
