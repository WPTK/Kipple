import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { handleServerEvent, initialLive, liveStore } from "@/api/events";
import type { QueryClient } from "@tanstack/react-query";
import { keys, scopeKey } from "@/api/queries";
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

describe("a fetch the reader did not start finishes while a list is open", () => {
  const fetchDone = (trigger: string, over: Record<string, unknown> = {}) =>
    act(() => void handleServerEvent(client, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "ok", new_items: 1, new_item_ids: ["1009"], trigger, ...over } } as never));

  it.each([["a scheduled poll", "scheduled"], ["a new feed's first fetch", "subscribe"]])(
    "%s offers the new articles in the pill without a word aloud, and moves nothing, even at the top",
    async (_, trigger) => {
      const toasts = await import("@/shell/toasts");
      const say = vi.spyOn(toasts, "announce");
      let items = [card(1), card(2), card(3)];
      const { loads } = open(() => items);
      await screen.findByText("Article number 1");
      scrollTo(0);
      const before = loads();
      items = [...fresh(), ...items];
      fetchDone(trigger);
      const pill = await screen.findByRole("button", { name: "1 new article" });
      expect(pill).toHaveAttribute("data-open", "true");
      await settle();
      expect(say).not.toHaveBeenCalled();
      expect(loads()).toBe(before);
      expect(screen.queryByText("Article number 9")).toBeNull();
      expect(screen.getByText("Article number 1")).toBeInTheDocument();
      await userEvent.setup().click(pill);
      await screen.findByText("Article number 9");
    },
  );

  it.each([["an import", "import", {}], ["a retention trim", "retention", { outcome: "trim_only", new_items: 0, new_item_ids: [], trimmed_items: 4 }]])(
    "%s raises no pill",
    async (_, trigger, over) => {
      const items = [card(1), card(2), card(3)];
      open(() => items);
      await screen.findByText("Article number 1");
      fetchDone(trigger, over);
      await settle();
      expect(screen.queryByRole("button", { name: /new article/ })?.getAttribute("data-open")).not.toBe("true");
    },
  );

  it("does not count an article the list already holds", async () => {
    const items = [card(1), card(2), card(3)];
    open(() => items);
    await screen.findByText("Article number 1");
    fetchDone("scheduled", { new_item_ids: ["1002"] });
    await settle();
    expect(screen.queryByRole("button", { name: /new article/ })?.getAttribute("data-open")).not.toBe("true");
  });

  it("asks for the unread counts again, so the header does not stay behind the pill", async () => {
    const items = [card(1), card(2), card(3)];
    const { calls } = open(() => items);
    await screen.findByText("Article number 1");
    const boots = () => calls.filter((c) => c.url.pathname === "/api/bootstrap").length;
    const before = boots();
    fetchDone("scheduled");
    await waitFor(() => expect(boots()).toBe(before + 1), { timeout: 3000 });
  });
});

describe("a reload that fails", () => {
  const scheduled = (ids: string[]) =>
    act(() => void handleServerEvent(client, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "ok", new_items: ids.length, new_item_ids: ids, trigger: "scheduled" } }));

  it("keeps the pill, so pressing it again can try again", async () => {
    let items = [card(1), card(2), card(3)];
    let broken = false;
    open(() => items, "/l/unread", { "GET /api/items": () => (broken ? json({ error: "down" }, 500) : json(pageOf(items, null, AS_OF))) });
    await screen.findByText("Article number 1");
    items = [card(9), ...items];
    scheduled(["1009"]);
    const pill = await screen.findByRole("button", { name: "1 new article" });
    broken = true;
    await userEvent.setup().click(pill);
    await settle();
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 1 });
    broken = false;
    await userEvent.setup().click(await screen.findByRole("button", { name: "1 new article" }));
    await screen.findByText("Article number 9");
    expect(liveStore.get().pendingByFeed).toEqual({});
  });

  it("leaves an empty list owed its reload, and keeps what it was told of", async () => {
    let items: Items = [];
    let broken = false;
    const { loads } = open(() => items, "/l/unread", { "GET /api/items": () => (broken ? json({ error: "down" }, 500) : json(pageOf(items, null, AS_OF))) });
    await screen.findByText("All caught up");
    broken = true;
    items = [card(1)];
    const before = loads();
    scheduled(["1001"]);
    await waitFor(() => expect(loads()).toBeGreaterThan(before), { timeout: 3000 });
    await settle();
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 1 });
    expect(liveStore.get().arrivalsHandled[scopeKey({ view: "unread" })] ?? 0).toBe(0);
  });

  it("leaves the pill what arrived while the reload was on its way", async () => {
    let items = [card(1), card(2), card(3)];
    let release: () => void = () => undefined;
    let hold: Promise<void> | null = null;
    open(() => items, "/l/unread", {
      "GET /api/items": async () => {
        if (hold) await hold;
        return json(pageOf(items, null, AS_OF));
      },
    });
    await screen.findByText("Article number 1");
    items = [card(9), ...items];
    scheduled(["1009"]);
    const pill = await screen.findByRole("button", { name: "1 new article" });
    hold = new Promise<void>((r) => (release = r));
    await userEvent.setup().click(pill);
    // A second fetch finishes while the list is being read again; the answer does not hold its article.
    scheduled(["1010"]);
    hold = null;
    release();
    await screen.findByText("Article number 9");
    await settle();
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 1 });
    expect(liveStore.get().pendingIds).toEqual({ "1": ["1010"] });
  });

  it("makes the other cached lists stale when an empty list fills itself, as pressing the pill does", async () => {
    let items: Items = [];
    open(() => items);
    await screen.findByText("All caught up");
    client.setQueryData(keys.items({ view: "all" }), { pages: [pageOf([card(5)], null, AS_OF)], pageParams: [undefined] });
    expect(client.getQueryState(keys.items({ view: "all" }))?.isInvalidated).toBe(false);
    items = [card(1)];
    scheduled(["1001"]);
    await screen.findByText("Article number 1", undefined, { timeout: 3000 });
    expect(client.getQueryState(keys.items({ view: "all" }))?.isInvalidated).toBe(true);
  });
});

describe("an empty list when feeds finish their first fetch", () => {
  const firstFetch = (feed: string) =>
    act(() => void handleServerEvent(client, { type: "fetch.done", data: { feed_id: feed, outcome: "ok", new_items: 5, trigger: "new_feed" } }));

  it("fills by itself, once for a burst of feeds, instead of staying on All caught up", async () => {
    let items: Items = [];
    const { loads } = open(() => items);
    await screen.findByText("All caught up");
    const before = loads();
    items = [card(1), card(2)];
    firstFetch("1");
    firstFetch("2");
    firstFetch("3");
    await screen.findByText("Article number 1", undefined, { timeout: 3000 });
    expect(loads()).toBe(before + 1);
  });

  it("offers no pill for articles it has just loaded by itself", async () => {
    let items: Items = [];
    open(() => items);
    await screen.findByText("All caught up");
    items = [card(1), card(2)];
    act(() => void handleServerEvent(client, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "ok", new_items: 2, trigger: "scheduled" } }));
    await screen.findByText("Article number 1", undefined, { timeout: 3000 });
    await settle();
    expect(screen.queryByRole("button", { name: /new article/ })?.getAttribute("data-open")).not.toBe("true");
  });

  it("leaves a list that has articles alone when a feed finishes a silent fetch", async () => {
    const { loads } = open(() => [card(1), card(2)]);
    await screen.findByText("Article number 1");
    const before = loads();
    firstFetch("1");
    await act(() => new Promise<void>((r) => setTimeout(r, 1200)));
    expect(loads()).toBe(before);
  });
});

describe("an empty list and which arrivals it reloads for", () => {
  const fetched = (feed: string) =>
    act(() => void handleServerEvent(client, { type: "fetch.done", data: { feed_id: feed, outcome: "ok", new_items: 2, trigger: "new_feed" } }));

  it("reloads for an arrival that landed while the first request was still pending", async () => {
    let items: Items = [];
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => (release = r));
    let first = true;
    const { loads } = open(() => items, "/l/unread", {
      // The first answer is the empty one that was in flight when the articles arrived; later ones hold them.
      "GET /api/items": async () => {
        if (!first) return json(pageOf(items, null, AS_OF));
        first = false;
        await gate;
        return json(pageOf([], null, AS_OF));
      },
    });
    await act(() => new Promise<void>((r) => setTimeout(r, 30)));
    items = [card(1)];
    fetched("1");
    await act(() => new Promise<void>((r) => setTimeout(r, 1000)));
    release();
    await screen.findByText("Article number 1", undefined, { timeout: 3000 });
    expect(loads()).toBeGreaterThanOrEqual(2);
  });

  it("reloads an empty Muted list for fetches that brought in only muted articles", async () => {
    const { loads } = open(() => [], "/l/muted");
    await settle();
    const before = loads();
    act(() => void handleServerEvent(client, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, muted_items: 2, new_item_ids: [], trigger: "scheduled" } }));
    await waitFor(() => expect(loads()).toBeGreaterThan(before), { timeout: 3000 });
  });

  it("still reloads for the next arrival after the feeds that had arrived were removed", async () => {
    const { loads } = open(() => []);
    await settle();
    act(() => void liveStore.set((s) => ({ ...s, arrivals: { "9": 2 } })));
    await waitFor(() => expect(liveStore.get().arrivalsHandled[scopeKey({ view: "unread" })]).toBe(2), { timeout: 3000 });
    // The feed is deleted: its arrivals go with it, so the list's total falls below what it handled.
    act(() => void liveStore.set((s) => ({ ...s, arrivals: {} })));
    await settle();
    const before = loads();
    fetched("1");
    await waitFor(() => expect(loads()).toBeGreaterThan(before), { timeout: 3000 });
  });

  it("does not reload for a feed that is not in the list's folder or feed", async () => {
    const { loads } = open(() => [], "/l/unread?feed=2");
    await settle();
    const before = loads();
    fetched("1");
    await act(() => new Promise<void>((r) => setTimeout(r, 1200)));
    expect(loads()).toBe(before);
  });
});

describe("handled arrivals are kept for each list", () => {
  it("another list's handled arrivals do not hide a first arrival from an empty list", async () => {
    // Another list (all of Starred) has already taken in five arrivals; this feed's list is empty and has seen none.
    liveStore.set((s) => ({ ...s, arrivalsHandled: { [scopeKey({ view: "starred" })]: 5 } }));
    let items: Items = [];
    const { loads } = open(() => items, "/l/unread?feed=1");
    await settle();
    const before = loads();
    items = [card(2)];
    act(() => void handleServerEvent(client, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 1, trigger: "new_feed" } }));
    await act(() => new Promise<void>((r) => setTimeout(r, 1300)));
    expect(loads()).toBeGreaterThan(before);
  });
});
