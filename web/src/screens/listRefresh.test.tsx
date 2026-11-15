import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { handleServerEvent, initialLive, liveStore } from "@/api/events";
import type { QueryClient } from "@tanstack/react-query";
import { keys } from "@/api/queries";
import { rowMenuStore } from "@/gestures/rowMenu";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";
import { AWAY_MS, clearListMemory } from "./ListPane";

// A refresh the reader started ends while a list is open: the list looks once for articles above the newest id it
// loaded with, offers them in the pill, and loads them by itself only when nothing would be lost by it.

class NoES {
  addEventListener() {}
  close() {}
}

/** The newest id the open list loaded with (the server's `as_of`); the articles below are the ones it already had. */
const AS_OF = "1005";

beforeEach(() => {
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

let client: QueryClient;
type Items = ReturnType<typeof card>[];

function open(items: () => Items, path = "/l/unread", over: Parameters<typeof mockFetch>[0] = {}) {
  const fetched = mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(items(), null, AS_OF)),
    "GET /api/items/1001": () => json(detail(1)),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    ...over,
  });
  window.history.replaceState({ idx: 0 }, "", path);
  client = makeQueryClient({ retry: false });
  render(<App client={client} />);
  return { ...fetched, loads: () => fetched.calls.filter((c) => c.url.pathname === "/api/items").length };
}

/** A refresh the reader asked for ends. */
const refreshEnds = () =>
  act(() => {
    handleServerEvent(client, { type: "run.start", data: { run_id: "5", kind: "manual", total: 1 } });
    handleServerEvent(client, { type: "run.done", data: { run_id: "5", new_items: 1, errors: 0 } });
  });

/** Let what a refresh set going (a request, its answer, the work after it) settle, and nothing more. */
const settle = () => act(() => new Promise<void>((r) => setTimeout(r, 60)));

const scrollTo = (top: number) => Object.defineProperty(screen.getByTestId("list-scroll"), "scrollTop", { configurable: true, writable: true, value: top });
const setVisibility = (state: "visible" | "hidden") => {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  act(() => void document.dispatchEvent(new Event("visibilitychange")));
};
const fresh = (): Items => [card(9)];

describe("a manual refresh that ends while a list is open", () => {
  it("leaves a reader part-way down where they are and offers the new articles in the pill", async () => {
    let items = [card(1), card(2), card(3)];
    open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(240);
    items = [...fresh(), ...items];
    refreshEnds();
    const pill = await screen.findByRole("button", { name: "1 new article" });
    expect(pill).toHaveAttribute("data-open", "true");
    expect(screen.queryByText("Article number 9")).toBeNull();
    expect(screen.getByText("Article number 1")).toBeInTheDocument();
    await userEvent.setup().click(pill);
    await screen.findByText("Article number 9");
  });

  it("loads the new articles by itself for a reader at the top", async () => {
    let items = [card(1), card(2), card(3)];
    open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(0);
    items = [...fresh(), ...items];
    refreshEnds();
    await screen.findByText("Article number 9");
    expect(screen.queryByRole("button", { name: /new article/ })?.getAttribute("data-open")).not.toBe("true");
  });

  it("keeps the list, and offers the pill, while a row has the focus", async () => {
    let items = [card(1), card(2), card(3)];
    open(() => items);
    const row = await screen.findByText("Article number 2");
    scrollTo(0);
    act(() => row.closest<HTMLElement>("[data-item-id]")?.querySelector<HTMLElement>("a, button")?.focus());
    expect(screen.getByTestId("list-scroll").contains(document.activeElement)).toBe(true);
    items = [...fresh(), ...items];
    refreshEnds();
    await screen.findByRole("button", { name: "1 new article" });
    await settle();
    expect(screen.queryByText("Article number 9")).toBeNull();
  });

  it("keeps the list, and offers the pill, while a menu is open", async () => {
    let items = [card(1), card(2), card(3)];
    open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(0);
    const menu = document.createElement("div");
    menu.setAttribute("role", "menu");
    document.body.appendChild(menu);
    items = [...fresh(), ...items];
    refreshEnds();
    await screen.findByRole("button", { name: "1 new article" });
    await settle();
    expect(screen.queryByText("Article number 9")).toBeNull();
    menu.remove();
  });

  it("keeps an open article's list as it is: the pill, and no reload, even at the top", async () => {
    wide();
    let items = [card(1), card(2), card(3)];
    open(() => items, "/i/1001?from=unread");
    await screen.findByText("Article number 2");
    scrollTo(0);
    items = [...fresh(), ...items];
    refreshEnds();
    await screen.findByRole("button", { name: "1 new article" });
    await settle();
    expect(screen.queryByText("Article number 9")).toBeNull();
  });

  it("on a phone, Back from an article returns to the list as it was left: a refresh that ended meanwhile does not reload it", async () => {
    const items = [card(1), card(2), card(3)];
    const { loads } = open(() => items);
    await userEvent.setup().click(await screen.findByRole("link", { name: /Article number 1/ }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1001"));
    refreshEnds();
    await settle();
    const before = loads();
    // The article's Next and Previous hold the list, so a refresh does not mark it stale.
    expect(client.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(false);
    window.history.back();
    await screen.findByText("Article number 2");
    await settle();
    expect(loads()).toBe(before);
  });

  it("waits for the tab to be shown before looking, then asks once", async () => {
    let items = [card(1), card(2), card(3)];
    const { loads } = open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(240);
    setVisibility("hidden");
    items = [...fresh(), ...items];
    const before = loads();
    refreshEnds();
    await settle();
    expect(loads()).toBe(before);
    setVisibility("visible");
    await screen.findByRole("button", { name: "1 new article" });
    await settle();
    expect(loads()).toBe(before + 1);
  });

  it("coming back after a long time away loads the new articles once, not twice", async () => {
    let items = [card(1), card(2), card(3)];
    const { loads } = open(() => items);
    await screen.findByText("Article number 1");
    let now = 1_000_000;
    vi.spyOn(Date, "now").mockImplementation(() => now);
    setVisibility("hidden");
    items = [...fresh(), ...items];
    const before = loads();
    refreshEnds();
    now += AWAY_MS;
    setVisibility("visible");
    await screen.findByText("Article number 9");
    await settle();
    // One look, and one reload of the list: the jump to the top does not load it a second time.
    expect(loads()).toBe(before + 2);
  });

  it("does nothing, and still goes to the top after a long time away, when the look fails", async () => {
    const items = [card(1), card(2), card(3)];
    let failing = false;
    open(() => items, "/l/unread", { "GET /api/items": () => (failing ? new Response("nope", { status: 500 }) : json(pageOf(items, null, AS_OF))) });
    await screen.findByText("Article number 1");
    const scroll = vi.spyOn(screen.getByTestId("list-scroll"), "scrollTo");
    let now = 1_000_000;
    vi.spyOn(Date, "now").mockImplementation(() => now);
    setVisibility("hidden");
    failing = true;
    refreshEnds();
    now += AWAY_MS;
    setVisibility("visible");
    await waitFor(() => expect(scroll).toHaveBeenCalledWith(expect.objectContaining({ top: 0 })));
    expect(screen.queryByRole("button", { name: /new article/ })?.getAttribute("data-open")).not.toBe("true");
    expect(screen.getByText("Article number 1")).toBeInTheDocument();
  });

  it("offers nothing for articles that are older than the list's own newest: an unread one after the loaded ones were read", async () => {
    let items = [card(1), card(2), card(3)];
    const { loads } = open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(240);
    items = [card(4)]; // below as_of: not an arrival
    const before = loads();
    refreshEnds();
    await waitFor(() => expect(loads()).toBe(before + 1));
    await settle();
    expect(screen.queryByRole("button", { name: /new article/ })?.getAttribute("data-open")).not.toBe("true");
  });

  it.each([
    ["Starred", "/l/starred", () => undefined],
    ["an oldest-first list", "/l/unread", () => updateDevicePrefs({ order: "oldest" })],
  ])("asks nothing for %s: it shows no pill", async (_, path, setUp) => {
    setUp();
    const { loads } = open(() => [card(1), card(2), card(3)], path);
    await screen.findByText("Article number 1");
    fireEvent.scroll(screen.getByTestId("list-scroll"));
    const before = loads();
    refreshEnds();
    await settle();
    expect(loads()).toBe(before);
  });
});
