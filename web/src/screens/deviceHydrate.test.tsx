import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import App, { BOOT_RECHECK_MAX_MS, BOOT_RECHECK_MS, makeQueryClient } from "@/App";
import { offlineStore, setOnline } from "@/lib/offlineState";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { keys } from "@/api/queries";
import type { DeviceView } from "@/api/types";
import { resetDeviceSync, SYNC_FLAG_KEY, syncStore } from "@/lib/deviceSync";
import { resetDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

const device = (textSize: number): DeviceView => ({ id: "dev1", name: "", profile: { "client.text_size": textSize }, merged: { "client.text_size": textSize } });

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  resetDeviceSync();
  prefsStore.set({ ...DEFAULT_PREFS });
  resetDevicePrefs();
  localStorage.clear();
  localStorage.setItem(SYNC_FLAG_KEY, "1");
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
  resetDeviceSync();
});

describe("device settings and the service worker's stored bootstrap", () => {
  function start() {
    const net = { cached: true };
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => (net.cached ? json({ ...bootstrap, device: device(2) }, 200, { "X-Kipple-Cache": "1" }) : json({ ...bootstrap, device: device(1.25) })),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    window.history.replaceState({ idx: 0 }, "", "/l/unread");
    const qc = makeQueryClient({ retry: false });
    render(<App client={qc} />);
    return { net, qc, boots: () => calls.filter((c) => c.url.pathname === "/api/bootstrap").length };
  }

  it("are not taken from a stored copy, and are from the live answer the app asks for again by itself", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { net, qc } = start();
      await screen.findByText("Article number 1");
      expect(prefsStore.get().textSize).toBe(1); // the stored copy's 2 was not applied
      expect(syncStore.get().status).toBe("off");

      // A local edit of the bootstrap (a counts event) keeps the stored copy's flag: it is still not live.
      act(() => qc.setQueryData(keys.bootstrap, (old: object | undefined) => (old ? { ...old, counts: { unread: 9, starred: 0 } } : old)));
      net.cached = false;
      // No hand-made refetch: the app asks again by itself (at most BOOT_RECHECK_MAX_MS apart, however long the
      // launch took on a busy machine).
      await act(() => vi.advanceTimersByTimeAsync(BOOT_RECHECK_MAX_MS + 100));
      await waitFor(() => expect(prefsStore.get().textSize).toBe(1.25));
      expect(syncStore.get().status).toBe("idle");
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps asking while the answers still come from the copy, and asks at once when the connection is back", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { net, boots } = start();
      await screen.findByText("Article number 1");
      await act(() => vi.advanceTimersByTimeAsync(BOOT_RECHECK_MS + 100));
      await waitFor(() => expect(boots()).toBeGreaterThanOrEqual(2)); // asked again, still the copy (offline)
      expect(offlineStore.get().online).toBe(false);
      net.cached = false;
      act(() => setOnline(true)); // the connection is back (the browser's online event)
      await waitFor(() => expect(prefsStore.get().textSize).toBe(1.25));
      expect(syncStore.get().status).toBe("idle");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("a bootstrap refetch that fails mid-session", () => {
  it("keeps the reader up instead of swapping it for a full-screen error", async () => {
    let expired = false;
    const opaqueRedirect = () => ({ type: "opaqueredirect", status: 0, ok: false, headers: new Headers() }) as unknown as Response;
    mockFetch({
      "GET /api/bootstrap": () => (expired ? opaqueRedirect() : json(bootstrap)),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    window.history.replaceState({ idx: 0 }, "", "/l/unread");
    const qc = makeQueryClient({ retry: false });
    render(<App client={qc} />);
    await screen.findByText("Article number 1");
    expired = true;
    await act(() => qc.refetchQueries({ queryKey: keys.bootstrap }));
    expect(qc.getQueryState(keys.bootstrap)?.status).toBe("error"); // TanStack keeps the data alongside the error
    await act(() => new Promise((r) => setTimeout(r, 100))); // let the error state reach the screen
    expect(screen.getByText("Article number 1")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Your sign-in has expired" })).toBeNull();
    expect(screen.getByTestId("offline-notice")).toHaveTextContent("Your sign-in has expired"); // the notice explains
  });
});
