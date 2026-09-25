import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { initialLive, liveStore, useServerEvents } from "./events";
import { authStore } from "./client";
import { json, mockFetch } from "@/test/mockApi";

class FakeES {
  static last: FakeES | null = null;
  listeners = new Map<string, ((m: MessageEvent<string>) => void)[]>();
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeES.last = this;
  }
  addEventListener(t: string, f: (m: MessageEvent<string>) => void) {
    this.listeners.set(t, [...(this.listeners.get(t) ?? []), f]);
  }
  emit(t: string, data: unknown) {
    for (const f of this.listeners.get(t) ?? []) f({ data: JSON.stringify(data) } as MessageEvent<string>);
  }
  close() {
    this.closed = true;
  }
}

function setup() {
  const qc = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return renderHook(() => useServerEvents(true), { wrapper });
}

beforeEach(() => {
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
});
