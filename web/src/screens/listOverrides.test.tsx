import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { modeFields, modeOf, ruleLabel } from "@/api/filters";
import { parseScopeKey, scopeKey } from "@/api/queries";
import { devicePrefsStore, resetDevicePrefs, setListOverride, updateDevicePrefs } from "@/lib/devicePrefs";
import { updatePrefs } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { ListOverrideFields } from "./feeds/ListOverrideFields";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

// Issue #38: per-feed and per-folder order and opening view, the reading-time filter, and "Only show matching".

class NoES {
  addEventListener() {}
  close() {}
}

function routes() {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1), card(2)])),
    "POST /api/items/mark-read": () => json({ changed: ["1001", "1002"], restored: [], count: 2, undoable: true }),
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

// The lists' own requests: the offline prefetch of Unread (`include=content`) is left out.
const itemCalls = (calls: { method: string; url: URL }[]) =>
  calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items" && c.url.searchParams.get("include") !== "content");

beforeEach(() => {
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updatePrefs({ shortcuts: true });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the view a feed or folder opens in", () => {
  it("a feed opens in Unread unless it or a folder above it says otherwise", async () => {
    routes();
    go("/l?feed=1");
    await screen.findByText("Article number 1");
    expect(window.location.pathname + window.location.search).toBe("/l/unread?feed=1");
  });

  it("follows its folder's view, and its own beats the folder's", async () => {
    setListOverride("folder", "1", "view", "all");
    const { calls } = routes();
    const r = go("/l?feed=1");
    await screen.findByText("Article number 1");
    expect(window.location.pathname + window.location.search).toBe("/l/all?feed=1");
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("view")).toBe("all");
    r.unmount();
    setListOverride("feed", "1", "view", "unread");
    go("/l?feed=1");
    await screen.findByText("Article number 1");
    expect(window.location.pathname + window.location.search).toBe("/l/unread?feed=1");
  });

  it("the sidebar links leave the view to the list", async () => {
    routes();
    go("/feeds");
    expect(await screen.findByRole("link", { name: /Example Feed/ })).toHaveAttribute("href", "/l?feed=1");
    expect(screen.getByRole("link", { name: /^News/ })).toHaveAttribute("href", "/l?folder=1");
  });

  it("is set from the list header's menu on a feed list", async () => {
    routes();
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /^Layout:/ }));
    const group = screen.getByRole("group", { name: "View this feed opens in" });
    expect(within(group).getByRole("menuitemradio", { name: "Use the default (Unread)" })).toBeChecked();
    await user.click(within(group).getByRole("menuitemradio", { name: "All" }));
    expect(devicePrefsStore.get().overrides.feed["1"]).toEqual({ view: "all" });
  });

  it("is not offered on the Unread, All and Starred lists", async () => {
    routes();
    go("/l/all");
    await screen.findByText("Article number 1");
    await userEvent.setup().click(screen.getByRole("button", { name: /^Layout:/ }));
    expect(screen.queryByRole("group", { name: /opens in/ })).toBeNull();
    expect(screen.getByRole("group", { name: "Order" })).toBeInTheDocument();
  });
});

describe("per-feed order", () => {
  it("a feed's own order sorts its list; other lists keep the device order", async () => {
    setListOverride("feed", "1", "order", "oldest");
    const { calls } = routes();
    const r = go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("order")).toBe("oldest");
    expect(screen.getByRole("button", { name: "Oldest first" })).toHaveAttribute("aria-pressed", "true");
    r.unmount();
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("order")).toBeNull();
    expect(screen.getByRole("button", { name: "Oldest first" })).toHaveAttribute("aria-pressed", "false");
  });

  it("a folder's order reaches the feeds inside it", async () => {
    setListOverride("folder", "1", "order", "oldest");
    const { calls } = routes();
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("order")).toBe("oldest");
  });

  it("the header toggle on a feed list sets the feed's order, and toggling back clears it", async () => {
    const { calls } = routes();
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Oldest first" }));
    expect(devicePrefsStore.get().overrides.feed["1"]).toEqual({ order: "oldest" });
    expect(devicePrefsStore.get().order).toBe("newest"); // the device default did not move
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.get("order")).toBe("oldest"));
    await user.click(screen.getByRole("button", { name: "Oldest first" }));
    expect(devicePrefsStore.get().overrides.feed["1"]).toBeUndefined(); // back to what it inherits: no override
  });

  it("the header toggle on the Unread list sets the device order", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    await userEvent.setup().click(screen.getByRole("button", { name: "Oldest first" }));
    expect(devicePrefsStore.get().order).toBe("oldest");
    expect(devicePrefsStore.get().overrides.feed).toEqual({});
  });

  it("the menu names where a feed's order comes from", async () => {
    setListOverride("folder", "1", "order", "oldest");
    routes();
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    await userEvent.setup().click(screen.getByRole("button", { name: /^Layout:/ }));
    const group = screen.getByRole("group", { name: "Order of this feed" });
    expect(within(group).getByRole("menuitemradio", { name: "Inherited from News (Oldest first)" })).toBeChecked();
  });
});

describe("the feed and folder editors", () => {
  it("set and clear each field of the override", async () => {
    render(<ListOverrideFields kind="folder" id="4" />);
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Order on this device"), "oldest");
    await user.selectOptions(screen.getByLabelText("Opens in"), "all");
    expect(devicePrefsStore.get().overrides.folder["4"]).toEqual({ order: "oldest", view: "all" });
    await user.selectOptions(screen.getByLabelText("Order on this device"), "default");
    await user.selectOptions(screen.getByLabelText("Opens in"), "default");
    expect(devicePrefsStore.get().overrides.folder["4"]).toBeUndefined();
  });
});

describe("reading-time filter", () => {
  it("filters the list through the address, keeps it across views and clears from the chip", async () => {
    const { calls } = routes();
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Reading time: Any length" }));
    await user.click(screen.getByRole("menuitemradio", { name: "5 min or less" }));
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.get("max_minutes")).toBe("5"));
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("min_minutes")).toBeNull();
    expect(window.location.search).toBe("?feed=1&len=short");
    expect(screen.getByRole("button", { name: "Reading time: 5 min or less" })).toBeInTheDocument();

    // The view pills keep it.
    await user.click(screen.getByRole("link", { name: "All" }));
    await waitFor(() => expect(window.location.pathname).toBe("/l/all"));
    expect(window.location.search).toBe("?feed=1&len=short");

    // A longer length sends both bounds.
    await user.click(screen.getByRole("button", { name: "Reading time: 5 min or less" }));
    await user.click(screen.getByRole("menuitemradio", { name: "6 to 15 min" }));
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.get("min_minutes")).toBe("6"));
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("max_minutes")).toBe("15");

    // The chip clears it.
    await user.click(screen.getByRole("link", { name: "Reading time 6 to 15 min. Show any length" }));
    await waitFor(() => expect(window.location.search).toBe("?feed=1"));
    expect(screen.getByRole("button", { name: "Reading time: Any length" })).toBeInTheDocument();
  });

  it("mark all as read marks only what the filter shows", async () => {
    const { calls } = routes();
    go("/l/unread?len=long");
    await screen.findByText("Article number 1");
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("min_minutes")).toBe("16");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "List actions" }));
    await user.click(screen.getByRole("menuitem", { name: /Mark all as read/ }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")).toBe(true));
    const body = JSON.parse(String(calls.find((c) => c.url.pathname === "/api/items/mark-read")?.init?.body));
    expect(body.scope).toMatchObject({ all: true, view: "unread", min_minutes: 16 });
    expect(body.scope.max_minutes).toBeUndefined();
  });

  it("an opened article's way back keeps the filter", () => {
    const scope = { view: "all" as const, folder: "3", length: "medium" as const, order: "oldest" as const };
    expect(parseScopeKey(scopeKey(scope))).toEqual(scope);
    expect(parseScopeKey("unread|len:forever")).toEqual({ view: "unread" });
  });

  it("an ignored length in the address shows the whole list", async () => {
    const { calls } = routes();
    go("/l/unread?len=forever");
    await screen.findByText("Article number 1");
    expect(itemCalls(calls).at(-1)?.url.searchParams.has("min_minutes")).toBe(false);
    expect(itemCalls(calls).at(-1)?.url.searchParams.has("max_minutes")).toBe(false);
  });
});

describe("Only show matching", () => {
  it("is an inverted Mute, the one stored form", () => {
    expect(modeOf({ action: "mute", invert: true })).toBe("only");
    expect(modeOf({ action: "mute", invert: false })).toBe("mute");
    expect(modeOf({ action: "star", invert: true })).toBe("star");
    expect(modeFields("only", { action: "star", invert: false })).toEqual({ action: "mute", invert: true });
    // Leaving it clears the inversion; Mute and Highlight are never inverted.
    expect(modeFields("star", { action: "mute", invert: true })).toEqual({ action: "star", invert: false });
    expect(modeFields("mute", { action: "star", invert: true })).toEqual({ action: "mute", invert: false });
    expect(modeFields("highlight", { action: "star", invert: true })).toEqual({ action: "highlight", invert: false });
    // Between Mark as read and Star the invert option keeps its value.
    expect(modeFields("mark_read", { action: "star", invert: true })).toEqual({ action: "mark_read", invert: true });
  });

  it("is named in the rule list", () => {
    expect(ruleLabel({ action: "mute", invert: true })).toBe("Only show matching");
    expect(ruleLabel({ action: "star", invert: true })).toBe("Star when it does not match");
    expect(ruleLabel({ action: "mute", invert: false })).toBe("Mute");
  });
});
