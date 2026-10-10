import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { initStatsQueue, resetStatsForTests } from "@/lib/statsSender";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

function routes(settings: Record<string, unknown> = {}) {
  mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, settings }),
    "GET /api/items": () => json(pageOf([card(1), card(2)])),
    "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
    "GET /api/items/1001": () => json(detail(1)),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
  });
}
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  const client = makeQueryClient({ retry: false });
  stopStats = initStatsQueue(client); // as main.tsx does: the sender reads the setting from this client
  return render(<App client={client} />);
}
/** The events handed to sendBeacon, of one kind. */
async function beaconed(beacon: ReturnType<typeof vi.fn>, kind: string) {
  const bodies = beacon.mock.calls.map(([, b]) => JSON.parse(b as string) as { events: { kind: string; event_id?: string }[] });
  // The ids are random: checked for shape here, and left out of what the tests compare.
  return bodies
    .flatMap((b) => b.events)
    .filter((e) => e.kind === kind)
    .map(({ event_id, ...rest }) => {
      expect(event_id).toMatch(/^[0-9a-f]{32}$/);
      return rest;
    });
}

let stopStats: (() => void) | undefined;

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("min-width: 900px"), media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }));
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetStatsForTests();
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
});
afterEach(() => {
  stopStats?.();
  stopStats = undefined;
  resetStatsForTests();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("article stats events", () => {
  it("records a share when the sheet resolves, and not when it is dismissed", async () => {
    routes();
    const beacon = vi.fn().mockReturnValue(true);
    const share = vi.fn().mockRejectedValueOnce(new DOMException("dismissed", "AbortError")).mockResolvedValueOnce(undefined);
    vi.stubGlobal("navigator", { userAgent: "test", onLine: true, share, sendBeacon: beacon });
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const user = userEvent.setup();
    await user.click(within(screen.getByRole("toolbar", { name: "Article actions" })).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Share" }));
    await waitFor(() => expect(share).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(await beaconed(beacon, "share")).toEqual([]);
    await user.click(within(screen.getByRole("toolbar", { name: "Article actions" })).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Share" }));
    await waitFor(async () => expect(await beaconed(beacon, "share")).toEqual([{ kind: "share", item_id: 1001 }]));
  });

  it("records open_original from the menu", async () => {
    routes();
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { userAgent: "test", onLine: true, sendBeacon: beacon });
    vi.stubGlobal("open", vi.fn());
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const user = userEvent.setup();
    await user.click(within(screen.getByRole("toolbar", { name: "Article actions" })).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Open original" }));
    await waitFor(async () => expect(await beaconed(beacon, "open_original")).toEqual([{ kind: "open_original", item_id: 1001 }]));
  });

  it("records nothing when stats are off", async () => {
    routes({ "stats.enabled": false });
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { userAgent: "test", onLine: true, sendBeacon: beacon });
    vi.stubGlobal("open", vi.fn());
    const user = userEvent.setup();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await user.click(within(screen.getByRole("toolbar", { name: "Article actions" })).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Open original" }));
    await new Promise((r) => setTimeout(r, 20));
    expect(beacon).not.toHaveBeenCalled();
  });
});
