import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
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
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
  resetDeviceSync();
});

describe("device settings and the service worker's stored bootstrap", () => {
  it("are not taken from a stored copy, and are from the live answer that follows", async () => {
    let cached = true;
    mockFetch({
      "GET /api/bootstrap": () => (cached ? json({ ...bootstrap, device: device(2) }, 200, { "X-Kipple-Cache": "1" }) : json({ ...bootstrap, device: device(1.25) })),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    window.history.replaceState({ idx: 0 }, "", "/l/unread");
    const qc = makeQueryClient({ retry: false });
    render(<App client={qc} />);
    await screen.findByText("Article number 1");
    expect(prefsStore.get().textSize).toBe(1); // the stored copy's 2 was not applied
    expect(syncStore.get().status).toBe("off");

    cached = false;
    await act(() => qc.refetchQueries({ queryKey: keys.bootstrap }));
    await waitFor(() => expect(prefsStore.get().textSize).toBe(1.25));
    expect(syncStore.get().status).toBe("idle");
  });
});
