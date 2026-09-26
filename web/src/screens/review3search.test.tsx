import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { searchHighlightStore, setSearchHighlight } from "@/lib/searchTerms";
import { setSearchOrder } from "@/lib/searchPrefs";
import { bootstrap, card, detail, json, mockFetch } from "@/test/mockApi";
import type { ItemsPage } from "@/api/types";

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
/** An in-app navigation (a sidebar link): the router hears a popstate. */
function navigateTo(path: string) {
  act(() => {
    window.history.pushState({ idx: 1 }, "", path);
    window.dispatchEvent(new PopStateEvent("popstate", { state: { idx: 1 } }));
  });
}
function wide() {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes("min-width: 900px"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

const items = (over: Partial<ItemsPage> = {}): ItemsPage => ({ items: [card(1, { title: "A big red dog" }), card(2, { title: "Cats" })], next_cursor: null, as_of: "5000", fallback: false, ...over });
const itemCalls = (calls: { method: string; url: URL }[]) => calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items" && !c.url.searchParams.has("include")); // the offline prefetch is not a list load
const base = (extra: Parameters<typeof mockFetch>[0] = {}) =>
  mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }), ...extra });

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  setSearchHighlight(undefined);
  setSearchOrder("rank");
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => vi.unstubAllGlobals());

describe("Search debounce reads the latest params", () => {
  it("tapping the scope chip's X inside the debounce window is not undone by the pending keystroke", async () => {
    base();
    go("/search?feed=1&view=unread&order=date");
    const user = userEvent.setup();
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await screen.findByRole("button", { name: "Search the whole library instead" });
    await user.type(box, "cat");
    await user.click(screen.getByRole("button", { name: "Search the whole library instead" }));
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("q")).toBe("cat"), { timeout: 3000 });
    const sp = new URLSearchParams(window.location.search);
    expect(sp.has("feed")).toBe(false);
    expect(sp.has("view")).toBe(false);
  });

  it("changing Sort inside the window keeps the new order and still lands the text", async () => {
    base();
    go("/search?order=oldest");
    const user = userEvent.setup();
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await user.type(box, "cat");
    await user.selectOptions(screen.getByRole("combobox", { name: "Sort by" }), "date");
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("q")).toBe("cat"), { timeout: 3000 });
    expect(new URLSearchParams(window.location.search).has("order")).toBe(false);
  });
});

describe("Search typing flag follows the text it was set for", () => {
  it("a saved search with the same q but another scope is a submitted search (no typing)", async () => {
    const { calls } = base();
    go("/search");
    const user = userEvent.setup();
    await user.type(await screen.findByRole("searchbox", { name: "Search articles" }), "rust");
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.get("typing")).toBe("1"), { timeout: 3000 });
    navigateTo("/search?q=rust&feed=1&ss=s1");
    await waitFor(() => expect(itemCalls(calls).some((c) => c.url.searchParams.get("feed") === "1")).toBe(true), { timeout: 3000 });
    const c = itemCalls(calls).filter((x) => x.url.searchParams.get("feed") === "1");
    expect(c.every((x) => !x.url.searchParams.has("typing"))).toBe(true);
    await waitFor(() => expect(screen.getByRole("button", { name: "Mark all results as read" })).toBeEnabled(), { timeout: 3000 });
  });

  it("a saved search with a different q never sends one typing=1 request", async () => {
    const { calls } = base();
    go("/search");
    const user = userEvent.setup();
    await user.type(await screen.findByRole("searchbox", { name: "Search articles" }), "rust");
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.get("typing")).toBe("1"), { timeout: 3000 });
    navigateTo("/search?q=cats&ss=s2");
    await waitFor(() => expect(itemCalls(calls).some((c) => c.url.searchParams.get("q") === "cats")).toBe(true), { timeout: 3000 });
    expect(itemCalls(calls).filter((c) => c.url.searchParams.get("q") === "cats").every((c) => !c.url.searchParams.has("typing"))).toBe(true);
  });
});

describe("Mark all while the search is still being typed", () => {
  it("the list menu item is disabled and says why", async () => {
    wide();
    base({
      "GET /api/items/1001": () => json(detail(1)),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1) }),
    });
    go(`/i/1001?from=${encodeURIComponent("all|q:rust|typing:1")}`);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "List actions" }, { timeout: 5000 }));
    const item = await screen.findByRole("menuitem", { name: /Mark all as read/ });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(item).toHaveTextContent("Finish your search first (press Enter)");
  });
});

describe("An article opened from a search", () => {
  it("follows the list's fallback flag when the list loads after the article first drew", async () => {
    wide();
    base({
      "GET /api/items": async () => {
        await new Promise((r) => setTimeout(r, 150));
        return json(items({ fallback: true }));
      },
      "GET /api/items/1001": () => json(detail(1)),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1) }),
    });
    go(`/i/1001?from=${encodeURIComponent("all|q:red%20dogs")}`);
    await waitFor(() => expect(searchHighlightStore.get().key).toBe("red dogs|f|"), { timeout: 5000 });
  });
});
