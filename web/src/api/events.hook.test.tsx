import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { initialLive, liveStore, reconnectDelay, useServerEvents } from "./events";
import { authStore } from "./client";
import { json, mockFetch } from "@/test/mockApi";

class FakeES {
  static CLOSED = 2;
  static last: FakeES | null = null;
  static all: FakeES[] = [];
  readyState = 0;
  listeners = new Map<string, ((m: MessageEvent<string>) => void)[]>();
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeES.last = this;
    FakeES.all.push(this);
  }
  addEventListener(t: string, f: (m: MessageEvent<string>) => void) {
    this.listeners.set(t, [...(this.listeners.get(t) ?? []), f]);
  }
  emit(t: string, data: unknown) {
    for (const f of this.listeners.get(t) ?? []) f({ data: JSON.stringify(data) } as MessageEvent<string>);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
  /** The browser gave up after a non-200 answer: CLOSED, then onerror. */
  fail() {
    this.readyState = 2;
    this.onerror?.();
  }
  /** A dropped connection: the browser would retry natively (readyState CONNECTING) after `retry:`. */
  drop() {
    this.readyState = 0;
    this.onerror?.();
  }
}

function setup() {
  const qc = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return renderHook(() => useServerEvents(true), { wrapper });
}

beforeEach(() => {
  FakeES.all = [];
  liveStore.set(initialLive);
  authStore.set("in");
  vi.stubGlobal("EventSource", FakeES);
});
afterEach(() => vi.unstubAllGlobals());

describe("useServerEvents", () => {
  it("connects to /api/events and marks the transport open on a message", () => {
    const { unmount } = setup();
    expect(FakeES.last?.url).toBe("/api/events");
    act(() => FakeES.last?.emit("run.start", { run_id: "1", kind: "refresh", total: 2 }));
    expect(liveStore.get().transport).toBe("open");
    expect(liveStore.get().runs["1"]?.total).toBe(2);
    unmount();
    expect(FakeES.last?.closed).toBe(true);
  });

  it("falls back to polling /api/status after two errors with no message, and stops when a message arrives", async () => {
    const { calls } = mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 4 }) });
    const { unmount } = setup();
    act(() => FakeES.last?.onerror?.());
    expect(liveStore.get().transport).toBe("connecting");
    expect(calls).toHaveLength(0);
    act(() => FakeES.last?.onerror?.());
    expect(liveStore.get().transport).toBe("fallback");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/status")).toBe(true));

    act(() => FakeES.last?.emit("counts", { unread_total: 1, feeds: {} }));
    expect(liveStore.get().transport).toBe("open");
    unmount();
  });

  it("does not fall back when the failure is a sign-out", () => {
    authStore.set("out");
    const { unmount } = setup();
    act(() => FakeES.last?.onerror?.());
    act(() => FakeES.last?.onerror?.());
    expect(liveStore.get().transport).toBe("connecting");
    unmount();
  });

  it("recreates a CLOSED stream with capped backoff, polls meanwhile, resyncs on reconnect", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      const { calls } = mockFetch({
        "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 4 }),
        "GET /api/bootstrap": () => json({}),
      });
      const { unmount } = setup();
      expect(FakeES.all).toHaveLength(1);
      const first = FakeES.all[0]!;
      act(() => first.fail());
      expect(first.closed).toBe(true);
      expect(FakeES.all).toHaveLength(1);
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(FakeES.all).toHaveLength(2);
      // Still failing: the delay grows, and the second error starts the fallback poll.
      act(() => FakeES.all[1]!.fail());
      expect(liveStore.get().transport).toBe("fallback");
      await vi.advanceTimersByTimeAsync(0);
      expect(calls.some((c) => c.url.pathname === "/api/status")).toBe(true);
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      expect(FakeES.all).toHaveLength(2);
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      expect(FakeES.all).toHaveLength(3);
      const before = liveStore.get().resyncTick;
      act(() => FakeES.all[2]!.onopen?.());
      expect(liveStore.get().transport).toBe("open");
      expect(liveStore.get().resyncTick).toBe(before + 1);
      // Stream is back and stays up: a later drop restarts the delay at about 1 s.
      vi.setSystemTime(Date.now() + 60_000);
      act(() => FakeES.all[2]!.fail());
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(FakeES.all).toHaveLength(4);
      unmount();
      act(() => undefined);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(FakeES.all).toHaveLength(4);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("reconnect discipline (killed backend)", () => {
  it("closes a dropped source at once so the browser's native 3 s retry never stacks on ours", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 0 }) });
      const { unmount } = setup();
      act(() => FakeES.all[0]!.drop());
      expect(FakeES.all[0]!.closed).toBe(true);
      await act(() => vi.advanceTimersByTimeAsync(60_000));
      // Backend stays dead: every attempt is refused instantly. In 60 s the backoff allows about
      // 1 + 2 + 4 + 8 + 16 + 30 s gaps, so at most 7 attempts, never one per 3 s (20) or a burst.
      for (let i = 0; i < 12; i += 1) {
        const last = FakeES.all[FakeES.all.length - 1]!;
        if (!last.closed) act(() => last.fail());
        await act(() => vi.advanceTimersByTimeAsync(60_000));
      }
      let attempts = FakeES.all.length;
      expect(attempts).toBeLessThanOrEqual(20);
      // Never two sources open at once.
      expect(FakeES.all.filter((e) => !e.closed).length).toBeLessThanOrEqual(1);
      // Measure the window: after the delay has grown to the cap, 60 s yields at most 3 attempts.
      const before = FakeES.all.length;
      const last = FakeES.all[FakeES.all.length - 1]!;
      if (!last.closed) act(() => last.fail());
      for (let t = 0; t < 60; t += 1) {
        await act(() => vi.advanceTimersByTimeAsync(1_000));
        const cur = FakeES.all[FakeES.all.length - 1]!;
        if (!cur.closed) act(() => cur.fail());
      }
      attempts = FakeES.all.length - before;
      expect(attempts).toBeLessThanOrEqual(3);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not reset the backoff for a stream that opens and dies at once", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 0 }) });
      const { unmount } = setup();
      for (let i = 0; i < 3; i += 1) {
        const cur = FakeES.all[FakeES.all.length - 1]!;
        act(() => cur.onopen?.());
        act(() => cur.drop());
        await act(() => vi.advanceTimersByTimeAsync(1_500 * 2 ** i));
      }
      // Delays 1 s, 2 s, 4 s: a fourth attempt is not due after 1.5 s more.
      const n = FakeES.all.length;
      const cur = FakeES.all[n - 1]!;
      act(() => cur.onopen?.());
      act(() => cur.drop());
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(FakeES.all).toHaveLength(n);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("reconnectDelay", () => {
  it("doubles from 1 s, caps at 30 s and jitters by 25 percent", () => {
    const mid = () => 0.5;
    expect([0, 1, 2, 3, 4, 5, 6, 9].map((n) => reconnectDelay(n, mid))).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000]);
    expect(reconnectDelay(0, () => 0)).toBe(750);
    expect(reconnectDelay(0, () => 1)).toBe(1250);
  });
});
