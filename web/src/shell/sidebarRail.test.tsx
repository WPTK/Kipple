import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { DEFAULT_DEVICE_PREFS, DEVICE_PREFS_KEY, devicePrefsStore, updateDevicePrefs } from "@/lib/devicePrefs";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { rail, wide } from "@/test/media";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  prefsStore.set({ ...DEFAULT_PREFS });
  devicePrefsStore.set({ ...DEFAULT_DEVICE_PREFS });
  vi.stubGlobal("EventSource", NoES);
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/health": () => json({}),
    "PATCH /api/device": () => json({}),
  });
  window.history.replaceState({ idx: 0 }, "", "/l/unread");
});
afterEach(() => vi.unstubAllGlobals());

async function open() {
  render(<App client={makeQueryClient({ retry: false })} />);
  return screen.findByRole("navigation", { name: "Primary" });
}

describe("the sidebar under 1100 px is an icon rail", () => {
  it("shows the primary destinations by name, a tooltip each, and not the feed tree", async () => {
    rail();
    const nav = await open();
    for (const name of ["Unread", "All articles", "Starred", "Muted", "Search", "Feeds", "Settings"]) {
      const link = within(nav).getByRole("link", { name });
      expect(link).toHaveAttribute("title", name);
    }
    expect(within(nav).queryByRole("link", { name: "Feed index" })).toBeNull();
    expect(within(nav).queryByRole("tree")).toBeNull();
    const show = within(nav).getByRole("button", { name: "Show sidebar" });
    expect(show).toHaveAttribute("aria-expanded", "false");
    expect(show.className).toContain("size-11"); // a 44 px target
  });

  it("opens with the toggle, keeps focus on the toggle that replaces it, and remembers it on this device", async () => {
    rail();
    const user = userEvent.setup();
    const nav = await open();
    const show = within(nav).getByRole("button", { name: "Show sidebar" });
    show.focus();
    await user.keyboard("{Enter}");
    const hide = await within(await screen.findByRole("navigation", { name: "Primary" })).findByRole("button", { name: "Hide sidebar" });
    expect(hide).toHaveAttribute("aria-expanded", "true");
    expect(hide).toHaveFocus();
    expect(screen.getByRole("link", { name: "Feed index" })).toBeInTheDocument();
    expect(devicePrefsStore.get().sidebarExpanded).toBe(true);
    expect(JSON.parse(localStorage.getItem(DEVICE_PREFS_KEY) ?? "{}").sidebarExpanded).toBe(true);
  });

  it("closes again to the rail with focus on the Show sidebar button", async () => {
    rail();
    updateDevicePrefs({ sidebarExpanded: true });
    const user = userEvent.setup();
    await open();
    await user.click(await screen.findByRole("button", { name: "Hide sidebar" }));
    const show = await screen.findByRole("button", { name: "Show sidebar" });
    await waitFor(() => expect(show).toHaveFocus());
    expect(devicePrefsStore.get().sidebarExpanded).toBe(false);
    expect(screen.queryByRole("link", { name: "Feed index" })).toBeNull();
  });

  it("starts open where this device left it open", async () => {
    rail();
    updateDevicePrefs({ sidebarExpanded: true });
    await open();
    expect(await screen.findByRole("button", { name: "Hide sidebar" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show sidebar" })).toBeNull();
  });

  it("is the full sidebar from 1100 px, with no toggle, whatever was saved", async () => {
    wide();
    const nav = await open();
    await waitFor(() => expect(within(nav).getByRole("link", { name: "Feed index" })).toBeInTheDocument());
    expect(within(nav).queryByRole("button", { name: /sidebar$/ })).toBeNull();
    updateDevicePrefs({ sidebarExpanded: false });
    expect(within(nav).getByRole("link", { name: "Feed index" })).toBeInTheDocument();
  });
});
