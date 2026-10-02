// Regression: the reading-font choice (one of the bundled fonts plus the ones on the device) must stay reachable from
// Settings > Appearance & Reading, from the Aa menu above every list screen and every article, at phone and desktop
// widths. It was dropped from Settings on 2026-09-25 (67d7685) and was never in the setup wizard; the wizard's own
// check is in setup/wizard.test.tsx ("Step 3").
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { FONTS } from "@/lib/fonts";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

function media(wide: boolean) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: wide && query.includes("min-width: 900px"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

function routes() {
  const folder = bootstrap.folders[0]?.id ?? "1";
  const feed = bootstrap.feeds[0]?.id ?? "1";
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1), card(2)])),
    "GET /api/items/1001": () => json(detail(1)),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    "GET /api/saved-searches": () => json({ searches: [] }),
  });
  return { folder, feed };
}

/** The font select offers every font of lib/fonts, in order, grouped. */
function expectEveryFont(select: HTMLElement) {
  const values = within(select).getAllByRole("option").map((o) => (o as HTMLOptionElement).value);
  expect(values).toEqual(FONTS.map((f) => f.id));
  expect(values.length).toBeGreaterThanOrEqual(12); // Default + the 11 bundled fonts, at the very least
  for (const g of ["Serif", "Sans-serif", "Monospace", "On this device"]) expect(select.querySelector(`optgroup[label="${g}"]`)).not.toBeNull();
}

beforeEach(() => {
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe.each([
  ["phone", false],
  ["desktop", true],
])("font choice on a %s", (_name, wide) => {
  it("Settings > Appearance & Reading has the Reading font with every font and a live preview", async () => {
    media(wide);
    routes();
    go("/settings/appearance");
    const select = await screen.findByRole("combobox", { name: "Reading font" });
    expectEveryFont(select);
    await userEvent.setup().selectOptions(select, "literata");
    expect(prefsStore.get().font).toBe("literata");
    expect(screen.getByTestId("font-preview").style.fontFamily).toContain("Literata");
  });

  it.each([
    ["Unread", "/l/unread"],
    ["All", "/l/all"],
    ["Starred", "/l/starred"],
    ["a feed", "/l/all?feed=FEED"],
    ["a folder", "/l/all?folder=FOLDER"],
    ["Search", "/search?q=article"],
    ["an open article", "/i/1001"],
  ])("the Aa menu above %s has the Reading font", async (_where, path) => {
    media(wide);
    const { feed, folder } = routes();
    go(path.replace("FEED", feed).replace("FOLDER", folder));
    const user = userEvent.setup();
    const buttons = await screen.findAllByRole("button", { name: "Reading appearance" });
    await user.click(buttons[0] as HTMLElement);
    const menu = await screen.findByRole("dialog", { name: "Reading appearance" });
    const select = within(menu).getByRole("combobox", { name: "Reading font" });
    expectEveryFont(select);
    await user.selectOptions(select, "inter");
    expect(prefsStore.get().font).toBe("inter");
  });
});
