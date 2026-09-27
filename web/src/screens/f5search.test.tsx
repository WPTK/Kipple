import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { setSearchHighlight } from "@/lib/searchTerms";
import { setSearchOrder } from "@/lib/searchPrefs";
import { devicePrefsStore } from "@/lib/devicePrefs";
import { bootstrap, card, json, mockFetch } from "@/test/mockApi";
import { navigateTo } from "@/test/nav";
import type { ItemsPage } from "@/api/types";

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const items = (over: Partial<ItemsPage> = {}): ItemsPage => ({ items: [card(1, { title: "A big red dog" }), card(2, { title: "Cats" })], next_cursor: null, as_of: "5000", fallback: false, ...over });
const itemCalls = (calls: { method: string; url: URL }[]) => calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items" && !c.url.searchParams.has("include")); // the offline prefetch is not a list load

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  setSearchHighlight(undefined);
  setSearchOrder("rank");
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Search: the / key on the Search screen", () => {
  it("focuses the box and keeps the query instead of navigating to a blank search", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search?q=cats");
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    expect(box).toHaveValue("cats");
    const row = (await screen.findByText("Cats")).closest("[data-item-id]")!.querySelector<HTMLElement>("a")!;
    row.focus();
    expect(box).not.toHaveFocus();
    await userEvent.setup().keyboard("/");
    await waitFor(() => expect(box).toHaveFocus());
    expect(new URLSearchParams(window.location.search).get("q")).toBe("cats");
    expect(box).toHaveValue("cats");
  });
});

describe("Search: the / key from another screen", () => {
  // Child effects run before the shell's: Search focused its box, then the shell's arrival focus moved it to the
  // heading. The / key now asks for the box (router state), the box names itself the arrival target, and the shell
  // is the only one that focuses.
  it("lands in the search box, not on the screen heading (UAT Suite 2, TC-R3)", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/l/unread");
    await screen.findByText("Cats");
    (document.activeElement as HTMLElement | null)?.blur();
    await userEvent.setup().keyboard("/");
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await waitFor(() => expect(box).toHaveFocus());
    // Used once: Back or Forward to this entry later arrives at the heading like any other screen.
    await waitFor(() => expect((window.history.state as { usr?: unknown } | null)?.usr ?? null).toBeNull());
    expect(window.location.pathname).toBe("/search");
  });

  it("arriving from the Search link or tab still goes to the heading, which announces the screen", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/l/unread");
    await screen.findByText("Cats");
    navigateTo("/search");
    const heading = await screen.findByRole("heading", { name: "Search", level: 1 });
    await waitFor(() => expect(heading).toHaveFocus());
  });

  it("a page load or reload of Search puts the caret in the box", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search");
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await waitFor(() => expect(box).toHaveFocus());
  });

  it("arriving back at results (a search with a query) still goes to the heading", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/l/unread");
    await screen.findByText("Cats");
    navigateTo("/search?q=cats"); // Back to results, or a saved search
    const heading = await screen.findByRole("heading", { name: "Search", level: 1 });
    await waitFor(() => expect(heading).toHaveFocus());
    expect(screen.getByRole("searchbox", { name: "Search articles" })).toHaveValue("cats");
  });
});

describe("Search: what is sent", () => {
  it("sends the text untrimmed with typing=1 while typing, and drops typing when submitted", async () => {
    const { calls } = mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search");
    const user = userEvent.setup();
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await user.type(box, "big re ");
    await waitFor(() => expect(itemCalls(calls).length).toBeGreaterThan(0), { timeout: 3000 });
    const typing = itemCalls(calls).at(-1)?.url.searchParams;
    expect(typing?.get("q")).toBe("big re "); // untrimmed: the trailing space says the word is finished
    expect(typing?.get("typing")).toBe("1");
    expect(typing?.get("order")).toBe("rank");
    expect(typing?.get("view")).toBe("all");

    await user.type(box, "{Enter}");
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.has("typing")).toBe(false), { timeout: 3000 });
    expect(itemCalls(calls).at(-1)?.url.searchParams.get("q")).toBe("big re ");
  });

  it("typing one more character and backspacing to the submitted query is not typing again", async () => {
    const { calls } = mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search");
    const user = userEvent.setup();
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await user.type(box, "cat{Enter}");
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.has("typing")).toBe(false), { timeout: 3000 });
    const markAll = screen.getByRole("button", { name: "Mark all results as read" });
    await waitFor(() => expect(markAll).toBeEnabled());
    await user.type(box, "s");
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.get("typing")).toBe("1"), { timeout: 3000 });
    expect(markAll).toBeDisabled();
    await user.keyboard("{Backspace}");
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("q")).toBe("cat"), { timeout: 3000 });
    // Back at the submitted text: the exact search again, so Mark all is allowed.
    expect(screen.getByRole("button", { name: "Mark all results as read" })).toBeEnabled();
  });

  it("a search opened from the URL (a saved search, a reload) is a submitted search: no typing", async () => {
    const { calls } = mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search?q=big%20red&order=oldest&feed=1");
    await screen.findByText("Cats");
    const p = itemCalls(calls)[0]?.url.searchParams;
    expect(p?.has("typing")).toBe(false);
    expect(p?.get("order")).toBe("oldest");
    expect(p?.get("feed")).toBe("1");
  });

  it("keeps the ordering choice on this device: Relevance by default, Newest sends no order", async () => {
    const { calls } = mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search?q=cat");
    await screen.findByText("Cats");
    expect(screen.getByRole("combobox", { name: "Sort by" })).toHaveValue("rank");
    expect(itemCalls(calls)[0]?.url.searchParams.get("order")).toBe("rank");
    const user = userEvent.setup();
    await user.selectOptions(screen.getByRole("combobox", { name: "Sort by" }), "date");
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.has("order")).toBe(false));
    expect(devicePrefsStore.get().searchOrder).toBe("date");
    await user.selectOptions(screen.getByRole("combobox", { name: "Sort by" }), "oldest");
    await waitFor(() => expect(itemCalls(calls).at(-1)?.url.searchParams.get("order")).toBe("oldest"));
    expect(devicePrefsStore.get().searchOrder).toBe("oldest");
  });

  it("shows one 'Best matches first' header instead of day headers in relevance order, and day headers by date", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    const { unmount } = go("/search?q=cat");
    await screen.findByText("Cats");
    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual(["Best matches first"]);
    unmount();
    setSearchOrder("date");
    go("/search?q=cat");
    await screen.findByText("Cats");
    expect(screen.queryByRole("heading", { name: "Best matches first" })).toBeNull();
    expect(document.querySelector("h2.sticky")).not.toBeNull();
  });

  it("a relevance search with no hits shows the empty message, not a lone header", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items({ items: [] })), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search?q=zzzz");
    expect(await screen.findByText(/No results for "zzzz"/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Best matches first" })).toBeNull();
  });

  it("puts the syntax in a help popover", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Search tips" }));
    const tips = await screen.findByLabelText("Search tips", { selector: "[role=dialog]" });
    for (const t of ['"exact phrase"', "-exclude", "title:word", "author:name", "word*"]) expect(within(tips).getByText(t)).toBeInTheDocument();
  });
});

describe("Search: what comes back", () => {
  it("says so quietly when the server fell back to partial matches, and marks the words the fallback used", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(items({ fallback: true })),
      "GET /api/saved-searches": () => json({ saved_searches: [] }),
    });
    go("/search?q=%22big%20red%20cat%22");
    expect(await screen.findByText("No exact matches: showing partial matches")).toBeInTheDocument();
    // The fallback ORs the words, so the phrase is split: "big" and "red" are marked in the title, not the phrase.
    await waitFor(() => expect([...document.querySelectorAll("mark.kp-hl")].map((m) => m.textContent)).toEqual(expect.arrayContaining(["big", "red", "Cats"])));
    expect(await axe(document.body)).toHaveNoViolations();
  });

  it("has no banner for an exact search, and marks the query terms", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search?q=red");
    await screen.findByText("Cats");
    expect(screen.queryByText(/No exact matches/)).toBeNull();
    await waitFor(() => expect([...document.querySelectorAll("mark.kp-hl")].map((m) => m.textContent)).toEqual(["red"]));
  });

  it("shows the server's message for a 422 search_too_broad, not the generic error", async () => {
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json({ error: "search_too_broad", message: "That search matches too much. Add a longer or more specific word." }, 422),
      "GET /api/saved-searches": () => json({ saved_searches: [] }),
    });
    go("/search?q=a%20b%20c");
    expect(await screen.findByText("That search matches too much. Add a longer or more specific word.")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load articles")).toBeNull();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(itemCalls(calls)).toHaveLength(1); // it is not retried
  });

  it("restarts the search when a later page's relevance cursor is refused with 400 bad_cursor", async () => {
    let restarted = 0;
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": (url) => {
        if (url.searchParams.get("cursor")) return json({ error: "bad_cursor" }, 400);
        restarted++;
        return json(items({ next_cursor: restarted === 1 ? "r|old" : null }));
      },
      "GET /api/saved-searches": () => json({ saved_searches: [] }),
    });
    go("/search?q=cat");
    await waitFor(() => expect(itemCalls(calls).map((c) => c.url.searchParams.get("cursor"))).toEqual([null, "r|old", null]), { timeout: 4000 });
    expect(await screen.findByText("Cats")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load more.")).toBeNull();
  });
});

describe("Search: other 400s", () => {
  it("does not restart on a 400 that is not bad_cursor", async () => {
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": (url) => (url.searchParams.get("cursor") ? json({ error: "bad_request" }, 400) : json(items({ next_cursor: "r|x" }))),
      "GET /api/saved-searches": () => json({ saved_searches: [] }),
    });
    go("/search?q=cat");
    await screen.findByText("Cats");
    await waitFor(() => expect(itemCalls(calls).length).toBeGreaterThanOrEqual(2), { timeout: 3000 });
    await new Promise((r) => setTimeout(r, 300));
    expect(itemCalls(calls).map((c) => c.url.searchParams.get("cursor"))).toEqual([null, "r|x"]);
  });
});

describe("Search: marking results read", () => {
  it("echoes the list's fallback flag in scope.fallback, and only for a submitted search", async () => {
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(items({ fallback: true })),
      "GET /api/saved-searches": () => json({ saved_searches: [] }),
      "POST /api/items/mark-read": () => json({ changed: ["1001", "1002"], restored: [], count: 2, undoable: true }),
    });
    go("/search");
    const user = userEvent.setup();
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await user.type(box, "big red");
    const markAll = await screen.findByRole("button", { name: "Mark all results as read" }, { timeout: 3000 });
    await screen.findByText("No exact matches: showing partial matches");
    // Still typing: what is shown is a wider set than the exact one the server would mark.
    expect(markAll).toBeDisabled();
    await user.type(box, "{Enter}");
    await waitFor(() => expect(screen.getByRole("button", { name: "Mark all results as read" })).toBeEnabled(), { timeout: 3000 });
    await user.click(screen.getByRole("button", { name: "Mark all results as read" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")).toBe(true));
    const post = calls.find((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read");
    const sent = JSON.parse(String(post?.init?.body));
    expect(sent.scope).toEqual({ view: "all", all: true, q: "big red", fallback: true });
    expect(sent.read).toBe(true);
    expect(sent.max_id).toBe("5000");
  });

  it("echoes fallback:false for an exact search, so the server marks the exact expression", async () => {
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(items()),
      "GET /api/saved-searches": () => json({ saved_searches: [] }),
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }),
    });
    go("/search?q=cat");
    await screen.findByText("Cats");
    await userEvent.setup().click(screen.getByRole("button", { name: "Mark all results as read" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const sent = JSON.parse(String(calls.find((c) => c.method === "POST")?.init?.body));
    expect(sent.scope.fallback).toBe(false);
  });

  it("disables mark above and below in a relevance-sorted search", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(items()), "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    go("/search?q=cat");
    await screen.findByText("Cats");
    const user = userEvent.setup();
    await user.click(screen.getAllByRole("button", { name: "More actions" })[0] as HTMLElement);
    expect(await screen.findByRole("menuitem", { name: /Mark above as read/ })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("menuitem", { name: /Mark below as read/ })).toHaveAttribute("aria-disabled", "true");
  });
});
