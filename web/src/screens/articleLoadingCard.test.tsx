import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("article while its detail loads", () => {
  it("shows the list card's title, source and picture instead of a bare skeleton", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1, { image: "/img/lead.jpg" }), card(2)])),
      "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
      "GET /api/items/1001": () => new Promise<Response>(() => undefined), // never answers
    });
    window.history.replaceState({ idx: 0 }, "", "/i/1001?from=unread");
    render(<App client={makeQueryClient({ retry: false })} />);
    const title = await screen.findByTestId("loading-card-title");
    expect(title.textContent).toBe("Article number 1");
    expect(screen.getByText("Loading article")).toBeTruthy();
    expect(document.querySelector('img[src="/img/lead.jpg"]')).not.toBeNull();
  });
});
