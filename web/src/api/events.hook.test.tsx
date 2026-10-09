import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { WATCHDOG_MS, handleServerEvent, initialLive, liveStore, reconnectDelay, useServerEvents } from "./events";
import { bumpUnread, keys, resetCountsGuard } from "./queries";
import type { ServerEvent } from "./types";
import { authStore } from "./client";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

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
  emit(t: string, data: unknown, lastEventId = "") {
    for (const f of this.listeners.get(t) ?? []) f({ data: JSON.stringify(data), lastEventId } as MessageEvent<string>);
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

  it("still falls back when the stream opens and dies each time (a proxy that accepts then resets)", async () => {
    mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 4 }) });
    const { unmount } = setup();
    const es = FakeES.last;
    act(() => es?.onopen?.());
    act(() => es?.onerror?.());
    act(() => es?.onopen?.());
    act(() => es?.onerror?.());
    expect(liveStore.get().transport).toBe("fallback");
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
      const statusBefore = calls.filter((c) => c.url.pathname === "/api/status").length;
      act(() => FakeES.all[2]!.onopen?.());
      expect(liveStore.get().transport).toBe("open");
      await vi.advanceTimersByTimeAsync(0);
      // Reconnect reconciles from /api/status (no invalidation of the lists).
      expect(calls.filter((c) => c.url.pathname === "/api/status").length).toBeGreaterThan(statusBefore);
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

  // The server answers 503 (with Retry-After) while its event-stream slots are full. A browser treats any non-200 answer
  // as a failed connection: the source is CLOSED and the stream never opened. Recovery must come from our own backoff.
  it("recovers once a refused stream (503, never opened) gets a free slot", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      mockFetch({
        "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 4 }),
        "GET /api/bootstrap": () => json({}),
      });
      const { unmount } = setup();
      for (let i = 0; i < 3; i += 1) {
        act(() => FakeES.all[i]!.fail());
        await act(() => vi.advanceTimersByTimeAsync(1_500 * 2 ** i));
      }
      expect(FakeES.all).toHaveLength(4);
      expect(liveStore.get().transport).toBe("fallback");
      act(() => FakeES.all[3]!.emit("heartbeat", {}));
      expect(liveStore.get().transport).toBe("open");
      act(() => FakeES.all[3]!.emit("counts", { unread_total: 9, feeds: {} }));
      expect(FakeES.all).toHaveLength(4);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  // Issue #29: a poll still awaiting /api/status when the stream recovers and fails again (starting a
  // second loop) must not reschedule itself when it finally answers, or two loops poll at once.
  it("never runs two fallback poll loops at once when a slow poll answers after a new loop started", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      let release: (() => void) | undefined;
      let n = 0;
      const { calls } = mockFetch({
        "GET /api/status": () => {
          const body = json({ runs: [], inflight: 0, unread_total: 0 });
          if (++n > 1) return body;
          // The first poll hangs until the test releases it.
          return new Promise<Response>((resolve) => {
            release = () => resolve(body);
          });
        },
        "GET /api/bootstrap": () => json({}),
      });
      const status = () => calls.filter((c) => c.url.pathname === "/api/status").length;
      const { unmount } = setup();
      const es = FakeES.last!;

      // Loop 1 starts and its first poll hangs.
      act(() => es.onerror?.());
      act(() => es.onerror?.());
      expect(liveStore.get().transport).toBe("fallback");
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(status()).toBe(1);
      expect(release).toBeDefined();

      // The stream delivers (polling stops, the reconnect reconciles), then fails twice again: loop 2 starts.
      act(() => es.emit("counts", { unread_total: 0, feeds: {} }));
      expect(liveStore.get().transport).toBe("open");
      act(() => es.onerror?.());
      act(() => es.onerror?.());
      expect(liveStore.get().transport).toBe("fallback");
      await act(() => vi.advanceTimersByTimeAsync(0));
      const started = status(); // loop 1's hung poll, the reconcile, loop 2's first poll
      expect(started).toBe(3);

      // Loop 1's poll finally answers while the transport is fallback again: it must not reschedule.
      await act(async () => {
        release!();
        await vi.advanceTimersByTimeAsync(0);
      });

      // Idle polling is every 60 s: one loop polls three times in three minutes, two loops would poll six.
      await act(() => vi.advanceTimersByTimeAsync(3 * 60_000 + 500));
      expect(status() - started).toBe(3);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("reconnect reconciliation", () => {
  it("clears a run whose run.done was missed, without refetching any list", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      mockFetch({
        "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 2 }),
        "GET /api/bootstrap": () => json(bootstrap),
      });
      const qc = new QueryClient();
      qc.setQueryData(keys.items({ view: "unread" }), { pages: [pageOf([card(1)])], pageParams: [""] });
      qc.setQueryData(keys.bootstrap, bootstrap);
      const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
      const { unmount } = renderHook(() => useServerEvents(true), { wrapper });
      act(() => FakeES.all[0]!.emit("run.start", { run_id: "7", kind: "manual", total: 3 }));
      expect(Object.keys(liveStore.get().runs)).toEqual(["7"]);
      act(() => FakeES.all[0]!.drop());
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      act(() => FakeES.all[1]!.onopen?.());
      await act(() => vi.advanceTimersByTimeAsync(10));
      expect(liveStore.get().runs).toEqual({});
      expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(false);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("passes the last seen event id on the recreated stream", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 0 }), "GET /api/bootstrap": () => json(bootstrap) });
      const { unmount } = setup();
      act(() => FakeES.all[0]!.emit("counts", { unread_total: 1, feeds: {} }, "41"));
      act(() => FakeES.all[0]!.drop());
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(FakeES.all[1]!.url).toBe("/api/events?last_event_id=41");
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("re-checks status when the tab becomes visible with a run showing", async () => {
    const { calls } = mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 0 }), "GET /api/bootstrap": () => json(bootstrap) });
    const { unmount } = setup();
    act(() => FakeES.all[0]!.emit("run.start", { run_id: "7", kind: "manual", total: 3 }));
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await waitFor(() => expect(liveStore.get().runs).toEqual({}));
    expect(calls.some((c) => c.url.pathname === "/api/status")).toBe(true);
    unmount();
  });

  it("seeds runs from the bootstrap when a run is already going at load", async () => {
    mockFetch({});
    const qc = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { unmount } = renderHook(() => useServerEvents(true), { wrapper });
    act(() => qc.setQueryData(keys.bootstrap, { ...bootstrap, runs: [{ id: "3", kind: "manual", done: 1, total: 5, new_items: 0, errors: 0 }] }));
    await waitFor(() => expect(Object.keys(liveStore.get().runs)).toEqual(["3"]));
    unmount();
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

describe("hung-stream watchdog", () => {
  const fake = () => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });

  it("reconnects when nothing arrives for 45 s after a heartbeat was seen", async () => {
    fake();
    try {
      mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 0 }), "GET /api/bootstrap": () => json({}) });
      const { unmount } = setup();
      const first = FakeES.all[0]!;
      act(() => first.onopen?.());
      act(() => first.emit("heartbeat", {}));
      await act(() => vi.advanceTimersByTimeAsync(WATCHDOG_MS - 1_000));
      act(() => first.emit("heartbeat", {})); // any event restarts the clock
      await act(() => vi.advanceTimersByTimeAsync(WATCHDOG_MS - 1_000));
      expect(first.closed).toBe(false);
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(first.closed).toBe(true);
      await act(() => vi.advanceTimersByTimeAsync(1_500));
      expect(FakeES.all).toHaveLength(2);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stays out of the way of older servers that never send a heartbeat", async () => {
    fake();
    try {
      const { unmount } = setup();
      const first = FakeES.all[0]!;
      act(() => first.onopen?.());
      await act(() => vi.advanceTimersByTimeAsync(WATCHDOG_MS * 3));
      expect(first.closed).toBe(false);
      expect(FakeES.all).toHaveLength(1);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("counts events versus optimistic bumps", () => {
  it("a stale counts event right after a local bump is skipped, then the truth is refetched", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    try {
      resetCountsGuard();
      const qc = new QueryClient();
      const boot = {
        counts: { unread: 3, starred: 0 },
        feeds: [{ id: "1", folder_id: "1", unread: 3 }],
        folders: [{ id: "1", unread: 3 }],
      };
      qc.setQueryData(keys.bootstrap, boot);
      const inval = vi.spyOn(qc, "invalidateQueries");
      bumpUnread(qc, "1", -1);
      const stale = { type: "counts", data: { unread_total: 3, feeds: { "1": 3 } } } as unknown as ServerEvent;
      handleServerEvent(qc, stale);
      expect((qc.getQueryData(keys.bootstrap) as typeof boot).counts.unread).toBe(2);
      await vi.advanceTimersByTimeAsync(2_000);
      expect(inval).toHaveBeenCalledWith({ queryKey: keys.bootstrap });
      // Outside the window an event applies as before.
      handleServerEvent(qc, { type: "counts", data: { unread_total: 1, feeds: { "1": 1 } } } as unknown as ServerEvent);
      expect((qc.getQueryData(keys.bootstrap) as typeof boot).counts.unread).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
