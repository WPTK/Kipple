import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { settingsKey, type SettingMeta } from "@/api/admin";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { themeStore } from "@/theme/theme";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { navigateTo } from "@/test/nav";

// The master-detail Settings (issue #55): a list of groups then a group page on a narrow screen, a rail beside the
// group page on a wide one, every group at its own URL.

class NoES {
  addEventListener() {}
  close() {}
}

const meta = (m: Partial<SettingMeta> & Pick<SettingMeta, "key" | "kind">): SettingMeta => ({
  value: null,
  default: null,
  label: m.key,
  description: `About ${m.key}`,
  group: "reading",
  surface: "settings",
  ...m,
});

const SETTINGS: SettingMeta[] = [
  meta({ key: "links.strip_tracking", kind: "bool", label: "Remove tracking from links", value: true, default: true }),
  meta({ key: "refresh.interval_minutes", kind: "int", group: "sync", label: "How often to check feeds", value: 30, default: 30, min: 5, max: 1440, step: 5, unit: "minutes" }),
  meta({ key: "stats.enabled", kind: "bool", group: "stats", label: "Reading statistics", value: true, default: true }),
  meta({ key: "tz", kind: "text", group: "account", label: "Time zone", value: "America/New_York", default: "America/New_York" }),
  meta({ key: "greader.icon_urls", kind: "bool", group: "advanced", label: "Send feed icons to sync apps", value: true, default: true }),
];
const settingsBody = (list = SETTINGS) => ({ settings: list, values: Object.fromEntries(list.map((s) => [s.key, s.value])) });

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json(settingsBody()),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

function wide(on: boolean) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: on && query.includes("min-width: 900px"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

const GROUPS = ["Appearance & Reading", "Sync & Feeds", "Statistics", "Filters & Saved Searches", "Account & Devices", "Advanced"];

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  prefsStore.set({ ...DEFAULT_PREFS });
  themeStore.set({ ...DEFAULT_THEME_SETTINGS });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Settings on a narrow screen", () => {
  it("opens on the list of the six groups, with a current value under the ones where it is cheap; passes axe", async () => {
    routes();
    const { container } = go("/settings");
    const nav = await screen.findByRole("navigation", { name: "Settings sections" });
    const links = within(nav).getAllByRole("link");
    expect(links.map((l) => l.getAttribute("href"))).toEqual(["appearance", "sync", "statistics", "filters", "account", "advanced"].map((g) => `/settings/${g}`));
    GROUPS.forEach((g, i) => expect(links[i]).toHaveTextContent(g));
    expect(links[0]).toHaveTextContent("Paper / Midnight · Default font");
    expect(await within(nav).findByText("Every 30 minutes")).toBeInTheDocument();
    expect(within(nav).getByText("Recording on")).toBeInTheDocument();
    // The list is the whole page: no group's settings yet.
    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Appearance" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Back to Settings" })).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("drills into a group with a back button and its name as the heading, and Back returns to the row it left", async () => {
    routes();
    const { container } = go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: /^Sync & Feeds/ }));
    expect(window.location.pathname).toBe("/settings/sync");
    const h1 = await screen.findByRole("heading", { level: 1, name: "Sync & Feeds" });
    await waitFor(() => expect(h1).toHaveFocus()); // arrival focus, as on every screen
    expect(screen.getByRole("heading", { level: 2, name: "Sync" })).toBeInTheDocument();
    expect(await screen.findByRole("spinbutton", { name: "How often to check feeds" })).toHaveValue(30);
    // Only this group: the list and the other groups' settings are gone.
    expect(screen.queryByRole("navigation", { name: "Settings sections" })).toBeNull();
    expect(screen.queryByRole("switch", { name: /Remove tracking from links/ })).toBeNull();
    expect(await axe(container)).toHaveNoViolations();

    await user.click(screen.getByRole("button", { name: "Back to Settings" }));
    await waitFor(() => expect(window.location.pathname).toBe("/settings"));
    const row = await screen.findByRole("link", { name: /^Sync & Feeds/ });
    await waitFor(() => expect(row).toHaveFocus());
  });

  it("Back from a group opened by its link (no list behind it) goes to the list without leaving Settings", async () => {
    routes();
    go("/settings/account");
    const user = userEvent.setup();
    await screen.findByRole("heading", { level: 1, name: "Account & Devices" });
    expect(await screen.findByRole("textbox", { name: "Time zone" })).toHaveValue("America/New_York");
    await user.click(screen.getByRole("button", { name: "Back to Settings" }));
    expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/settings");
  });

  it("the browser's Back button works like the back button", async () => {
    routes();
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: /^Statistics/ }));
    await screen.findByRole("heading", { level: 1, name: "Statistics" });
    expect(screen.getByRole("region", { name: "Your statistics data" })).toBeInTheDocument();
    window.history.back();
    expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/settings");
  });

  it("sends an unknown group back to the list", async () => {
    routes();
    go("/settings/nope");
    expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/settings");
  });

  it("every group page has its own loading and error state, and the groups that need no settings do not wait for them", async () => {
    routes({ "GET /api/settings": () => json({ error: "boom" }, 500) });
    go("/settings/sync");
    expect(await screen.findByText(/Couldn't load your settings/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    navigateTo("/settings/filters");
    expect(await screen.findByRole("heading", { level: 1, name: "Filters & Saved Searches" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Filters" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Saved searches" })).toBeInTheDocument();
    expect(screen.queryByText(/Couldn't load your settings/)).toBeNull();
  });

  it("a failed refetch keeps the settings already loaded on screen, under the error", async () => {
    let fail = false;
    routes({ "GET /api/settings": () => (fail ? json({ error: "boom" }, 500) : json(settingsBody())) });
    window.history.replaceState({ idx: 0 }, "", "/settings/sync");
    const qc = makeQueryClient({ retry: false });
    render(<App client={qc} />);
    expect(await screen.findByRole("spinbutton", { name: "How often to check feeds" })).toHaveValue(30);
    fail = true;
    await act(() => qc.invalidateQueries({ queryKey: settingsKey }));
    expect(await screen.findByText(/Couldn't load your settings/)).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: "How often to check feeds" })).toHaveValue(30);
  });

  it("says so when a group's server settings are all empty instead of showing a blank page", async () => {
    routes({ "GET /api/settings": () => json(settingsBody([])) });
    go("/settings/sync");
    expect(await screen.findByText("No sync or feed settings to change yet.")).toBeInTheDocument();
    navigateTo("/settings/advanced");
    expect(await screen.findByText("No advanced settings to change yet.")).toBeInTheDocument();
  });
});

describe("Settings on a wide screen", () => {
  it("opens the first group beside a rail of all six, and the rail switches groups in place", async () => {
    wide(true);
    routes();
    const { container } = go("/settings");
    const rail = await screen.findByRole("navigation", { name: "Settings sections" });
    // A bare /settings shows the first group in place (no redirect, so its history entry stays the list's).
    expect(window.location.pathname).toBe("/settings");
    expect(within(rail).getAllByRole("link").map((l) => l.textContent)).toEqual(GROUPS);
    await waitFor(() => expect(within(rail).getByRole("link", { name: "Appearance & Reading" })).toHaveAttribute("aria-current", "page"));
    // The rail and the page at once: the h1 stays "Settings" and the group is an h2 with its sections under it.
    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Appearance & Reading" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 3, name: "Accessibility" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Back to Settings" })).toBeNull();
    expect(await axe(container)).toHaveNoViolations();

    const user = userEvent.setup();
    await user.click(within(rail).getByRole("link", { name: "Statistics" }));
    expect(window.location.pathname).toBe("/settings/statistics");
    const h2 = await screen.findByRole("heading", { level: 2, name: "Statistics" });
    await waitFor(() => expect(h2).toHaveFocus()); // moving between groups lands on the group's heading
    expect(await screen.findByRole("switch", { name: /Reading statistics/ })).toBeChecked();
    expect(screen.getByRole("region", { name: "Your statistics data" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Accessibility" })).toBeNull();
    expect(within(rail).getByRole("link", { name: "Statistics" })).toHaveAttribute("aria-current", "page");
    expect(within(rail).getByRole("link", { name: "Appearance & Reading" })).not.toHaveAttribute("aria-current");
    // The rail stays put.
    expect(screen.getByRole("navigation", { name: "Settings sections" })).toBe(rail);
  });

  it("arriving from another screen focuses the Settings heading, not a group's", async () => {
    wide(true);
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    navigateTo("/settings");
    expect(window.location.pathname).toBe("/settings");
    const h1 = await screen.findByRole("heading", { level: 1, name: "Settings" });
    await waitFor(() => expect(h1).toHaveFocus());
  });
});
