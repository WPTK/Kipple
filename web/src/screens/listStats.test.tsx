import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { offlineStore } from "@/lib/offlineState";
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
    "GET /api/items/1001": () => json(detail(1)),
    "GET /api/items/1002": () => json(detail(2)),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    "GET /api/saved-searches": () => json({ saved_searches: [] }),
    "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
  });
}
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  const client = makeQueryClient({ retry: false });
  stopStats = initStatsQueue(client); // as main.tsx does: the sender reads the setting from this client
  return render(<App client={client} />);
}
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
const settle = () => new Promise((r) => setTimeout(r, 30));

/** The list, ready, with stats on or off. `nav` adds to the stubbed navigator. */
async function ready(settings: Record<string, unknown>, nav: Record<string, unknown> = {}) {
  routes(settings);
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
  const beacon = vi.fn().mockReturnValue(true);
  const open = vi.fn();
  vi.stubGlobal("navigator", { userAgent: "test", onLine: true, sendBeacon: beacon, ...nav });
  vi.stubGlobal("open", open);
  go("/l/unread");
  await screen.findByText("Article number 1");
  // The bootstrap answer is what turns the hook on.
  await waitFor(() => expect(screen.getAllByRole("button", { name: "More actions" }).length).toBeGreaterThan(0));
  await settle();
  const row = document.querySelector('[data-item-id="1001"]') as HTMLElement;
  return { beacon, open, row, user: userEvent.setup() };
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
  localStorage.clear();
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
});
afterEach(() => {
  stopStats?.();
  stopStats = undefined;
  resetStatsForTests();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("list surface stats events", () => {
  it("records open_original from the row menu when stats are on", async () => {
    const { beacon, open, row, user } = await ready({});
    await user.click(within(row).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Open original" }));
    expect(open).toHaveBeenCalled();
    await waitFor(async () => expect(await beaconed(beacon, "open_original")).toEqual([{ kind: "open_original", item_id: 1001 }]));
  });

  it("records nothing from the row menu when stats are off", async () => {
    const { beacon, open, row, user } = await ready({ "stats.enabled": false });
    await user.click(within(row).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Open original" }));
    expect(open).toHaveBeenCalled();
    await settle();
    expect(beacon).not.toHaveBeenCalled();
  });

  it("records share from the row menu when the sheet resolves, and not when it is cancelled", async () => {
    const share = vi.fn().mockRejectedValueOnce(new DOMException("dismissed", "AbortError")).mockResolvedValueOnce(undefined);
    const { beacon, row, user } = await ready({}, { share });
    await user.click(within(row).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: /^Share/ }));
    await waitFor(() => expect(share).toHaveBeenCalledTimes(1));
    await settle();
    expect(await beaconed(beacon, "share")).toEqual([]);
    await user.click(within(row).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: /^Share/ }));
    await waitFor(async () => expect(await beaconed(beacon, "share")).toEqual([{ kind: "share", item_id: 1001 }]));
  });

  it("records nothing for a share when stats are off", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    const { beacon, row, user } = await ready({ "stats.enabled": false }, { share });
    await user.click(within(row).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: /^Share/ }));
    await waitFor(() => expect(share).toHaveBeenCalledTimes(1));
    await settle();
    expect(beacon).not.toHaveBeenCalled();
  });

  it("records open_original from the o hotkey (and v) when stats are on", async () => {
    const { beacon, open, user } = await ready({});
    await user.keyboard("jo");
    expect(open).toHaveBeenCalledTimes(1);
    await user.keyboard("v");
    expect(open).toHaveBeenCalledTimes(2);
    await waitFor(async () => expect(await beaconed(beacon, "open_original")).toHaveLength(2));
    expect(await beaconed(beacon, "open_original")).toEqual([
      { kind: "open_original", item_id: 1001 },
      { kind: "open_original", item_id: 1001 },
    ]);
  });

  it("records nothing from the o hotkey when stats are off", async () => {
    const { beacon, open, user } = await ready({ "stats.enabled": false });
    await user.keyboard("jo");
    expect(open).toHaveBeenCalledTimes(1);
    await settle();
    expect(beacon).not.toHaveBeenCalled();
  });
});
