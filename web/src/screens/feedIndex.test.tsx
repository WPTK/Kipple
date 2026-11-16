import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { patchItems } from "@/api/queryKeys";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { Bootstrap, Feed, Headline } from "@/api/types";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { REVEAL_STEP } from "@/lib/reveal";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { wide } from "@/test/media";

// The Feed index: one block for each feed with its unread count and its newest headlines.

class NoES {
  addEventListener() {}
  close() {}
}

/** An IntersectionObserver that reports nothing until the test says the watched elements came into view. */
const watched = new Set<{ cb: IntersectionObserverCallback; el: Element }>();
class HeldIO {
  private mine: { cb: IntersectionObserverCallback; el: Element }[] = [];
  constructor(private cb: IntersectionObserverCallback) {}
  observe(el: Element) {
    const w = { cb: this.cb, el };
    this.mine.push(w);
    watched.add(w);
  }
  unobserve() {}
  disconnect() {
    for (const w of this.mine) watched.delete(w);
  }
  takeRecords() {
    return [];
  }
}
const scrollIntoView = () =>
  act(() => {
    for (const w of [...watched]) w.cb([{ target: w.el, isIntersecting: true } as unknown as IntersectionObserverEntry], {} as IntersectionObserver);
  });

const BASE = 1_790_000_000;

function feed(id: number, title: string, over: Partial<Feed> = {}): Feed {
  return { ...bootstrap.feeds![0]!, id: String(id), folder_id: "1", title, unread: 0, ...over };
}

function library(feeds: Feed[]): Bootstrap {
  return { ...bootstrap, feeds, counts: { unread: feeds.reduce((a, f) => a + f.unread, 0), starred: 0 } };
}

const headlines = (feedId: number, n: number, read: number[] = []): Headline[] =>
  Array.from({ length: n }, (_, k) => ({ id: String(feedId * 100 + k), feed_id: String(feedId), title: `Feed ${feedId} story ${k + 1}`, published_at: BASE - k * 3600, read: read.includes(k), starred: false }));

function routes(feeds: Feed[], latest: Record<number, Headline[]>, extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(library(feeds)),
    "GET /api/feeds/latest": () => json({ feeds: Object.entries(latest).map(([id, items]) => ({ feed_id: id, items })) }),
    "GET /api/items": () => json({ items: [], next_cursor: null }),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    ...extra,
  });
}

/** `key` names the history entry: a place remembered for the way back belongs to it, so a test that needs a clean start uses its own. */
function go(path = "/feeds/index", key = "default") {
  window.history.replaceState({ idx: 0, key }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
const blockNames = (root: ParentNode) => [...root.querySelectorAll("section[data-feed-block] h2 a")].map((a) => a.textContent);
const block = (root: ParentNode, id: number) => root.querySelector<HTMLElement>(`section[data-feed-block="${id}"]`)!;

beforeEach(() => {
  clearToasts();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  vi.stubGlobal("EventSource", NoES);
  watched.clear();
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
    const retryAt = Math.floor(Date.now() / 1000) + 7200;
    routes(feeds, { 1: headlines(1, 2), 2: headlines(2, 1) }, { "GET /api/health/feeds": () => json({ feeds: [{ id: "1", enabled: true, next_fetch_at: retryAt }] }) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(within(block(container, 1)).getByTestId("feed-index-failure")).toHaveTextContent(/Kipple can't fetch this feed/);
    await waitFor(() => expect(within(block(container, 1)).getByTestId("feed-index-failure")).toHaveTextContent(/Trying again in/)); // when the feed's own list says it
    expect(within(block(container, 2)).queryByTestId("feed-index-failure")).toBeNull();
  });

  it("says so for a feed the answer leaves out (no articles, or only muted ones)", async () => {
    const { container } = (routes([feed(1, "Empty"), feed(2, "Fine")], { 2: headlines(2, 1) }), go());
    await screen.findByText("Feed 2 story 1");
    expect(within(block(container, 1)).getByText("No articles to show.")).toBeInTheDocument();
    expect(within(block(container, 1)).queryByText(/No articles yet/)).toBeNull();
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

  it("keeps what it shows when a later refresh fails, and only says it can't load when it has nothing", async () => {
    let fail = false;
    routes([feed(1, "Alpha", { unread: 1 })], { 1: headlines(1, 2) }, {
      "GET /api/feeds/latest": () => (fail ? json({ error: "internal" }, 500) : json({ feeds: [{ feed_id: "1", items: headlines(1, 2) }] })),
    });
    const client = makeQueryClient({ retry: false });
    window.history.replaceState({ idx: 0 }, "", "/feeds/index");
    render(<App client={client} />);
    await screen.findByText("Feed 1 story 1");
    fail = true;
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["feeds-latest"] });
    });
    expect(screen.getByText("Feed 1 story 1")).toBeInTheDocument();
    expect(screen.queryByText(/Couldn't load the feed index/)).toBeNull();
  });

  it("puts the scroll position and the row last visited back after an article, and starts at the top when opened afresh", async () => {
    const feeds = Array.from({ length: 30 }, (_, i) => feed(i + 1, `Feed ${String(i + 1).padStart(2, "0")}`));
    routes(feeds, Object.fromEntries(feeds.map((f) => [Number(f.id), headlines(Number(f.id), 3)])));
    const user = userEvent.setup();
    go();
    await screen.findByText("Feed 20 story 1");
    const scroller = screen.getByTestId("feed-index-scroll");
    scroller.scrollTop = 900;
    scroller.dispatchEvent(new Event("scroll"));
    await user.click(screen.getByRole("link", { name: "Feed 20 story 2" }));
    await screen.findByRole("heading", { name: "Feed 20 story 2" }).catch(() => undefined);
    window.history.back();
    await screen.findByText("Feed 20 story 2");
    await waitFor(() => expect(screen.getByTestId("feed-index-scroll").scrollTop).toBe(900));
    expect(screen.getByRole("link", { name: "Feed 20 story 2" })).toHaveFocus();
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

  it("draws a long library a step at a time: a button and the end of the page bring the rest", async () => {
    vi.stubGlobal("IntersectionObserver", HeldIO);
    const feeds = Array.from({ length: 700 }, (_, i) => feed(i + 1, `Feed ${String(i + 1).padStart(3, "0")}`));
    routes(feeds, {});
    const { container } = go();
    await screen.findByText("Feed 001");
    expect(container.querySelectorAll("section[data-feed-block]")).toHaveLength(REVEAL_STEP);
    expect(screen.queryByText("That's every feed.")).toBeNull();
    // A press draws the next step and puts the focus on the first block it added.
    await userEvent.setup().click(screen.getByRole("button", { name: "Show more feeds (400 not shown yet)" }));
    expect(container.querySelectorAll("section[data-feed-block]")).toHaveLength(2 * REVEAL_STEP);
    expect(document.activeElement).toBe(container.querySelector(`section[data-feed-block="${REVEAL_STEP + 1}"] h2 a`));
    // Scrolling near the end draws the rest, and only then does the page say it has shown every feed.
    scrollIntoView();
    await waitFor(() => expect(container.querySelectorAll("section[data-feed-block]")).toHaveLength(700));
    expect(screen.queryByRole("button", { name: /^Show more feeds/ })).toBeNull();
    expect(screen.getByText("That's every feed.")).toBeInTheDocument();
  });

  it("on the way back draws the block of the row last visited, with every block before it, and focuses that row", async () => {
    vi.stubGlobal("IntersectionObserver", HeldIO);
    const feeds = Array.from({ length: 700 }, (_, i) => feed(i + 1, `Feed ${String(i + 1).padStart(3, "0")}`));
    routes(feeds, { 450: headlines(450, 3) });
    const user = userEvent.setup();
    const { container } = go("/feeds/index", "start-fresh");
    await screen.findByText("Feed 001");
    expect(block(container, 450)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Show more feeds (400 not shown yet)" }));
    await user.click(screen.getByRole("link", { name: "Feed 450 story 2" }));
    // Leaving and coming back (a history POP) starts a page with one step drawn; the stop in block 450 is asked for
    // while rendering, so that block is in the first commit that has the data and its link takes the focus.
    window.history.back();
    await screen.findByText("Feed 450 story 2");
    await waitFor(() => expect(screen.getByRole("link", { name: "Feed 450 story 2" })).toHaveFocus());
    expect(block(container, 450)).not.toBeNull();
    expect(block(container, 451)).toBeNull();
    expect(container.querySelectorAll("section[data-feed-block]")).toHaveLength(450);
    // Moving the focus to a block above index 300 later stops asking for block 450, and the drawn set shrinks back to
    // one step: a hold lasts only as long as the row it was made for is the stop.
    act(() => within(block(container, 5)).getByRole("link", { name: /Feed 005/ }).focus());
    await waitFor(() => expect(container.querySelectorAll("section[data-feed-block]")).toHaveLength(REVEAL_STEP));
    expect(block(container, 450)).toBeNull();
  });

  it("has no accessibility violations", async () => {
    routes([feed(1, "Alpha", { unread: 2, status: "failing", last_error: "dial tcp: no such host", last_error_class: "dns" })], { 1: headlines(1, 3, [1]) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(await axe(container)).toHaveNoViolations();
  });
});

describe("the Feed index on a large library", () => {
  const folders = [
    { id: "1", name: "Uncategorized", position: 0, is_default: true, unread: 0 },
    { id: "2", name: "News", position: 1, is_default: false, unread: 6 },
    { id: "3", name: "Tech", position: 2, is_default: false, unread: 1234 },
  ];
  const inFolder = (f: Feed, folder_id: string): Feed => ({ ...f, folder_id });
  const lib = (feeds: Feed[]): Bootstrap => ({ ...library(feeds), folders });
  const grouped = () => {
    const feeds = [inFolder(feed(1, "Wire", { unread: 6 }), "2"), inFolder(feed(2, "Byte", { unread: 1234 }), "3"), inFolder(feed(3, "Chip", { unread: 0 }), "3")];
    return { feeds, latest: { 1: headlines(1, 1), 2: headlines(2, 1), 3: headlines(3, 1) } };
  };
  const routesIn = (feeds: Feed[], latest: Record<number, Headline[]>, extra: Parameters<typeof mockFetch>[0] = {}) =>
    routes(feeds, latest, { "GET /api/bootstrap": () => json(lib(feeds)), ...extra });

  it("draws each feed's count as the sidebar's badge: the number, thousands as 1.2k, and the exact count for a screen reader", async () => {
    const { feeds, latest } = grouped();
    routesIn(feeds, latest);
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(within(block(container, 1)).getByTestId("unread-count")).toHaveTextContent("6");
    const thousands = within(block(container, 2)).getByTestId("unread-count");
    expect(thousands).toHaveTextContent("1.2k");
    expect(thousands.querySelector("[aria-hidden]")).toHaveTextContent("1.2k");
    expect(thousands).toHaveTextContent("1,234 unread");
    expect(within(block(container, 3)).queryByTestId("unread-count")).toBeNull();
    expect(within(block(container, 3)).getByTestId("feed-index-unread")).toHaveTextContent("All read");
    // The device's badge setting rules here too.
    act(() => updateDevicePrefs({ unreadBadge: "dot" }));
    expect(within(block(container, 1)).getByTestId("unread-dot")).toBeInTheDocument();
    act(() => updateDevicePrefs({ unreadBadge: "off" }));
    expect(within(block(container, 1)).queryByTestId("unread-count")).toBeNull();
  });

  it("marks one feed's unread articles read from its own menu, after the question, and offers Undo", async () => {
    const { feeds, latest } = grouped();
    const { calls } = routesIn(feeds, latest, { "POST /api/items/mark-read": () => json({ changed: ["100", "101"], restored: [] }) });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    const user = userEvent.setup();
    expect(within(block(container, 3)).queryByRole("button", { name: /More actions/ })).toBeNull(); // nothing to mark in a read feed
    await user.click(within(block(container, 1)).getByRole("button", { name: "More actions for Wire" }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark feed as read" }));
    expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read")).toHaveLength(0); // it asks first
    await user.click(await screen.findByRole("button", { name: "Mark all 6 as read" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read")).toHaveLength(1));
    const body = JSON.parse(String(calls.find((c) => c.url.pathname === "/api/items/mark-read")!.init?.body));
    expect(body.scope).toMatchObject({ feed_id: "1", view: "unread" });
    expect(await screen.findByRole("button", { name: "Undo" })).toBeInTheDocument();
  });

  it("keeps the menu button on screen where the only pointer is a finger, and a Tab stop only in the block that holds the page's", async () => {
    const { feeds, latest } = grouped();
    routesIn(feeds, latest);
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    const button = within(block(container, 1)).getByRole("button", { name: "More actions for Wire" });
    // Nothing hides it: no row-touch-unseen or hover-only class, which index.css turns invisible on a touch screen.
    expect(button.closest("[class*='row-touch-unseen'],[class*='row-reveal']")).toBeNull();
    expect(button.className).not.toMatch(/row-touch-unseen|row-reveal/);
    expect(button.tabIndex).toBe(0); // the first block holds the page's Tab stop
    expect(within(block(container, 2)).getByRole("button", { name: "More actions for Byte" }).tabIndex).toBe(-1);
  });

  it("groups the feeds under their folders in the sidebar's order, with a jump list that goes to a folder", async () => {
    const { feeds, latest } = grouped();
    routesIn(feeds, latest);
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    const labels = [...container.querySelectorAll("[data-folder-group]")].map((p) => p.textContent);
    expect(labels).toEqual(["News6 unread", "Tech1.2k1,234 unread"]);
    const jump = screen.getByRole("combobox", { name: "Jump to a folder" });
    expect([...jump.querySelectorAll("option")].map((o) => o.textContent)).toEqual(["Jump to a folder", "News (6)", "Tech (1.2k)"]);
    const target = container.querySelector<HTMLElement>('[data-folder-group="3"]')!;
    const into = vi.fn();
    target.scrollIntoView = into;
    await userEvent.setup().selectOptions(jump, "Tech (1.2k)");
    await waitFor(() => expect(into).toHaveBeenCalled());
    expect(target).toHaveFocus();
    // The page's one Tab stop moves with it: Tab from the label reaches the folder's first block, not the one left behind.
    expect(within(block(container, 2)).getByRole("button", { name: "More actions for Byte" }).tabIndex).toBe(0);
    expect(within(block(container, 1)).getByRole("button", { name: "More actions for Wire" }).tabIndex).toBe(-1);
  });

  it("has no folder groups or jump list in the quietest-first order, where a folder's feeds are not together", async () => {
    const { feeds, latest } = grouped();
    routesIn(feeds, latest);
    updateDevicePrefs({ digestOrder: "quiet" });
    const { container } = go();
    await screen.findByText("Feed 1 story 1");
    expect(container.querySelector("[data-folder-group]")).toBeNull();
    expect(screen.queryByRole("combobox", { name: "Jump to a folder" })).toBeNull();
  });
});
