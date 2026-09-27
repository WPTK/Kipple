import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useBootstrap } from "@/api/queries";
import { keys } from "@/api/queryKeys";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { useStatsEnabled } from "./statsSender";

afterEach(() => {
  document.body.innerHTML = "";
});

function Probe() {
  const on = useStatsEnabled();
  const boot = useBootstrap();
  return (
    <div>
      <span data-testid="on">{String(on)}</span>
      <span data-testid="unread">{boot.data?.counts.unread ?? "none"}</span>
    </div>
  );
}

function mount(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  render(
    <QueryClientProvider client={client}>
      <Probe />
    </QueryClientProvider>,
  );
  return client;
}

describe("useStatsEnabled", () => {
  it("leaves the bootstrap query's fetch function alone: a later refetch still fetches and updates", async () => {
    let unread = 3;
    const { calls } = mockFetch({ "GET /api/bootstrap": () => json({ ...bootstrap, counts: { ...bootstrap.counts, unread } }) });
    const qc = mount();
    await waitFor(() => expect(screen.getByTestId("unread")).toHaveTextContent("3"));
    const boots = () => calls.filter((c) => c.url.pathname === "/api/bootstrap").length;
    expect(boots()).toBe(1);
    unread = 9;
    await act(async () => {
      await qc.invalidateQueries({ queryKey: keys.bootstrap });
    });
    await waitFor(() => expect(screen.getByTestId("unread")).toHaveTextContent("9"));
    expect(boots()).toBe(2);
    unread = 12;
    await act(async () => {
      await qc.refetchQueries({ queryKey: keys.bootstrap });
    });
    await waitFor(() => expect(screen.getByTestId("unread")).toHaveTextContent("12"));
    expect(boots()).toBe(3);
    expect(qc.getQueryCache().find({ queryKey: keys.bootstrap })?.observers.length).toBe(1); // ours added no observer
  });

  it("reads the setting from the cache and follows changes", async () => {
    mockFetch({});
    const qc = new QueryClient();
    mount(qc);
    // Bootstrap absent (the probe's own fetch is unmocked and fails): off.
    expect(screen.getByTestId("on")).toHaveTextContent("false");
    const set = async (settings: Record<string, unknown>) =>
      act(async () => {
        qc.setQueryData(keys.bootstrap, { ...bootstrap, settings });
      });
    await set({ "stats.enabled": false });
    await waitFor(() => expect(screen.getByTestId("on")).toHaveTextContent("false"));
    await set({ "stats.enabled": true });
    await waitFor(() => expect(screen.getByTestId("on")).toHaveTextContent("true"));
    await set({ "stats.enabled": false });
    await waitFor(() => expect(screen.getByTestId("on")).toHaveTextContent("false"));
    await set({}); // setting absent counts as on
    await waitFor(() => expect(screen.getByTestId("on")).toHaveTextContent("true"));
  });

  it("subscribes once per client and wakes only for the bootstrap query", async () => {
    const qc = new QueryClient();
    qc.setQueryData(keys.bootstrap, { ...bootstrap, settings: {} });
    const subscribe = vi.spyOn(qc.getQueryCache(), "subscribe");
    let renders = 0;
    const W = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result, rerender } = renderHook(
      () => {
        renders++;
        return useStatsEnabled();
      },
      { wrapper: W },
    );
    rerender();
    rerender();
    expect(subscribe).toHaveBeenCalledTimes(1); // not once per render
    const before = renders;
    // A wake-up reads the setting again: count those reads while only other queries change.
    const reads = vi.spyOn(qc, "getQueryData");
    const bootReads = () => reads.mock.calls.filter(([k]) => JSON.stringify(k) === JSON.stringify(keys.bootstrap)).length;
    await act(async () => {
      for (let i = 0; i < 5; i++) qc.setQueryData(["items", i], { n: i }); // other queries: no wake-up
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(bootReads()).toBe(0);
    expect(renders).toBe(before);
    expect(result.current).toBe(true);
    await act(async () => {
      qc.setQueryData(keys.bootstrap, { ...bootstrap, settings: { "stats.enabled": false } });
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(result.current).toBe(false);
  });
});
