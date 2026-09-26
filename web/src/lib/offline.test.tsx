import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { ApiError, api, authStore } from "@/api/client";
import { applyRead, applyStar, keys, useOpenItem } from "@/api/queries";
import { OfflineNotice } from "@/shell/OfflineNotice";
import { card, detail, json, mockFetch } from "@/test/mockApi";
import { flushQueue, isOffline, prefetchUnread, queueRead, queueStar, resetOfflineForTests, resetPrefetchForTests, supersede, wipeOfflineData } from "./offline";
import { devicePrefsStore } from "./devicePrefs";
import * as toasts from "@/shell/toasts";
import { offlineStore, setOnline, setPending } from "./offlineState";

function fresh() {
  resetOfflineForTests();
  resetPrefetchForTests();
  authStore.set("in");
  offlineStore.set({ online: true, pending: 0, updateReady: false });
}
beforeEach(fresh);

const netFail = () => vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("offline")));

describe("the offline queue", () => {
  it("replays in order with the time each change was made, then empties", async () => {
    await queueStar("1001", true, 1_700_000_000);
    await queueRead(["1002", "1003"], true);
    expect(offlineStore.get().pending).toBe(2);

    const { calls } = mockFetch({
      "PUT /api/items/1001/star": () => json({ starred: true, restored: false }),
      "POST /api/items/mark-read": () => json({ changed: ["1002", "1003"], restored: [] }),
      "GET /api/bootstrap": () => json({}),
    });
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    await flushQueue(qc);

    expect(calls.map((c) => `${c.method} ${c.url.pathname}`)).toEqual(["PUT /api/items/1001/star", "POST /api/items/mark-read"]);
    expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ starred: true, at: 1_700_000_000 });
    expect(JSON.parse(String(calls[1]?.init?.body))).toMatchObject({ ids: ["1002", "1003"], read: true });
    expect(offlineStore.get().pending).toBe(0);
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.bootstrap });
  });

  it("a later star of the same article replaces the earlier one", async () => {
    await queueStar("1001", true, 10);
    await queueStar("1001", false, 20);
    expect(offlineStore.get().pending).toBe(1);
    const { calls } = mockFetch({ "PUT /api/items/1001/star": () => json({ starred: false, restored: false }) });
    await flushQueue();
    expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ starred: false, at: 20 });
  });

  it("stops at the first network failure and keeps everything left", async () => {
    await queueStar("1", true);
    await queueStar("2", true);
    netFail();
    await flushQueue();
    expect(offlineStore.get().pending).toBe(2);
    expect(offlineStore.get().online).toBe(false);
  });

  it("stops on 401, 429 and 5xx (retry later) but drops a change the server refuses for good, and says so", async () => {
    const toast = vi.spyOn(toasts, "toast");
    await queueStar("1", true);
    await queueStar("2", true);
    await queueStar("3", true);
    mockFetch({
      "PUT /api/items/1/star": () => json({ error: "not_found" }, 404),
      "PUT /api/items/2/star": () => json({ error: "bad_request" }, 400),
      "PUT /api/items/3/star": () => json({ error: "internal" }, 503),
    });
    await flushQueue();
    expect(offlineStore.get().pending).toBe(1); // 1 and 2 are gone for good, 3 waits
    expect(toast).toHaveBeenCalledWith("2 changes made offline couldn't be saved.", "error");
  });

  it("a 401 keeps the queue: an expired session is not a sign-out", async () => {
    await queueStar("1", true);
    await queueRead(["2"], true);
    mockFetch({ "PUT /api/items/1/star": () => json({ error: "auth" }, 401) });
    await flushQueue();
    expect(offlineStore.get().pending).toBe(2);
  });

  it("does nothing while signed out; an explicit sign-out wipes the queue", async () => {
    await queueStar("1", true);
    authStore.set("out");
    const { calls } = mockFetch({});
    await flushQueue();
    expect(calls).toHaveLength(0);
    expect(offlineStore.get().pending).toBe(1);
    await wipeOfflineData();
    expect(offlineStore.get().pending).toBe(0);
  });

  it("an online change to the same article settles the queued one instead of being overwritten later", async () => {
    await queueStar("1", true);
    await queueRead(["2", "3"], true);
    await supersede({ star: "1" });
    await supersede({ read: ["3"] });
    expect(offlineStore.get().pending).toBe(1);
    const { calls } = mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["2"], restored: [] }) });
    await flushQueue();
    expect(JSON.parse(String(calls[0]?.init?.body)).ids).toEqual(["2"]);
  });

  it("concurrent flushes share one run", async () => {
    await queueStar("1", true);
    const { calls } = mockFetch({ "PUT /api/items/1/star": () => json({ starred: true, restored: false }) });
    await Promise.all([flushQueue(), flushQueue()]);
    expect(calls).toHaveLength(1);
  });
});

describe("changes made offline", () => {
  const qc = () => {
    const c = new QueryClient();
    c.setQueryData(keys.item("1001"), detail(1));
    return c;
  };

  it("a star keeps its optimistic state and is queued instead of reverted", async () => {
    netFail();
    const c = qc();
    const ok = await applyStar(c, "1001", true);
    expect(ok).toBe(true);
    expect(c.getQueryData<{ starred: boolean }>(keys.item("1001"))?.starred).toBe(true);
    expect(offlineStore.get().pending).toBe(1);
  });

  it("a read answers like the server would, and other failures still revert", async () => {
    netFail();
    const c = qc();
    await expect(applyRead(c, ["1001"], true, "key")).resolves.toEqual({ changed: ["1001"], restored: [] });
    expect(offlineStore.get().pending).toBe(1);

    mockFetch({ "POST /api/items/mark-read": () => json({ error: "bad_request" }, 400) });
    const c2 = qc();
    await expect(applyRead(c2, ["1001"], true, "key")).resolves.toBeUndefined();
    expect(c2.getQueryData<{ read: boolean }>(keys.item("1001"))?.read).toBe(false);
    expect(offlineStore.get().pending).toBe(0); // the online attempt settled the queued read for that article
  });

  it("opening a held article offline marks it read and queues the read", async () => {
    netFail();
    const c = qc();
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={c}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useOpenItem(), { wrapper });
    result.current.mutate({ id: "1001", via: "tap" });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(offlineStore.get().pending).toBe(1);
    expect(c.getQueryData<{ read: boolean }>(keys.item("1001"))?.read).toBe(true);
  });

  it("isOffline is only the request that never arrived", () => {
    expect(isOffline(new ApiError(0, "network"))).toBe(true);
    expect(isOffline(new ApiError(503, "x"))).toBe(false);
    expect(isOffline(new Error("x"))).toBe(false);
  });
});

describe("the handshake and the connection state", () => {
  it("a server on a newer API asks for a reload; the same or an older one does not", async () => {
    mockFetch({
      "GET /api/a": () => json({}, 200, { "X-Kipple-API": "1" }),
      "GET /api/older": () => json({}, 200, { "X-Kipple-API": "0" }),
      "GET /api/b": () => json({}, 200, { "X-Kipple-API": "2" }),
    });
    await api("/api/older");
    expect(offlineStore.get().updateReady).toBe(false);
    await api("/api/a");
    expect(offlineStore.get().updateReady).toBe(false);
    await api("/api/b");
    expect(offlineStore.get().updateReady).toBe(true);
  });

  it("a slow answer the worker replaced with its copy does not make a background request call the app offline", async () => {
    mockFetch({ "GET /api/a": () => json({}, 200, { "X-Kipple-Cache": "1" }) });
    await api("/api/a", { quiet: true });
    expect(offlineStore.get().online).toBe(true);
  });

  it("an answer the service worker served from its copy means offline; a live one means online again", async () => {
    mockFetch({ "GET /api/a": () => json({}, 200, { "X-Kipple-Cache": "1" }), "GET /api/b": () => json({}) });
    await api("/api/a");
    expect(offlineStore.get().online).toBe(false);
    await api("/api/b");
    expect(offlineStore.get().online).toBe(true);
  });
});

describe("prefetch for offline reading", () => {
  it("follows the device's sort order, so the worker files the page where the list asks for it", async () => {
    const prev = devicePrefsStore.get();
    devicePrefsStore.set({ ...prev, order: "oldest" });
    try {
      const { calls } = mockFetch({ "GET /api/items": () => json({ items: [], next_cursor: null }) });
      await prefetchUnread(9_000_000);
      expect(calls[0]?.url.search).toBe("?view=unread&order=oldest&limit=50&include=content");
    } finally {
      devicePrefsStore.set(prev);
    }
  });

  it("asks for the first Unread page with content, at most every 15 minutes", async () => {
    const { calls } = mockFetch({ "GET /api/items": () => json({ items: [card(1)], next_cursor: null }) });
    await prefetchUnread(1_000_000);
    await prefetchUnread(1_000_000 + 60_000);
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url.search).toBe("?view=unread&limit=50&include=content");
    await prefetchUnread(1_000_000 + 16 * 60_000);
    expect(calls).toHaveLength(2);
  });

  it("does not run offline, and retries after a failure", async () => {
    setOnline(false);
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    const { calls } = mockFetch({});
    await prefetchUnread(5_000_000);
    expect(calls).toHaveLength(0);
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(true);
    netFail();
    await prefetchUnread(5_000_000);
    const again = mockFetch({ "GET /api/items": () => json({ items: [], next_cursor: null }) });
    await prefetchUnread(5_000_001);
    expect(again.calls).toHaveLength(1);
  });
});

describe("<OfflineNotice />", () => {
  it("says nothing while online with nothing waiting", () => {
    const { container } = render(<OfflineNotice />);
    expect(container).toBeEmptyDOMElement();
  });

  it("explains offline, with and without waiting changes, and while sending", () => {
    setOnline(false);
    const { rerender } = render(<OfflineNotice />);
    expect(screen.getByRole("status")).toHaveTextContent("You're offline. Reading what's on this device.");
    setPending(2);
    rerender(<OfflineNotice />);
    expect(screen.getByRole("status")).toHaveTextContent("2 changes will be sent when you're back");
    setOnline(true);
    rerender(<OfflineNotice />);
    expect(screen.getByRole("status")).toHaveTextContent("Sending 2 changes");
  });

  it("offers a reload when a newer version is ready", () => {
    offlineStore.set((s) => ({ ...s, updateReady: true }));
    render(<OfflineNotice />);
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});
