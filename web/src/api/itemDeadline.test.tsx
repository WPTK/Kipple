import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ApiError, authStore } from "./client";
import { ITEM_DEADLINE_MS, useItem } from "./queries";
import { detail, json, mockFetch } from "@/test/mockApi";

/** A request that never answers until the caller cancels it, as one waiting in the browser's queue. */
const hang = (_url: URL, init?: RequestInit) =>
  new Promise<Response>((_resolve, reject) => {
    init?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
  });

function setup() {
  const qc = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return renderHook(() => useItem("1001"), { wrapper });
}

beforeEach(() => {
  vi.useFakeTimers();
  authStore.set("in");
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("useItem deadline", () => {
  it("drops a read that has not answered by the deadline and asks once more", async () => {
    let n = 0;
    const { calls } = mockFetch({ "GET /api/items/1001": (url, init) => (++n === 1 ? hang(url, init) : json(detail(1))) });
    const { result } = setup();
    await act(() => vi.advanceTimersByTimeAsync(ITEM_DEADLINE_MS - 1));
    expect(calls).toHaveLength(1);
    expect(result.current.isPending).toBe(true);
    await act(() => vi.advanceTimersByTimeAsync(5_000)); // past the deadline and the retry delay
    expect(calls).toHaveLength(2);
    expect(result.current.data?.id).toBe("1001");
  });

  it("shows an error once the second try also misses the deadline", async () => {
    const { calls } = mockFetch({ "GET /api/items/1001": hang });
    const { result } = setup();
    await act(() => vi.advanceTimersByTimeAsync(ITEM_DEADLINE_MS * 2 + 5_000));
    expect(calls).toHaveLength(2);
    expect(result.current.isError).toBe(true);
    expect(result.current.error).toBeInstanceOf(ApiError);
  });

  it("does not retry a definite answer", async () => {
    const { calls } = mockFetch({ "GET /api/items/1001": () => json({ error: "gone" }, 404) });
    const { result } = setup();
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(calls).toHaveLength(1);
    expect(result.current.isError).toBe(true);
  });
});
