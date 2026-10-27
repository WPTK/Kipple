import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { Bootstrap, Card, Feed } from "@/api/types";
import { resetCatchup, catchupGroups, withinVisit } from "@/lib/catchup";
import { devicePrefsStore, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory, resetScrollReadForTests } from "./ListPane";
import { clearDigestPlaces } from "./digestSource";
import { parseScopeKey, scopeKey } from "@/api/queryKeys";
import { updatePrefs } from "@/lib/prefs";

// The New page: the unread articles that arrived since the account last left it, a few for each top-level folder.

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;
const NAMES = ["Alpha", "Beta", "Gamma", "Delta"];

function library(unread: number[]): Bootstrap {
  const folders = unread.map((n, i) => ({ id: String(10 + i), name: NAMES[i]!, position: i, is_default: i === 0, unread: n }));
  const feeds: Feed[] = unread.map((n, i) => ({ ...bootstrap.feeds![0]!, id: String(20 + i), folder_id: String(10 + i), title: `${NAMES[i]} Feed`, unread: n }));
  return { ...bootstrap, folders, feeds, counts: { unread: unread.reduce((a, b) => a + b, 0), starred: 0 } };
}

const articlesOf = (n: number, count: number): Card[] =>
  Array.from({ length: count }, (_, k) =>
    card(1, { id: String(1000 * (n + 1) + k), title: `${NAMES[n]} story ${k + 1}`, feed_id: String(20 + n), source: `${NAMES[n]} Feed`, published_at: BASE - k * 3600, sort_at: BASE - k * 3600 }),
  );

/** What the server holds: `arrived[n]` new articles in folder n; the mark and as_of the next catchup answers. */
const world = { mark: "100", asOf: "900", arrived: [] as number[], seen: [] as string[] };

function routes(arrived: number[], extra: Parameters<typeof mockFetch>[0] = {}) {
  world.arrived = arrived;
  const boot = library(arrived);
  const articles = arrived.flatMap((c, n) => articlesOf(n, c));
  return mockFetch({
    "GET /api/bootstrap": () => json(boot),
    "GET /api/catchup": (u) => {
      const groups = (u.searchParams.get("groups") ?? "").split(",").filter(Boolean);
      return json({
        mark: world.mark,
        as_of: world.asOf,
        sections: groups.map((g) => ({ group: g, count: g.startsWith("d") ? (world.arrived[Number(g.slice(1)) - 10] ?? 0) : 0 })),
      });
    },
    "POST /api/catchup/seen": (_u, init) => {
      const body = JSON.parse(String(init?.body)) as { as_of: string };
      world.seen.push(body.as_of);
      world.mark = body.as_of;
      return json({ mark: body.as_of });
    },
    "GET /api/items": (u) => {
      const n = boot.folders.findIndex((f) => f.id === u.searchParams.get("folder"));
      if (n < 0) return json(pageOf(u.searchParams.has("folder") ? [] : articles.slice(0, Number(u.searchParams.get("limit") ?? 50)))); // the whole library, or a prefetch
      const all = articlesOf(n, world.arrived[n]!);
      const limit = Number(u.searchParams.get("limit") ?? 50);
      const from = Number(u.searchParams.get("cursor")?.slice(1) ?? 0);
      return json(pageOf(all.slice(from, from + limit), from + limit < all.length ? `c${from + limit}` : null, world.asOf));
    },
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids ?? [], restored: [] }),
    ...Object.fromEntries(
      articles.flatMap((c) => [
        [`GET /api/items/${c.id}`, () => json(detail(1, { id: c.id, title: c.title }))],
        [`POST /api/items/${c.id}/open`, () => json({ session_key: "k", item: detail(1, { id: c.id, read: true }) })],
      ]),
    ),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
const headings = (root: ParentNode) => [...root.querySelectorAll("section[data-section] > h2")].map((h) => h.textContent);
const storyIds = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>("article[data-item-id]")].map((a) => a.dataset.itemId);
const section = (root: ParentNode, name: string) => [...root.querySelectorAll<HTMLElement>("section[data-section]")].find((s) => s.querySelector("h2")?.textContent?.startsWith(name))!;
const sectionCalls = (calls: { method: string; url: URL }[]) => calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items" && c.url.searchParams.has("folder"));

beforeEach(() => {
  clearToasts();
  clearListMemory();
  clearDigestPlaces();
  resetScrollReadForTests();
  resetCatchup();
  updatePrefs({ markReadOnScroll: false });
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updateDevicePrefs({ peekSeen: true, layout: "magazine" });
  Object.assign(world, { mark: "100", asOf: "900", arrived: [], seen: [] });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the New page", () => {
  it("lists a folder's newest three with how many are new, and 'N more' for the rest, whatever the device's layout", async () => {
    const { calls } = routes([8, 2, 0, 5]);
    const { container } = go("/l/new");
    await screen.findByText("Alpha story 1");
    expect(devicePrefsStore.get().layout).toBe("magazine");
    expect(headings(container)).toEqual(["Alpha8 new", "Beta2 new", "Delta5 new"]); // Gamma has nothing new
    expect(storyIds(container)).toEqual(["1000", "1001", "1002", "2000", "2001", "4000", "4001", "4002"]);
    expect(screen.getByRole("button", { name: "5 more in Alpha" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /more in Beta/ })).toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "New" })).toBeInTheDocument();
    // The server was asked how many arrived after the mark, for the top-level folders; each list is asked after the same mark.
    const ask = calls.find((c) => c.url.pathname === "/api/catchup")!;
    expect(ask.url.searchParams.get("groups")).toBe("d10,d11,d12,d13");
    expect(sectionCalls(calls).every((c) => c.url.searchParams.get("after") === "100" && c.url.searchParams.get("view") === "unread")).toBe(true);
  });

  it("ends with a line when everything new is on the page, and says so when nothing is new", async () => {
    routes([2, 1]);
    const { container, unmount } = go("/l/new");
    await screen.findByText("Alpha story 1");
    expect(await screen.findByTestId("digest-end")).toHaveTextContent("That's every folder with something new.");
    expect(container.querySelector("[data-digest]")).not.toBeNull();
    unmount();
    resetCatchup();
    routes([0, 0]);
    go("/l/new");
    expect(await screen.findByText("Nothing new")).toBeInTheDocument();
    expect(screen.queryByTestId("digest-end")).toBeNull();
  });

  it("opens a folder out in place and moves focus to the first article it brought in", async () => {
    routes([30, 2]);
    const user = userEvent.setup();
    const { container } = go("/l/new");
    await screen.findByText("Beta story 1");
    await user.click(screen.getByRole("button", { name: "27 more in Alpha" }));
    await waitFor(() => expect(storyIds(section(container, "Alpha"))).toHaveLength(23));
    await waitFor(() => expect(screen.getByRole("link", { name: /^Alpha story 4/ })).toHaveFocus());
    expect(storyIds(section(container, "Beta"))).toEqual(["2000", "2001"]);
  });

  it("a section that fails says so and offers Retry while the others show", async () => {
    let fail = true;
    routes([3, 3], {
      "GET /api/items": (u) => {
        const n = u.searchParams.get("folder") === "10" ? 0 : u.searchParams.get("folder") === "11" ? 1 : -1;
        if (n < 0) return json(pageOf([]));
        if (n === 1 && fail) return json({ error: "gone" }, 404);
        return json(pageOf(articlesOf(n, 3), null, world.asOf));
      },
    });
    const user = userEvent.setup();
    const { container } = go("/l/new");
    await screen.findByText("Alpha story 1");
    expect(within(section(container, "Beta")).getByRole("alert")).toHaveTextContent("Couldn't load Beta.");
    fail = false;
    await user.click(within(section(container, "Beta")).getByRole("button", { name: "Retry" }));
    await screen.findByText("Beta story 1");
  });

  it("says so when the server cannot be asked, and asks again on Try again", async () => {
    let up = false;
    routes([3], { "GET /api/catchup": () => (up ? json({ mark: "100", as_of: "900", sections: [{ group: "d10", count: 3 }] }) : json({ error: "gone" }, 500)) });
    const user = userEvent.setup();
    go("/l/new");
    expect(await screen.findByText("Couldn't load what is new")).toBeInTheDocument();
    up = true;
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByText("Alpha story 1");
  });

  it("has a New pill before Unread on the library, not on a folder's list, and the other pills leave the page", async () => {
    routes([3, 3]);
    const user = userEvent.setup();
    go("/l/unread");
    await screen.findByText("Alpha story 1");
    const pills = within(screen.getByRole("navigation", { name: "Show" })).getAllByRole("link");
    expect(pills.map((p) => p.textContent)).toEqual(["New", "Unread", "All", "Starred"]);
    await user.click(pills[0]!);
    await waitFor(() => expect(window.location.pathname).toBe("/l/new"));
    expect(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "New" })).toHaveAttribute("aria-current", "page");
    expect(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "Unread" })).not.toHaveAttribute("aria-current");
  });

  it("has no New pill on a folder's list", async () => {
    routes([3, 3]);
    go("/l/unread?folder=10");
    await screen.findByText("Alpha story 1");
    expect(within(screen.getByRole("navigation", { name: "Show" })).queryByRole("link", { name: "New" })).toBeNull();
  });

  it("Mark all as read marks only what arrived after the mark", async () => {
    const { calls } = routes([3, 3]);
    const user = userEvent.setup();
    go("/l/new");
    await screen.findByText("Beta story 1");
    await user.click(screen.getByRole("button", { name: "List actions" }));
    await user.click(await screen.findByRole("menuitem", { name: /Mark all 6 as read/ }));
    await user.click(await screen.findByRole("button", { name: "Mark all 6 as read" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read").length).toBeGreaterThan(0));
    const bodies = calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read").map((c) => JSON.parse(String(c.init?.body)));
    expect(bodies.every((b) => b.scope.after_id === "100" && b.scope.view === "unread")).toBe(true);
    expect(bodies[0].scope.all).toBe(true);
    expect(bodies[0].max_id).toBe("900");
  });

  it("passes axe", async () => {
    routes([5, 2]);
    const { container } = go("/l/new");
    await screen.findByText("Alpha story 1");
    expect(await axe(container)).toHaveNoViolations();
  });
});

describe("the mark", () => {
  const seenCalls = (calls: { method: string; url: URL; init?: RequestInit }[]) => calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/catchup/seen");

  it("does not move when an article of the page is opened, and moves to the page's as_of when the page is left", async () => {
    const { calls } = routes([3, 3]);
    const user = userEvent.setup();
    go("/l/new");
    await screen.findByText("Alpha story 1");
    await user.click(screen.getByRole("link", { name: /^Alpha story 2/ }));
    await screen.findByTestId("article-body");
    expect(window.location.pathname).toBe("/i/1001");
    expect(window.location.search).toContain("after%3A100");
    expect(seenCalls(calls)).toHaveLength(0);
    // The page is not left by closing the article either.
    window.history.back();
    await screen.findByText("Beta story 1");
    expect(seenCalls(calls)).toHaveLength(0);
    // Hiding the tab is not leaving.
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    fireEvent(document, new Event("visibilitychange"));
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
    expect(seenCalls(calls)).toHaveLength(0);
    // Going anywhere else is.
    await user.click(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "Unread" }));
    await waitFor(() => expect(seenCalls(calls)).toHaveLength(1));
    expect(JSON.parse(String(seenCalls(calls)[0]!.init?.body))).toEqual({ as_of: "900" });
  });

  it("shows only what arrived after it on the next visit", async () => {
    const { calls } = routes([2, 2]);
    const user = userEvent.setup();
    const { container } = go("/l/new");
    await screen.findByText("Beta story 1");
    await user.click(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "Unread" }));
    await waitFor(() => expect(world.seen).toEqual(["900"]));
    // Two more arrive in Beta; the mark is now 900, so only they are new.
    world.asOf = "950";
    world.arrived = [0, 2];
    await user.click(await screen.findByRole("link", { name: "New" }));
    await waitFor(() => expect(headings(container)).toEqual(["Beta2 new"]));
    const asks = calls.filter((c) => c.url.pathname === "/api/catchup");
    expect(asks).toHaveLength(2);
    expect(sectionCalls(calls).at(-1)!.url.searchParams.get("after")).toBe("900");
  });

  it("is not moved by a reload, which starts the visit again from the same mark", async () => {
    const { calls } = routes([2]);
    const first = go("/l/new");
    await screen.findByText("Alpha story 1");
    first.unmount();
    resetCatchup();
    go("/l/new");
    await screen.findByText("Alpha story 1");
    expect(calls.filter((c) => c.url.pathname === "/api/catchup")).toHaveLength(2);
    expect(world.seen).toEqual([]);
  });

  it("moves once the reader goes back to the page from an article opened by its address", async () => {
    // A wide screen: the page's list stands beside the article, with the pills above it.
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("min-width: 900px"), media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }));
    const { calls } = routes([3, 3]);
    const user = userEvent.setup();
    go("/i/1001?from=unread%7Cafter%3A100"); // a reload on an article of the page: the visit is adopted with mark 100
    await screen.findByTestId("article-body");
    const show = () => within(screen.getByRole("navigation", { name: "Show" }));
    // Going to the page itself leaves nothing: the visit goes on, now as the page's own.
    await user.click(show().getByRole("link", { name: "New" }));
    await screen.findByText("Beta story 1");
    expect(calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/catchup/seen")).toHaveLength(0);
    // The page was read; leaving it now moves the mark to what the visit knew of.
    await user.click(show().getByRole("link", { name: "Unread" }));
    await waitFor(() => expect(world.seen).toEqual(["900"]));
  });
});

describe("the page's loading, failure and edges", () => {
  it("keeps the header, and so focus on its pill, while the visit is not ready", async () => {
    routes([3], { "GET /api/catchup": () => json({ error: "gone" }, 500) });
    go("/l/new");
    expect(await screen.findByText("Couldn't load what is new")).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "New" })).toHaveAttribute("aria-current", "page");
  });

  it("forgets an earlier failure when the page is opened again", async () => {
    let state: "down" | "hung" = "down";
    const { calls } = routes([3], { "GET /api/catchup": () => (state === "down" ? json({ error: "gone" }, 500) : new Promise<Response>(() => {})) });
    const user = userEvent.setup();
    go("/l/new");
    expect(await screen.findByText("Couldn't load what is new")).toBeInTheDocument();
    await user.click(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "Unread" }));
    await screen.findByText("Alpha story 1");
    state = "hung";
    await user.click(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "New" }));
    expect(await screen.findByText("Looking for what is new.")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load what is new")).toBeNull();
    // The page asked the server again rather than showing the failure's leftovers.
    expect(calls.filter((c) => c.url.pathname === "/api/catchup").length).toBeGreaterThanOrEqual(2);
  });

  it("has the page's counts beside an article opened by its address", async () => {
    // A wide screen: the page's list stands beside the article.
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("min-width: 900px"), media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }));
    routes([8, 2]);
    const { container } = go("/i/1001?from=unread%7Cafter%3A100");
    await waitFor(() => expect(headings(container)).toEqual(["Alpha8 new", "Beta2 new"]));
    expect(screen.getByRole("button", { name: "5 more in Alpha" })).toBeInTheDocument();
    expect(world.seen).toEqual([]);
  });

  it("marks all only up to what the visit knew of when it opened", async () => {
    const { calls } = routes([3, 3], {
      "GET /api/items": (u) => {
        const n = u.searchParams.get("folder") === "10" ? 0 : u.searchParams.get("folder") === "11" ? 1 : -1;
        return json(pageOf(n < 0 ? [] : articlesOf(n, 3), null, "950")); // the sections loaded after the visit began
      },
    });
    const user = userEvent.setup();
    go("/l/new");
    await screen.findByText("Beta story 1");
    await user.click(screen.getByRole("button", { name: "List actions" }));
    await user.click(await screen.findByRole("menuitem", { name: /Mark all 6 as read/ }));
    await user.click(await screen.findByRole("button", { name: "Mark all 6 as read" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")).toBe(true));
    const body = JSON.parse(String(calls.find((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")!.init?.body));
    expect(body.max_id).toBe("900");
  });

  it("turns Mark all as read off when nothing is new", async () => {
    routes([0, 0]);
    const user = userEvent.setup();
    go("/l/new");
    await screen.findByText("Nothing new");
    await user.click(screen.getByRole("button", { name: "List actions" }));
    const item = await screen.findByRole("menuitem", { name: /Mark all as read/ });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(item).toHaveTextContent("Nothing unread here");
  });
});

describe("a visit adopted from an article's address", () => {
  const WIDE = (query: string) => ({ matches: query.includes("min-width: 900px"), media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false });
  /** The server's mark has moved on to 900; the link still names 100, which had three new articles in each folder. */
  const behindServer = (arrived = [3, 3]) =>
    routes(arrived, {
      "GET /api/catchup": (u) => {
        const groups = (u.searchParams.get("groups") ?? "").split(",").filter(Boolean);
        const mark = u.searchParams.get("mark") ?? "900";
        return json({ mark, as_of: "950", sections: groups.map((g) => ({ group: g, count: g.startsWith("d") && mark === "100" ? 3 : 0 })) });
      },
    });
  const asks = (calls: { url: URL }[]) => calls.filter((c) => c.url.pathname === "/api/catchup");

  it("asks for the counts after the link's mark, not the server's", async () => {
    vi.stubGlobal("matchMedia", WIDE);
    const { calls } = behindServer();
    const { container } = go("/i/1001?from=unread%7Cafter%3A100");
    await waitFor(() => expect(headings(container)).toEqual(["Alpha3 new", "Beta3 new"]));
    expect(asks(calls)[0]!.url.searchParams.get("mark")).toBe("100");
  });

  it("keeps Mark all as read on while the page lists the articles the link's mark leaves", async () => {
    vi.stubGlobal("matchMedia", WIDE);
    behindServer();
    const user = userEvent.setup();
    go("/i/1001?from=unread%7Cafter%3A100");
    await screen.findByText("Beta story 1");
    await user.click(screen.getByRole("button", { name: "List actions" }));
    const item = await screen.findByRole("menuitem", { name: /Mark all 6 as read/ });
    expect(item).not.toHaveAttribute("aria-disabled", "true");
  });

  it("is dropped when the reader goes to the page itself, which asks for the server's own mark", async () => {
    vi.stubGlobal("matchMedia", WIDE);
    const { calls } = behindServer();
    const user = userEvent.setup();
    const { container } = go("/i/1001?from=unread%7Cafter%3A100");
    await waitFor(() => expect(headings(container)).toEqual(["Alpha3 new", "Beta3 new"]));
    await user.click(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "New" }));
    await waitFor(() => expect(asks(calls)).toHaveLength(2));
    expect(asks(calls)[1]!.url.searchParams.has("mark")).toBe(false);
    // The page is the server's: nothing is new after its mark, and leaving it moves the mark on.
    expect(await screen.findByText("Nothing new")).toBeInTheDocument();
    await user.click(within(screen.getByRole("navigation", { name: "Show" })).getByRole("link", { name: "Unread" }));
    await waitFor(() => expect(world.seen).toEqual(["950"]));
  });

  it("lists previous and next from the link's mark on a narrow screen, before and after the visit is known", async () => {
    const { calls } = behindServer();
    go("/i/1001?from=unread%7Cafter%3A100");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(asks(calls)).toHaveLength(1));
    const afters = calls.filter((c) => c.url.pathname === "/api/items" && c.url.searchParams.has("after")).map((c) => c.url.searchParams.get("after"));
    expect(afters.length).toBeGreaterThan(0);
    expect(new Set(afters)).toEqual(new Set(["100"]));
  });
});

describe("helpers", () => {
  it("names the sections the server counts: each top-level folder, nested ones inside it", () => {
    const folders = [
      { id: "10", name: "A", parent_id: null },
      { id: "11", name: "B", parent_id: "10" },
      { id: "12", name: "C", parent_id: null },
    ];
    const feeds = [
      { id: "20", title: "x", folder_id: "11", unread: 1, starred_count: 0, is_archive: false },
      { id: "21", title: "y", folder_id: "12", unread: 1, starred_count: 0, is_archive: false },
    ];
    expect(catchupGroups(feeds, folders)).toEqual(["d10", "d12"]);
  });

  it("keeps the mark in the scope key, so a visit under another mark is another list", () => {
    const s = { view: "unread" as const, after: "123" };
    expect(scopeKey(s)).toBe("unread|after:123");
    expect(parseScopeKey("unread|after:123")).toEqual(s);
    expect(parseScopeKey("unread|after:abc")).toEqual({ view: "unread" });
    // The mark of an account that has never left the page is 0, and the page is still the New page.
    expect(parseScopeKey("unread|after:0")).toEqual({ view: "unread", after: "0" });
  });

  it("is within a visit on the page and on articles opened from it, not elsewhere", () => {
    const v = { id: 1, mark: "100", asOf: "900", counts: new Map() };
    expect(withinVisit(v, "/l/new", "")).toBe(true);
    expect(withinVisit(v, "/i/5", "?from=unread%7Cafter%3A100")).toBe(true);
    expect(withinVisit(v, "/i/5", "?from=unread%7Cafter%3A99")).toBe(false);
    expect(withinVisit(v, "/i/5", "?from=unread")).toBe(false);
    expect(withinVisit(v, "/l/unread", "")).toBe(false);
  });
});
