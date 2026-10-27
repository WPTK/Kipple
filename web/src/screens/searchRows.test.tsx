import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { setSearchOrder } from "@/lib/searchPrefs";
import { bootstrap, card, json, mockFetch } from "@/test/mockApi";
import { endCopy } from "./ListPane";
import type { ItemsPage, SavedSearch } from "@/api/types";

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const page = (cards = [card(1, { title: "A big red dog" }), card(2, { title: "Cats" })], over: Partial<ItemsPage> = {}): ItemsPage => ({ items: cards, next_cursor: null, as_of: "5000", fallback: false, ...over });
const SAVED: SavedSearch[] = [{ id: "s1", name: "Rust news", q: "rust", order: "rank" }];
const api = (items: ItemsPage, saved: SavedSearch[] = []) => ({
  "GET /api/bootstrap": () => json(bootstrap),
  "GET /api/items": () => json(items),
  "GET /api/saved-searches": () => json({ saved_searches: saved }),
});

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  setSearchOrder("rank");
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Search: the results line", () => {
  it("says how many results there are for the query", async () => {
    mockFetch(api(page()));
    go("/search?q=cats");
    await waitFor(() => expect(screen.getByTestId("result-count")).toHaveTextContent("2 results for cats"));
  });

  it("says result, not results, for one", async () => {
    mockFetch(api(page([card(1, { title: "Cats" })])));
    go("/search?q=cats");
    await waitFor(() => expect(screen.getByTestId("result-count")).toHaveTextContent("1 result for cats"));
  });

  it("marks a count that is not the whole of the results with a plus", async () => {
    mockFetch(api(page(undefined, { next_cursor: "more" })));
    go("/search?q=cats");
    await waitFor(() => expect(screen.getByTestId("result-count")).toHaveTextContent("2+ results for cats"));
  });

  it("announces the settled result once, in a region that is there before the results", async () => {
    mockFetch(api(page()));
    go("/search?q=cats");
    const live = await screen.findByTestId("result-announce");
    expect(live).toHaveAttribute("role", "status");
    expect(live).toHaveTextContent("");
    expect(screen.getByTestId("result-count")).not.toHaveAttribute("role");
    await waitFor(() => expect(live).toHaveTextContent("2 results for cats"), { timeout: 4000 });
  });
});

describe("Search: announcing and the short screen", () => {
  it("announces a search once: the next page changing the count says nothing more", async () => {
    // The first page has more behind it; the next page changes the count the line under the box shows.
    let calls = 0;
    mockFetch({
      ...api(page()),
      "GET /api/items": () => json(++calls === 1 ? page(undefined, { next_cursor: "more" }) : page([card(3, { title: "Dogs" }), card(4, { title: "Birds" })])),
    });
    go("/search?q=cats");
    const live = await screen.findByTestId("result-announce");
    await waitFor(() => expect(live.textContent).not.toBe(""), { timeout: 4000 });
    const said = live.textContent;
    const heard: string[] = [];
    const watch = new MutationObserver(() => heard.push(live.textContent ?? ""));
    watch.observe(live, { childList: true, characterData: true, subtree: true });
    // The line under the box follows the count; the announcement does not.
    await new Promise((r) => setTimeout(r, 1000));
    watch.disconnect();
    expect(heard).toEqual([]);
    expect(live.textContent).toBe(said);
  });

  describe("on a screen shorter than its header", () => {
    const sizes = (client: number, offset: number, scroll: number) => {
      vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(client);
      vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(offset);
      vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(scroll);
    };
    afterEach(() => vi.restoreAllMocks());

    it("marks the screen short when the header takes more than half of it, and not otherwise", async () => {
      sizes(100, 80, 400);
      mockFetch(api(page()));
      const short = go("/search?q=cats");
      await screen.findByTestId("result-count");
      expect(short.container.querySelector(".search-shell")).toHaveAttribute("data-short");
      short.unmount();
      vi.restoreAllMocks();
      sizes(1000, 10, 1000);
      mockFetch(api(page()));
      const tall = go("/search?q=cats");
      await screen.findByTestId("result-count");
      expect(tall.container.querySelector(".search-shell")).not.toHaveAttribute("data-short");
    });

    it("moves the screen, not the list, for PageDown and the arrow keys", async () => {
      sizes(100, 80, 400);
      mockFetch(api(page()));
      const { container } = go("/search?q=cats");
      await screen.findByTestId("result-count");
      const shell = container.querySelector<HTMLElement>(".search-shell")!;
      const results = container.querySelector<HTMLElement>(".search-results")!;
      expect(shell.scrollTop).toBe(0);
      fireEvent.keyDown(results, { key: "PageDown" });
      expect(shell.scrollTop).toBeGreaterThan(0);
      const after = shell.scrollTop;
      fireEvent.keyDown(results, { key: "ArrowUp" });
      expect(shell.scrollTop).toBeLessThan(after);
    });

    it("leaves the keys of a focused result to the list, which moves focus between rows", async () => {
      sizes(100, 80, 400);
      mockFetch(api(page()));
      const { container } = go("/search?q=cats");
      await screen.findByTestId("result-count");
      const shell = container.querySelector<HTMLElement>(".search-shell")!;
      await waitFor(() => expect(container.querySelector("article[data-item-id] a")).not.toBeNull());
      fireEvent.keyDown(container.querySelector("article[data-item-id] a")!, { key: "PageDown" });
      expect(shell.scrollTop).toBe(0);
    });

    it("leaves keys alone on a tall screen", async () => {
      sizes(1000, 10, 1000);
      mockFetch(api(page()));
      const { container } = go("/search?q=cats");
      await screen.findByTestId("result-count");
      const shell = container.querySelector<HTMLElement>(".search-shell")!;
      fireEvent.keyDown(container.querySelector(".search-results")!, { key: "PageDown" });
      expect(shell.scrollTop).toBe(0);
    });
  });
});

describe("Search: a failed search", () => {
  it("announces the error, not that there were no results", async () => {
    mockFetch({ ...api(page()), "GET /api/items": () => json({ error: "boom" }, 500) });
    go("/search?q=cats");
    const live = await screen.findByTestId("result-announce");
    await waitFor(() => expect(live.textContent).not.toBe(""), { timeout: 4000 });
    expect(live).not.toHaveTextContent("No results");
  });

  it("announces no results when the search worked and found none", async () => {
    mockFetch(api(page([])));
    go("/search?q=cats");
    const live = await screen.findByTestId("result-announce");
    await waitFor(() => expect(live).toHaveTextContent("No results for cats"), { timeout: 4000 });
  });
});

describe("Search: Mark all names what it will mark", () => {
  it("counts the unread results, not the read ones that are loaded", async () => {
    mockFetch(api(page([card(1), card(2), card(3, { read: true }), card(4, { read: true })])));
    const user = userEvent.setup();
    go("/search?q=cats");
    const btn = await screen.findByRole("button", { name: "Mark all 2 unread results as read" });
    await waitFor(() => expect(btn).toBeEnabled());
    await user.click(btn);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("All unread articles in these results are marked read.")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Mark all 2 unread results as read" })).toBeInTheDocument();
  });

  it("gives no count when none are unread, or when more results are still to load", async () => {
    mockFetch(api(page([card(1, { read: true }), card(2, { read: true })])));
    const first = go("/search?q=cats");
    expect(await screen.findByRole("button", { name: "Mark all results as read" })).toBeInTheDocument();
    first.unmount();
    mockFetch(api(page(undefined, { next_cursor: "more" })));
    go("/search?q=cats");
    expect(await screen.findByRole("button", { name: "Mark all results as read" })).toBeInTheDocument();
  });
});

describe("Search: Saved and Edit", () => {
  it("offers Save this search for a new query", async () => {
    mockFetch(api(page(), SAVED));
    go("/search?q=cats");
    expect(await screen.findByRole("button", { name: "Save this search" })).toBeInTheDocument();
    expect(screen.getByTestId("result-count")).not.toHaveTextContent("Saved");
  });

  it("shows Saved and offers Edit saved search when the query is a saved one", async () => {
    mockFetch(api(page(), SAVED));
    go("/search?q=rust&ss=s1");
    expect(await screen.findByRole("button", { name: "Edit saved search" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("result-count")).toHaveTextContent("Saved"));
  });
});

describe("Search: before a query", () => {
  it("shows an example and the saved searches as links", async () => {
    mockFetch(api(page(), SAVED));
    go("/search");
    expect(await screen.findByText("title:bridges")).toBeInTheDocument();
    const list = await screen.findByRole("list", { name: "Saved searches" });
    const link = within(list).getByRole("link", { name: "Rust news" });
    expect(link).toHaveAttribute("href", "/search?q=rust&order=rank&ss=s1");
  });

  it("shows only the example when there are no saved searches", async () => {
    mockFetch(api(page()));
    go("/search");
    expect(await screen.findByText("title:bridges")).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Saved searches" })).toBeNull();
  });
});

describe("The list's end line and count", () => {
  it("names the list it ends", () => {
    expect(endCopy({ view: "all", q: "x" })).toBe("End of results");
    expect(endCopy({ view: "unread", after: "1" })).toBe("End of new articles");
    expect(endCopy({ view: "muted" })).toBe("End of muted");
    expect(endCopy({ view: "starred" })).toBe("End of starred");
    expect(endCopy({ view: "unread" })).toBe("End of unread");
    expect(endCopy({ view: "all" })).toBe("End of articles");
  });

  it("draws the end line under the last row of a list read to its end", async () => {
    mockFetch(api(page()));
    go("/search?q=cats");
    expect(await screen.findByTestId("list-end")).toHaveTextContent("End of results");
  });

  it("puts the unread count beside the title in Unread, and none in All", async () => {
    mockFetch(api(page()));
    const first = go("/l/unread");
    expect(await screen.findByTestId("list-count")).toBeInTheDocument();
    // The count sits beside the heading, not in it: the heading reads as the list's name alone.
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Unread");
    first.unmount();
    go("/l/all");
    await screen.findByText("Cats");
    expect(screen.queryByTestId("list-count")).toBeNull();
  });
});
