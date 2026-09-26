import { describe, expect, it, beforeEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { authStore } from "./client";
import { applyCounts, initialLive, liveStore } from "./events";
import { keys, useOpenItem } from "./queries";
import type { Bootstrap, ItemDetail } from "./types";
import { bootstrap, detail, json, mockFetch } from "@/test/mockApi";

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  qc.setQueryData<Bootstrap>(keys.bootstrap, bootstrap);
  qc.setQueryData<ItemDetail>(keys.item("1001"), detail(1));
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  return { qc, ...renderHook(() => useOpenItem(), { wrapper }) };
}
const unread = (qc: QueryClient) => {
  const b = qc.getQueryData<Bootstrap>(keys.bootstrap)!;
  return { total: b.counts.unread, feed: b.feeds[0]!.unread, folder: b.folders[0]!.unread };
};

beforeEach(() => {
  authStore.set("in");
  liveStore.set(initialLive);
});

describe("useOpenItem counts", () => {
  it("moves total, feed and folder by one on open, once", async () => {
    mockFetch({ "POST /api/items/1001/open": () => json({ session_key: "s", item: { ...detail(1), read: true } }) });
    const { qc, result } = setup();
    act(() => result.current.mutate({ id: "1001", via: "tap" }));
    expect(unread(qc)).toEqual({ total: 2, feed: 2, folder: 2 }); // optimistic, before the response
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(unread(qc)).toEqual({ total: 2, feed: 2, folder: 2 });
  });

  it("does not double-apply when the server counts event lands before the response", async () => {
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => (release = r));
    mockFetch({
      "POST /api/items/1001/open": async () => {
        await gate;
        return json({ session_key: "s", item: { ...detail(1), read: true } });
      },
    });
    const { qc, result } = setup();
    act(() => result.current.mutate({ id: "1001", via: "tap" }));
    act(() => applyCounts(qc, { unread_total: 2, feeds: { "1": 2 } }));
    release();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(unread(qc)).toEqual({ total: 2, feed: 2, folder: 2 });
  });

  it("does not bump for an already read item and rolls back on failure", async () => {
    mockFetch({ "POST /api/items/1001/open": () => json({ error: "boom" }, 500) });
    const { qc, result } = setup();
    act(() => result.current.mutate({ id: "1001", via: "tap" }));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(unread(qc)).toEqual({ total: 3, feed: 3, folder: 3 });
    expect(qc.getQueryData<ItemDetail>(keys.item("1001"))?.read).toBe(false);

    qc.setQueryData<ItemDetail>(keys.item("1001"), { ...detail(1), read: true });
    act(() => result.current.mutate({ id: "1001", via: "tap" }));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(unread(qc)).toEqual({ total: 3, feed: 3, folder: 3 });
  });
});
