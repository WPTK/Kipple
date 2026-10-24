import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { patchItems } from "@/api/queryKeys";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { Bootstrap, Card, Feed } from "@/api/types";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch } from "@/test/mockApi";
import { wide } from "@/test/media";

// The Feed index: one block for each feed with its unread count and its newest headlines.

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;

function feed(id: number, title: string, over: Partial<Feed> = {}): Feed {
  return { ...bootstrap.feeds![0]!, id: String(id), folder_id: "1", title, unread: 0, ...over };
}

function library(feeds: Feed[]): Bootstrap {
  return { ...bootstrap, feeds, counts: { unread: feeds.reduce((a, f) => a + f.unread, 0), starred: 0 } };
}

const headlines = (feedId: number, n: number, read: number[] = []): Card[] =>
  Array.from({ length: n }, (_, k) => card(k, { id: String(feedId * 100 + k), feed_id: String(feedId), title: `Feed ${feedId} story ${k + 1}`, published_at: BASE - k * 3600, sort_at: BASE - k * 3600, read: read.includes(k) }));

function routes(feeds: Feed[], latest: Record<number, Card[]>, extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(library(feeds)),
    "GET /api/feeds/latest": () => json({ feeds: Object.entries(latest).map(([id, items]) => ({ feed_id: id, items })) }),
    "GET /api/items": () => json({ items: [], next_cursor: null }),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    ...extra,
  });
}

function go(path = "/feeds/index") {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
const blockNames = (root: ParentNode) => [...root.querySelectorAll("section[data-feed-block] > h2 a")].map((a) => a.textContent);
const block = (root: ParentNode, id: number) => root.querySelector<HTMLElement>(`section[data-feed-block="${id}"]`)!;

beforeEach(() => {
  clearToasts();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the Feed index", () => {
  it("has a block for each feed in the sidebar's order with its name, unread count and three headlines", async () => {
    const feeds = [feed(1, "Beta", { unread: 4 }), feed(2, "Alpha", { unread: 0 })];
    const { calls } = routes(feeds, { 1: headlines(1, 3, [2]), 2: headlines(2, 1) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(blockNames(container)).toEqual(["Alpha", "Beta"]); // the sidebar lists a folder's feeds by title
    expect(within(block(container, 1)).getByTestId("feed-index-unread")).toHaveTextContent("4 unread");
    expect(within(block(container, 2)).getByTestId("feed-index-unread")).toHaveTextContent("All read");
    expect([...block(container, 1).querySelectorAll("article")].map((a) => a.dataset.itemId)).toEqual(["100", "101", "102"]);
    expect(block(container, 1).querySelector('article[data-item-id="102"]')).toHaveAttribute("data-read");
    expect(block(container, 1).querySelector('article[data-item-id="100"]')).not.toHaveAttribute("data-read");
    expect(calls.filter((c) => c.url.pathname === "/api/feeds/latest").map((c) => c.url.search)).toEqual(["?n=3"]);
    expect(calls.filter((c) => c.url.pathname === "/api/items" && c.url.searchParams.has("feed"))).toHaveLength(0); // one request for every feed, not one for each
  });

  it("links the name to the feed's list and each headline to the article, inside that feed's list", async () => {
    routes([feed(1, "Alpha", { unread: 1 })], { 1: headlines(1, 2) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(within(block(container, 1)).getByRole("link", { name: "Alpha" })).toHaveAttribute("href", "/l?feed=1");
    expect(screen.getByRole("link", { name: "Feed 1 story 2" })).toHaveAttribute("href", "/i/101?from=all%7Cfeed%3A1");
  });

  it("orders the feeds that post least first when the device asks for it, like the Digest", async () => {
    const feeds = [feed(1, "Busy", { pace: 9 }), feed(2, "Quiet", { pace: 0.2 }), feed(3, "New feed", { pace: null }), feed(4, "Middle", { pace: 2 })];
    routes(feeds, { 1: headlines(1, 1), 2: headlines(2, 1), 3: headlines(3, 1), 4: headlines(4, 1) });
    updateDevicePrefs({ digestOrder: "quiet" });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(blockNames(container)).toEqual(["Quiet", "Middle", "Busy", "New feed"]);
  });

  it("changes the order from its own menu, which sets the same device setting", async () => {
    routes([feed(1, "Busy", { pace: 9 }), feed(2, "Quiet", { pace: 0.2 })], { 1: headlines(1, 1), 2: headlines(2, 1) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(blockNames(container)).toEqual(["Busy", "Quiet"]);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Order" }));
    await user.click(await screen.findByRole("menuitemradio", { name: /Quiet Feeds First/ }));
    await waitFor(() => expect(blockNames(container)).toEqual(["Quiet", "Busy"]));
  });

  it("says a failing feed can't be fetched, in the words its own list uses, and says nothing for the others", async () => {
    const feeds = [feed(1, "Broken", { status: "failing", last_error: "dial tcp: no such host", last_error_class: "dns" }), feed(2, "Fine")];
    routes(feeds, { 1: headlines(1, 2), 2: headlines(2, 1) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(within(block(container, 1)).getByTestId("feed-index-failure")).toHaveTextContent(/Kipple can't fetch this feed/);
    expect(within(block(container, 2)).queryByTestId("feed-index-failure")).toBeNull();
  });

  it("says so for a feed with no articles", async () => {
    const { container } = (routes([feed(1, "Empty")], {}), go());
    await screen.findByText("No articles yet.");
    expect(within(block(container, 1)).getByText("No articles yet.")).toBeInTheDocument();
  });

  it("is one Tab stop, and the arrow keys visit the feed names and the headlines in the order drawn", async () => {
    routes([feed(1, "Alpha", { unread: 2 }), feed(2, "Beta", { unread: 1 })], { 1: headlines(1, 2), 2: headlines(2, 1) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    const links = [...container.querySelectorAll<HTMLAnchorElement>("[data-feed-index] a")];
    expect(links.map((a) => a.textContent)).toEqual(["Alpha", "Feed 1 story 1", "Feed 1 story 2", "Beta", "Feed 2 story 1"]);
    expect(links.filter((a) => a.tabIndex !== -1).map((a) => a.textContent)).toEqual(["Alpha"]);
    const user = userEvent.setup();
    links[0]!.focus();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(links[1]).toHaveFocus());
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(links[2]).toHaveFocus());
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(links[3]).toHaveFocus());
    await user.keyboard("{End}");
    await waitFor(() => expect(links[4]).toHaveFocus());
    await user.keyboard("{Home}");
    await waitFor(() => expect(links[0]).toHaveFocus());
    // The stop follows focus: only the row last visited is in the Tab order.
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(links[1]).toHaveFocus());
    expect(links.filter((a) => a.tabIndex !== -1).map((a) => a.textContent)).toEqual(["Feed 1 story 1"]);
  });

  it("shows a loading state, then an error with a way to try again", async () => {
    let fail = true;
    routes([feed(1, "Alpha")], { 1: headlines(1, 1) }, {
      "GET /api/feeds/latest": () => (fail ? json({ error: "internal" }, 500) : json({ feeds: [{ feed_id: "1", items: headlines(1, 1) }] })),
    });
    go();
    expect(await screen.findByText(/Couldn't load the feed index/)).toBeInTheDocument();
    fail = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByText("Feed 1 story 1");
  });

  it("offers adding a feed when there are none", async () => {
    routes([], {});
    go();
    expect(await screen.findByText("No feeds yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add a feed" })).toBeInTheDocument();
  });

  it("is reached from the sidebar on a wide screen", async () => {
    routes([feed(1, "Alpha")], { 1: headlines(1, 1) });
    wide();
    go("/l/unread");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(await within(nav).findByRole("link", { name: "Feed index" })).toHaveAttribute("href", "/feeds/index");
  });

  it("is reached from the Feeds screen on a phone, where the Feeds tab stays current", async () => {
    routes([feed(1, "Alpha")], { 1: headlines(1, 1) });
    const first = go("/feeds");
    const link = await screen.findByRole("link", { name: "Feed index" });
    expect(link).toHaveAttribute("href", "/feeds/index");
    first.unmount();
    go("/feeds/index");
    await screen.findByText("Feed 1 story 1");
    const tabs = screen.getByRole("navigation", { name: "Primary" });
    expect(within(tabs).getByRole("link", { name: "Feeds" })).toHaveAttribute("aria-current", "page");
  });

  it("shows a headline as read when an article is read elsewhere in the app", async () => {
    routes([feed(1, "Alpha", { unread: 2 })], { 1: headlines(1, 2) });
    const client = makeQueryClient({ retry: false });
    window.history.replaceState({ idx: 0 }, "", "/feeds/index");
    const { container } = render(<App client={client} />);
    await screen.findByText("Feed 1 story 1");
    expect(block(container, 1).querySelector('article[data-item-id="100"]')).not.toHaveAttribute("data-read");
    act(() => patchItems(client, ["100"], { read: true }));
    await waitFor(() => expect(block(container, 1).querySelector('article[data-item-id="100"]')).toHaveAttribute("data-read"));
  });

  it("has no accessibility violations", async () => {
    routes([feed(1, "Alpha", { unread: 2, status: "failing", last_error: "dial tcp: no such host", last_error_class: "dns" })], { 1: headlines(1, 3, [1]) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(await axe(container)).toHaveNoViolations();
  });
});
