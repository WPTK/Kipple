import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import { onlineManager, QueryClientProvider, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { QueryClient } from "@tanstack/react-query";
import { useRefreshAll } from "@/api/refresh";
import { ApiError, api, authStore } from "@/api/client";
import { applyRead, applyStar, flattenItems, keys, useBootstrap, useItem, useItems, useOpenItem, useToggleStar } from "@/api/queries";
import type { Bootstrap } from "@/api/types";
import { OfflineNotice } from "@/shell/OfflineNotice";
import App, { makeQueryClient } from "@/App";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import {
  FLUSH_REQUEST_MS,
  flushQueue,
  initOffline,
  isOffline,
  memoryBackendForTests,
  SUPERSEDE_WAIT_MS,
  prefetchUnread,
  QueueWriteError,
  queueRead,
  queueStar,
  resetOfflineForTests,
  resetPrefetchForTests,
  setOfflineBackendForTests,
  supersede,
  watchForUpdates,
  wipeOfflineData,
} from "./offline";
import { devicePrefsStore } from "./devicePrefs";
import * as toasts from "@/shell/toasts";
import { offlineStore, setOnline, setPending } from "./offlineState";

function fresh() {
  resetOfflineForTests();
  resetPrefetchForTests();
  authStore.set("in");
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
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

  it("an online change made while a flush runs is not overwritten by the flush's older copy of the queue", async () => {
    await queueRead(["9", "3"], true);
    await queueStar("1", true);
    await queueRead(["2", "3"], true);
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    const { calls } = mockFetch({
      "POST /api/items/mark-read": async (_u, init) => {
        const ids = JSON.parse(String(init?.body)).ids as string[];
        if (ids.includes("9")) await held;
        return json({ changed: ids, restored: [] });
      },
      "PUT /api/items/1/star": () => json({ starred: true, restored: false }),
    });
    const flush = flushQueue();
    await waitFor(() => expect(calls).toHaveLength(1)); // the first row (9 and 3) is on its way
    // Online writes to article 1 and article 3 happen now, while the flush waits on the first request.
    let starDone = false;
    let readDone = false;
    const star = supersede({ star: "1" }).then(() => (starDone = true));
    const read = supersede({ read: ["3"] }).then(() => (readDone = true));
    await new Promise((r) => setTimeout(r, 20));
    // Article 3 is in the request already in flight: the online write must land after it, so supersede waits.
    expect(readDone).toBe(false);
    // Article 1 is not: its online write has nothing to wait for.
    expect(starDone).toBe(true);
    release();
    await Promise.all([flush, star, read]);
    expect(calls.map((c) => `${c.method} ${c.url.pathname} ${JSON.parse(String(c.init?.body)).ids ?? ""}`)).toEqual([
      "POST /api/items/mark-read 9,3",
      "POST /api/items/mark-read 2",
    ]);
    expect(offlineStore.get().pending).toBe(0);
  });

  it("a stalled flush request holds an online change to the same article only briefly, and is given up later", async () => {
    vi.useFakeTimers();
    try {
      await queueRead(["3"], true);
      const { calls } = mockFetch({
        // Never answers until its signal gives up, like a request lost in a network switch.
        "POST /api/items/mark-read": (_u, init) =>
          new Promise<Response>((_res, rej) => init?.signal?.addEventListener("abort", () => rej(init.signal?.reason))),
      });
      const flush = flushQueue();
      await vi.waitFor(() => expect(calls).toHaveLength(1));
      let done = false;
      const online = supersede({ read: ["3"] }).then(() => (done = true));
      await vi.advanceTimersByTimeAsync(SUPERSEDE_WAIT_MS - 100);
      expect(done).toBe(false);
      await vi.advanceTimersByTimeAsync(200);
      expect(done).toBe(true); // the online write goes ahead; before, it waited as long as the stalled request
      await online;
      await vi.advanceTimersByTimeAsync(FLUSH_REQUEST_MS);
      await flush; // the stalled run ends, so later flushes are not stuck behind it
      expect(calls[0]?.init?.signal).toBeInstanceOf(AbortSignal);
    } finally {
      vi.useRealTimers();
    }
  });

  it("supersede never brings back a row the flush sent and removed meanwhile", async () => {
    // A store whose listing is taken just before a flush removes the row: supersede works from a stale copy.
    const inner = memoryBackendForTests();
    await inner.put({ kind: "read", ids: ["2", "3"], read: true, seq: 1 });
    let raced = false;
    const racing = {
      ...inner,
      all: async () => {
        const rows = await inner.all();
        if (!raced) {
          raced = true;
          await inner.del(1); // the flush sent it and removed it just after this listing
        }
        return rows;
      },
    };
    setOfflineBackendForTests(racing);
    setPending(1);
    await supersede({ read: ["3"] });
    expect(await inner.all()).toEqual([]); // before: the narrowed copy { ids: ["2"] } was written back and sent again
  });

  it("a change queued while a flush runs is sent in the same run", async () => {
    await queueRead(["1"], true);
    let queuedLate = false;
    const { calls } = mockFetch({
      "POST /api/items/mark-read": async (_u, init) => {
        if (!queuedLate) {
          queuedLate = true;
          await queueRead(["2"], false);
        }
        return json({ changed: JSON.parse(String(init?.body)).ids, restored: [] });
      },
    });
    await flushQueue();
    expect(calls.map((c) => JSON.parse(String(c.init?.body)).ids)).toEqual([["1"], ["2"]]);
    expect(offlineStore.get().pending).toBe(0);
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

  it("a change that could not be stored on the device is reverted and said, not reported as queued", async () => {
    const toast = vi.spyOn(toasts, "toast");
    const broken = {
      all: async () => [],
      put: async () => {
        throw new DOMException("quota", "QuotaExceededError");
      },
      del: async () => {},
      update: async () => {},
      clear: async () => {},
    };
    setOfflineBackendForTests(broken);
    netFail();
    const c = qc();
    await expect(queueRead(["1"], true)).rejects.toBeInstanceOf(QueueWriteError);
    await expect(queueStar("1", true)).rejects.toBeInstanceOf(QueueWriteError);

    await expect(applyRead(c, ["1001"], true, "key")).resolves.toBeUndefined();
    expect(c.getQueryData<{ read: boolean }>(keys.item("1001"))?.read).toBe(false);
    await expect(applyStar(c, "1001", true)).resolves.toBe(false);
    expect(c.getQueryData<{ starred: boolean }>(keys.item("1001"))?.starred).toBe(false);
    expect(toast).toHaveBeenCalledWith("Kipple couldn't save that change on this device.", "error");

    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={c}>{children}</QueryClientProvider>;
    const star = renderHook(() => useToggleStar(), { wrapper });
    star.result.current.mutate({ id: "1001", starred: true });
    await waitFor(() => expect(star.result.current.isError).toBe(true));
    expect(c.getQueryData<{ starred: boolean }>(keys.item("1001"))?.starred).toBe(false);

    const open = renderHook(() => useOpenItem(), { wrapper });
    open.result.current.mutate({ id: "1001", via: "tap" });
    await waitFor(() => expect(open.result.current.isError).toBe(true));
    expect(c.getQueryData<{ read: boolean }>(keys.item("1001"))?.read).toBe(false);
    expect(toast).toHaveBeenCalledTimes(4);
    expect(offlineStore.get().pending).toBe(0);
  });

  it("isOffline is only the request that never arrived", () => {
    expect(isOffline(new ApiError(0, "network"))).toBe(true);
    expect(isOffline(new ApiError(503, "x"))).toBe(false);
    expect(isOffline(new Error("x"))).toBe(false);
  });
});

describe("an expired access-proxy session", () => {
  /** What fetch gives for `redirect: "manual"` when the proxy sends the request to its login page. */
  const opaqueRedirect = () => ({ type: "opaqueredirect", status: 0, ok: false, headers: new Headers() }) as unknown as Response;

  it("asks for no redirects to be followed, and a redirect is a session problem, not offline", async () => {
    const { calls } = mockFetch({ "GET /api/a": opaqueRedirect });
    const err = await api("/api/a").catch((e: unknown) => e);
    expect(calls[0]?.init?.redirect).toBe("manual");
    expect(err).toBeInstanceOf(ApiError);
    expect(isOffline(err)).toBe(false);
    expect(offlineStore.get()).toMatchObject({ online: true, sessionExpired: true });
    expect(authStore.get()).toBe("in"); // Kipple's own sign-in screen cannot help; a reload can
  });

  it("a change made meanwhile is reverted and explained, not queued; the queue waits", async () => {
    const toast = vi.spyOn(toasts, "toast");
    await queueStar("7", true);
    mockFetch({ "POST /api/items/mark-read": opaqueRedirect, "PUT /api/items/7/star": opaqueRedirect });
    const c = new QueryClient();
    c.setQueryData(keys.item("1001"), detail(1));
    await expect(applyRead(c, ["1001"], true, "key")).resolves.toBeUndefined();
    expect(c.getQueryData<{ read: boolean }>(keys.item("1001"))?.read).toBe(false);
    expect(toast).toHaveBeenCalledWith("Your sign-in has expired. Reload Kipple to sign in again.", "error");
    await flushQueue();
    expect(offlineStore.get().pending).toBe(1);
  });

  it("a real network failure is still offline, and a live answer clears the expired state", async () => {
    offlineStore.set((s) => ({ ...s, sessionExpired: true }));
    netFail();
    const err = await api("/api/a").catch((e: unknown) => e);
    expect(isOffline(err)).toBe(true);
    expect(offlineStore.get().sessionExpired).toBe(true);
    mockFetch({ "GET /api/a": () => json({}, 200, { "X-Kipple-Cache": "1" }), "GET /api/b": () => json({}) });
    await api("/api/a");
    expect(offlineStore.get().sessionExpired).toBe(true); // the worker's copy proves nothing
    await api("/api/b");
    expect(offlineStore.get().sessionExpired).toBe(false);
  });

  it("at launch, says the sign-in expired instead of offline or a server error", async () => {
    mockFetch({ "GET /api/bootstrap": opaqueRedirect });
    render(<App client={makeQueryClient({ retry: false })} />);
    expect(await screen.findByRole("heading", { name: "Your sign-in has expired" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    expect(screen.queryByText(/offline/i)).toBeNull();
  });

  it("the notice offers a reload", () => {
    offlineStore.set((s) => ({ ...s, sessionExpired: true }));
    render(<OfflineNotice />);
    expect(screen.getByText(/Your sign-in has expired/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
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
  it("says nothing while online with nothing waiting, but its live region is already there", () => {
    render(<OfflineNotice />);
    const region = screen.getByRole("status");
    expect(region).toHaveAttribute("aria-live", "polite");
    expect(region).toBeEmptyDOMElement();
    expect(screen.getByTestId("offline-notice")).toHaveClass("sr-only-live");
    expect(screen.getByTestId("offline-notice")).not.toHaveClass("pt-safe");
  });

  it("puts the text into the region that was mounted before it, and keeps the button out of it", () => {
    const { rerender } = render(<OfflineNotice />);
    const region = screen.getByRole("status");
    act(() => setOnline(false));
    rerender(<OfflineNotice />);
    expect(screen.getByRole("status")).toBe(region); // the same node: an announcement, not a new region
    expect(region).toHaveTextContent("You're offline.");
    expect(screen.getByTestId("offline-notice")).toHaveClass("pt-safe");
    act(() => offlineStore.set((s) => ({ ...s, updateReady: true })));
    expect(screen.getByRole("status")).toBe(region);
    expect(region).toHaveTextContent("A newer version of Kipple is ready.");
    expect(within(region).queryByRole("button")).toBeNull();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
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

describe("new builds", () => {
  function fakeContainer(controller: object | null) {
    const listeners = new Set<() => void>();
    const sw = {
      controller,
      addEventListener: (_t: string, f: () => void) => listeners.add(f),
      removeEventListener: (_t: string, f: () => void) => listeners.delete(f),
      change(next: object | null) {
        sw.controller = next;
        listeners.forEach((f) => f());
      },
    };
    return sw;
  }

  it("a page that was controlled offers a reload when the controller changes", () => {
    const sw = fakeContainer({});
    watchForUpdates(sw as unknown as ServiceWorkerContainer);
    sw.change({});
    expect(offlineStore.get().updateReady).toBe(true);
  });

  it("a tab opened before any worker: the first controller is the install, the next change is an update", () => {
    const sw = fakeContainer(null);
    const stop = watchForUpdates(sw as unknown as ServiceWorkerContainer);
    sw.change({}); // the first worker claims the page
    expect(offlineStore.get().updateReady).toBe(false);
    sw.change({}); // a new build takes over later
    expect(offlineStore.get().updateReady).toBe(true);
    stop();
  });
});

// #108: under TanStack Query's default network mode ("online"), every query and mutation started after the browser's
// `offline` event sat paused before sending anything, so the service worker never got to answer from its copies and
// the offline queue never ran.
describe("the query client offline", () => {
  const offline = () => {
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    onlineManager.setOnline(false);
  };
  afterEach(() => {
    onlineManager.setOnline(true);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
  const wrapperFor = (c: QueryClient) =>
    function Wrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={c}>{children}</QueryClientProvider>;
    };
  /** One plain read through the app's client: the article detail, which the worker keeps. */
  const useArticle = () => useQuery({ queryKey: ["probe-108"], queryFn: ({ signal }) => api<{ title: string }>("/api/items/1001", { signal }) });

  it("reads and writes always try the network, and a failed screen loads again on reconnect", () => {
    const d = makeQueryClient().getDefaultOptions();
    expect(d.queries?.networkMode).toBe("always");
    expect(d.queries?.refetchOnReconnect).toBe(true);
    expect(d.queries?.refetchOnWindowFocus).toBe(false);
    expect(d.mutations?.networkMode).toBe("always");
  });

  it("a network failure is retried online, not while the browser says it is offline", () => {
    const retry = makeQueryClient().getDefaultOptions().queries?.retry as (n: number, e: unknown) => boolean;
    const net = new ApiError(0, "network");
    expect(retry(0, net)).toBe(true);
    expect(retry(1, new ApiError(503, "x"))).toBe(true);
    expect(retry(2, net)).toBe(false);
    expect(retry(0, new ApiError(401, "auth"))).toBe(false);
    expect(retry(0, new ApiError(404, "not_found"))).toBe(false);
    offline();
    expect(retry(0, net)).toBe(false);
    expect(retry(0, new ApiError(503, "x"))).toBe(true); // an answer from a struggling server is still worth another try
    const never = makeQueryClient({ retry: false }).getDefaultOptions().queries?.retry as typeof retry;
    expect(never(0, new ApiError(503, "x"))).toBe(false);
  });

  it("a read offline asks the service worker: its copy is shown, not a paused query", async () => {
    offline();
    const res = new Response(JSON.stringify(detail(1)), { headers: { "Content-Type": "application/json", "X-Kipple-Cache": "1" } });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(res));
    const { result } = renderHook(useArticle, { wrapper: wrapperFor(makeQueryClient()) });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.title).toBe(detail(1).title);
  });

  it("a read nothing on the device can answer fails at once, without retries, and loads when the network is back", async () => {
    offline();
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useArticle, { wrapper: wrapperFor(makeQueryClient()) });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.fetchStatus).toBe("idle");
    expect(fetchMock).toHaveBeenCalledTimes(1);

    fetchMock.mockResolvedValue(json(detail(1)));
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(true);
    act(() => onlineManager.setOnline(true));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("opening and starring offline are queued, not paused until the network returns", async () => {
    offline();
    netFail();
    const c = makeQueryClient();
    c.setQueryData(keys.item("1001"), detail(1));
    const star = renderHook(() => useToggleStar(), { wrapper: wrapperFor(c) });
    star.result.current.mutate({ id: "1001", starred: true });
    await waitFor(() => expect(star.result.current.isSuccess).toBe(true));
    const open = renderHook(() => useOpenItem(), { wrapper: wrapperFor(c) });
    open.result.current.mutate({ id: "1001", via: "tap" });
    await waitFor(() => expect(open.result.current.isSuccess).toBe(true));
    expect(offlineStore.get().pending).toBe(2);
  });

  it("a write that is not queued fails with its usual error offline", async () => {
    offline();
    netFail();
    const toast = vi.spyOn(toasts, "toast");
    const refresh = renderHook(() => useRefreshAll(), { wrapper: wrapperFor(makeQueryClient()) });
    refresh.result.current.mutate();
    await waitFor(() => expect(refresh.result.current.isError).toBe(true));
    expect(toast).toHaveBeenCalledWith("Kipple couldn't reach the server.", "error");
  });

  it("the online state starts from the browser's, so a launch offline still sees the network come back", () => {
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    const stop = initOffline(new QueryClient());
    try {
      expect(onlineManager.isOnline()).toBe(false);
    } finally {
      stop();
    }
  });
});

// Read an article offline, close the app, reopen it offline: the service worker's stored copy still says unread, and
// the queue (what the user did since) was never laid over it.
describe("the queue laid over the worker's stored copy", () => {
  afterEach(() => {
    onlineManager.setOnline(true);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
  const wrap = (c: QueryClient) =>
    function Wrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={c}>{children}</QueryClientProvider>;
    };
  const cached = { "X-Kipple-Cache": "1" };
  const unread = { view: "unread" } as const;
  const goOffline = () => {
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    onlineManager.setOnline(false);
  };

  it("a list served from the stored copy shows what was queued since", async () => {
    await queueRead(["1001"], true);
    await queueStar("1002", true);
    goOffline();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(pageOf([card(1), card(2), card(3)]), 200, cached)));
    const { result } = renderHook(() => useItems(unread), { wrapper: wrap(makeQueryClient()) });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(flattenItems(result.current.data).map((r) => [r.id, r.read, r.starred])).toEqual([
      ["1001", true, false],
      ["1002", false, true],
      ["1003", false, false],
    ]);
  });

  it("an article served from the stored copy shows what was queued since", async () => {
    await queueRead(["1001"], true);
    goOffline();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(detail(1), 200, cached)));
    const { result } = renderHook(() => useItem("1001"), { wrapper: wrap(makeQueryClient()) });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.read).toBe(true);
  });

  it("a live answer is left alone", async () => {
    await queueRead(["1001"], true);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(pageOf([card(1)]))));
    const { result } = renderHook(() => useItems(unread), { wrapper: wrap(makeQueryClient()) });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(flattenItems(result.current.data)[0]?.read).toBe(false);
  });

  it("a sent queue patches the lists it changed", async () => {
    await queueRead(["1001"], true);
    mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }), "GET /api/bootstrap": () => json({}) });
    const qc = new QueryClient();
    qc.setQueryData(keys.items(unread), { pages: [pageOf([card(1)])], pageParams: [""] });
    await flushQueue(qc);
    expect(flattenItems(qc.getQueryData(keys.items(unread)))[0]?.read).toBe(true);
  });
});

describe("the badges while offline", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
  const unread = { view: "unread" } as const;
  /** A client holding the bootstrap and an Unread list of articles 1001 to 1003. */
  function client(items = [card(1), card(2), card(3)]) {
    const c = new QueryClient();
    c.setQueryData(keys.bootstrap, bootstrap);
    c.setQueryData(keys.items(unread), { pages: [pageOf(items)], pageParams: [""] });
    return c;
  }
  const counts = (b: Bootstrap | undefined) => [b?.counts.unread, b?.feeds[0]?.unread, b?.folders[0]?.unread];

  it("a mark queued offline (swipe, key, scroll, bulk) moves them at once, and only for articles it changed", async () => {
    const c = client([card(1), card(2), card(3, { read: true })]);
    netFail();
    await applyRead(c, ["1001", "1002", "1003"], true, "swipe");
    expect(counts(c.getQueryData(keys.bootstrap))).toEqual([1, 1, 1]);
    await applyRead(c, ["1001"], false, "scroll");
    expect(counts(c.getQueryData(keys.bootstrap))).toEqual([2, 2, 2]);
    expect(offlineStore.get().pending).toBe(2);
  });

  it("opening an article offline moves them too", async () => {
    const c = client();
    c.setQueryData(keys.item("1001"), detail(1));
    netFail();
    const { result } = renderHook(() => useOpenItem(), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={c}>{children}</QueryClientProvider> });
    result.current.mutate({ id: "1001", via: "tap" });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(counts(c.getQueryData(keys.bootstrap))).toEqual([2, 2, 2]);
  });

  it("a mark sent online leaves them to the server's counts event", async () => {
    const c = client();
    mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }) });
    await applyRead(c, ["1001"], true, "key");
    expect(counts(c.getQueryData(keys.bootstrap))).toEqual([3, 3, 3]);
  });

  describe("after the app is closed and opened again before the queue is sent", () => {
    afterEach(() => onlineManager.setOnline(true));
    // Two folders, Tech inside News, a feed in each; 5 unread, 1 starred.
    const stored: Bootstrap = {
      ...bootstrap,
      server_time: 1_700_000_000,
      folders: [
        { id: "1", name: "News", position: 0, is_default: true, unread: 5 },
        { id: "2", parent_id: "1", name: "Tech", position: 1, is_default: false, unread: 2 },
      ],
      feeds: [
        { ...bootstrap.feeds[0]!, id: "1", folder_id: "1", unread: 3 },
        { ...bootstrap.feeds[0]!, id: "2", folder_id: "2", unread: 2 },
      ],
      counts: { unread: 5, starred: 1 },
    };
    /** total, feed 1, feed 2, News, Tech, starred */
    const all = (b: Bootstrap | undefined) => [b?.counts.unread, ...(b?.feeds.map((f) => f.unread) ?? []), ...(b?.folders.map((f) => f.unread) ?? []), b?.counts.starred];
    function session(over: Partial<Bootstrap> = {}) {
      const c = new QueryClient();
      c.setQueryData(keys.bootstrap, { ...stored, ...over });
      c.setQueryData(keys.items(unread), { pages: [pageOf([card(1), card(2), card(3), card(4, { feed_id: "2" }), card(5, { feed_id: "2" })])], pageParams: [""] });
      return c;
    }
    /** A new launch: the bootstrap the service worker answers with (its stored copy unless `live`). */
    async function relaunch(answer: Bootstrap, live = false) {
      vi.unstubAllGlobals();
      vi.restoreAllMocks();
      onlineManager.setOnline(live);
      if (!live) {
        vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
      }
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(answer, 200, live ? {} : { "X-Kipple-Cache": "1" })));
      const c = makeQueryClient();
      const { result } = renderHook(() => useBootstrap(), { wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={c}>{children}</QueryClientProvider> });
      await waitFor(() => expect(result.current.isSuccess).toBe(true));
      return result.current.data;
    }

    it("the badges count the queued marks across feeds, folders and the total", async () => {
      const c = session();
      netFail();
      await applyRead(c, ["1001", "1002"], true, "swipe");
      await applyRead(c, ["1004"], true, "key");
      await applyStar(c, "1003", true);
      expect(all(await relaunch(stored))).toEqual([2, 1, 1, 2, 1, 1]);
    });

    it("a mark undone offline leaves them where the stored copy had them", async () => {
      const c = session();
      netFail();
      await applyRead(c, ["1001", "1004"], true, "swipe");
      await applyRead(c, ["1001", "1004"], false, "key");
      expect(all(await relaunch(stored))).toEqual([5, 3, 2, 5, 2, 1]);
    });

    it("a read made online before going offline is not counted twice", async () => {
      // The server's counts event after reading 1001 online; the stored copy still counts it.
      const c = session({
        counts: { unread: 4, starred: 1 },
        feeds: [{ ...stored.feeds[0]!, unread: 2 }, stored.feeds[1]!],
        folders: [{ ...stored.folders[0]!, unread: 4 }, stored.folders[1]!],
      });
      c.setQueryData(keys.items(unread), { pages: [pageOf([card(1, { read: true }), card(2), card(4, { feed_id: "2" })])], pageParams: [""] });
      netFail();
      await applyRead(c, ["1001", "1002"], true, "bulk");
      expect(all(await relaunch(stored))).toEqual([3, 1, 2, 3, 2, 1]);
    });

    it("once the queue is sent the server's counts take over, and nothing counts twice", async () => {
      const c = session();
      netFail();
      await applyRead(c, ["1001"], true, "swipe");
      // Sent, but the bootstrap could not be fetched again before the app closed: the stored copy predates the
      // send, and the held counts (which already include it) still apply, once.
      mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }) });
      await flushQueue();
      expect(offlineStore.get().pending).toBe(0);
      expect(all(await relaunch(stored))).toEqual([4, 2, 2, 4, 2, 1]);
      // The server's own answer (which counts the read) replaces them, and the copy it leaves is not changed again.
      const after: Bootstrap = { ...stored, server_time: stored.server_time + 60, counts: { unread: 4, starred: 1 }, feeds: [{ ...stored.feeds[0]!, unread: 2 }, stored.feeds[1]!], folders: [{ ...stored.folders[0]!, unread: 4 }, stored.folders[1]!] };
      expect(all(await relaunch(after, true))).toEqual([4, 2, 2, 4, 2, 1]);
      expect(all(await relaunch(after))).toEqual([4, 2, 2, 4, 2, 1]);
      expect(all(await relaunch(stored))).toEqual([5, 3, 2, 5, 2, 1]);
    });

    it("are only laid over the copy they were worked out from, and go with a sign-out", async () => {
      const c = session();
      netFail();
      await applyRead(c, ["1001"], true, "swipe");
      expect(all(await relaunch({ ...stored, server_time: stored.server_time - 60 }))).toEqual([5, 3, 2, 5, 2, 1]);
      expect(all(await relaunch(stored))).toEqual([4, 2, 2, 4, 2, 1]);
      await wipeOfflineData();
      expect(all(await relaunch(stored))).toEqual([5, 3, 2, 5, 2, 1]);
    });
  });

  it("a mark that could not be queued leaves them alone", async () => {
    const c = client();
    netFail();
    setOfflineBackendForTests({ ...memoryBackendForTests(), put: async () => Promise.reject(new Error("quota")) });
    vi.spyOn(toasts, "toast").mockImplementation(() => 0);
    await applyRead(c, ["1001"], true, "swipe");
    expect(counts(c.getQueryData(keys.bootstrap))).toEqual([3, 3, 3]);
  });
});
