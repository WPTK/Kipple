import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { SKELETON_DELAY_MS, SKELETON_MIN_MS, useSkeletonGate } from "./kit";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("useSkeletonGate", () => {
  it("shows nothing at first, then the skeleton, and keeps it a minimum time", () => {
    const { result, rerender } = renderHook(({ pending }) => useSkeletonGate(pending), { initialProps: { pending: true } });
    expect(result.current).toBe("blank");
    act(() => void vi.advanceTimersByTime(SKELETON_DELAY_MS));
    expect(result.current).toBe("skeleton");
    act(() => void vi.advanceTimersByTime(100));
    rerender({ pending: false });
    expect(result.current).toBe("skeleton");
    act(() => void vi.advanceTimersByTime(SKELETON_MIN_MS - 100));
    expect(result.current).toBe("done");
  });

  it("never shows a skeleton for an answer that comes quickly", () => {
    const { result, rerender } = renderHook(({ pending }) => useSkeletonGate(pending), { initialProps: { pending: true } });
    act(() => void vi.advanceTimersByTime(SKELETON_DELAY_MS - 50));
    rerender({ pending: false });
    expect(result.current).toBe("done");
    act(() => void vi.advanceTimersByTime(1000));
    expect(result.current).toBe("done");
  });

  it("counts the minimum from when loading started again during the hold", () => {
    const { result, rerender } = renderHook(({ pending }) => useSkeletonGate(pending), { initialProps: { pending: true } });
    act(() => void vi.advanceTimersByTime(SKELETON_DELAY_MS));
    rerender({ pending: false });
    act(() => void vi.advanceTimersByTime(SKELETON_MIN_MS - 50));
    rerender({ pending: true });
    act(() => void vi.advanceTimersByTime(300));
    rerender({ pending: false });
    act(() => void vi.advanceTimersByTime(SKELETON_MIN_MS - 350));
    expect(result.current).toBe("skeleton");
    act(() => void vi.advanceTimersByTime(350));
    expect(result.current).toBe("done");
  });

  it("shows the skeleton at once when a list that had an answer loads another", () => {
    const { result, rerender } = renderHook(({ pending }) => useSkeletonGate(pending), { initialProps: { pending: false } });
    expect(result.current).toBe("done");
    rerender({ pending: true });
    expect(result.current).toBe("skeleton");
  });
});
