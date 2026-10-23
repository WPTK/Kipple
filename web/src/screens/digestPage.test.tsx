import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { Bootstrap, Card, Feed } from "@/api/types";
import { devicePrefsStore, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory, resetScrollReadForTests } from "./ListPane";
import { clearDigestPlaces, FIRST_SECTIONS, STEP_SECTIONS } from "./digestSource";
import { updatePrefs } from "@/lib/prefs";

// The Digest as the list screen draws it: a section for each folder, loaded on its own, opened out in place.

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;
const NAMES = ["Alpha", "Beta", "Gamma", "Delta", "Epsilon", "Zeta", "Eta", "Theta", "Iota", "Kappa"];

/** A library of folders, each holding one feed with `unread[i]` unread articles (and `pace[i]` new ones a day). */
function library(unread: number[], pace: (number | null)[] = []): Bootstrap {
  const folders = unread.map((n, i) => ({ id: String(10 + i), name: NAMES[i]!, position: i, is_default: i === 0, unread: n }));
  const feeds: Feed[] = unread.map((n, i) => ({
    ...bootstrap.feeds![0]!,
    id: String(20 + i),
    folder_id: String(10 + i),
    title: `${NAMES[i]} Feed`,
    unread: n,
    pace: pace[i] ?? null,
  }));
  return { ...bootstrap, folders, feeds, counts: { unread: unread.reduce((a, b) => a + b, 0), starred: 0 } };
}

/** The unread articles of folder `n` (0-based): ids 1000 * (n + 1) + k, newest first. */
const articlesOf = (n: number, count: number, over: (k: number) => Partial<Card> = () => ({})): Card[] =>
  Array.from({ length: count }, (_, k) =>
    card(1, { id: String(1000 * (n + 1) + k), title: `${NAMES[n]} story ${k + 1}`, feed_id: String(20 + n), source: `${NAMES[n]} Feed`, published_at: BASE - k * 3600, sort_at: BASE - k * 3600, ...over(k) }),
  );

interface Opts {
  boot: Bootstrap;
  counts: number[];
  over?: (n: number, k: number) => Partial<Card>;
  asOf?: (n: number) => string | undefined;
  fail?: Set<number>;
  extra?: Parameters<typeof mockFetch>[0];
}
function routes({ boot, counts, over = () => ({}), asOf = () => "500", fail = new Set(), extra = {} }: Opts) {
  const folderIndex = (u: URL) => boot.folders.findIndex((f) => f.id === u.searchParams.get("folder") || boot.feeds!.find((x) => x.id === u.searchParams.get("feed"))?.folder_id === f.id);
  return mockFetch({
    "GET /api/bootstrap": () => json(boot),
    "GET /api/items": (u) => {
      const n = folderIndex(u);
      if (n < 0) return json(pageOf([])); // the app's own prefetch for offline reading, not a section
      if (fail.has(n)) return json({ error: "gone" }, 404);
      const all = articlesOf(n, counts[n]!, (k) => over(n, k));
      const limit = Number(u.searchParams.get("limit") ?? 50);
      const from = Number(u.searchParams.get("cursor")?.slice(1) ?? 0);
      const items = all.slice(from, from + limit);
      return json(pageOf(items, from + limit < all.length ? `c${from + limit}` : null, asOf(n)));
    },
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids ?? [], restored: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
/** The requests for a section's list (not the app's own prefetch for offline reading). */
const itemCalls = (calls: { method: string; url: URL }[]) => calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items" && (c.url.searchParams.has("folder") || c.url.searchParams.has("feed")));
const headings = (root: ParentNode) => [...root.querySelectorAll("section[data-section] > h2")].map((h) => h.textContent);
const storyIds = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>("article[data-item-id]")].map((a) => a.dataset.itemId);
const section = (root: ParentNode, name: string) => [...root.querySelectorAll<HTMLElement>("section[data-section]")].find((s) => s.querySelector("h2")?.textContent === name)!;

beforeEach(() => {
  clearToasts();
  clearListMemory();
  clearDigestPlaces();
  resetScrollReadForTests();
  updatePrefs({ markReadOnScroll: false });
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updateDevicePrefs({ peekSeen: true, layout: "digest" });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Digest sections", () => {
  it("draws a section for each folder in sidebar order, each with its first rows and how many more", async () => {
    routes({ boot: library([8, 2, 5]), counts: [8, 2, 5] });
    const { container } = go("/l/unread");
    await screen.findByText("Alpha story 1");
    expect(headings(container)).toEqual(["Alpha", "Beta", "Gamma"]);
    expect(storyIds(container)).toEqual(["1000", "1001", "1002", "2000", "2001", "3000", "3001", "3002"]);
    expect(screen.getByRole("button", { name: "5 more in Alpha" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /more in Beta/ })).toBeNull(); // Beta has only two
    expect(screen.getByRole("button", { name: "2 more in Gamma" })).toBeInTheDocument();
    // A title first, then its source and time; no excerpt.
    const row = container.querySelector<HTMLElement>('[data-item-id="1000"]')!;
    expect(row.querySelector("h3")).toHaveTextContent("Alpha story 1");
    expect(within(row).queryByText(/Excerpt/)).toBeNull();
    expect(container.querySelector("[data-index]")).toBeNull();
  });

  it("shows the rows per section setting", async () => {
    updateDevicePrefs({ digestRows: 5 });
    routes({ boot: library([8]), counts: [8] });
    const { container } = go("/l/unread");
    await screen.findByText("Alpha story 1");
    expect(storyIds(container)).toHaveLength(5);
    expect(screen.getByRole("button", { name: "3 more in Alpha" })).toBeInTheDocument();
  });

  it("opens a section out in place, a batch for each press, loading the next page when it needs one", async () => {
    const { calls } = routes({ boot: library([45, 2]), counts: [45, 2] });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Alpha story 1");
    const beta = storyIds(section(container, "Beta"));
    await user.click(screen.getByRole("button", { name: "42 more in Alpha" }));
    await waitFor(() => expect(storyIds(section(container, "Alpha"))).toHaveLength(23));
    // The first request returns 20, so the 23 shown needed a second page.
    expect(itemCalls(calls).filter((c) => c.url.searchParams.get("folder") === "10").map((c) => c.url.searchParams.get("cursor"))).toEqual([null, "c20"]);
    expect(storyIds(section(container, "Beta"))).toEqual(beta); // the next section moved down, its own rows did not change
    await user.click(await screen.findByRole("button", { name: "22 more in Alpha" }));
    await waitFor(() => expect(storyIds(section(container, "Alpha"))).toHaveLength(43));
    await user.click(await screen.findByRole("button", { name: "2 more in Alpha" }));
    await waitFor(() => expect(storyIds(section(container, "Alpha"))).toHaveLength(45));
    expect(screen.queryByRole("button", { name: /more in Alpha/ })).toBeNull();
  });

  it("asks for the first sections at open and four more each time the end of the page is reached", async () => {
    const counts = Array.from({ length: 10 }, () => 3);
    const { calls } = routes({ boot: library(counts), counts });
    const { container } = go("/l/unread");
    await waitFor(() => expect(headings(container)).toHaveLength(counts.length));
    const asked = itemCalls(calls).map((c) => c.url.searchParams.get("folder"));
    expect(asked).toHaveLength(counts.length);
    // In sidebar order: the first FIRST_SECTIONS, then STEP_SECTIONS at a time.
    expect(asked.slice(0, FIRST_SECTIONS)).toEqual(Array.from({ length: FIRST_SECTIONS }, (_, i) => String(10 + i)));
    expect(asked.slice(FIRST_SECTIONS, FIRST_SECTIONS + STEP_SECTIONS)).toEqual(Array.from({ length: STEP_SECTIONS }, (_, i) => String(10 + FIRST_SECTIONS + i)));
  });

  it("leaves out a folder with nothing unread, and keeps the order when the counts move", async () => {
    routes({ boot: library([2, 0, 3]), counts: [2, 0, 3] });
    const { container } = go("/l/unread");
    await screen.findByText("Alpha story 1");
    expect(headings(container)).toEqual(["Alpha", "Gamma"]);
  });

  it("Quiet Feeds First puts the folders that get fewer articles a day first, those with no pace last", async () => {
    updateDevicePrefs({ digestOrder: "quiet" });
    routes({ boot: library([3, 3, 3, 3], [9, 1, null, 1]), counts: [3, 3, 3, 3] });
    const { container } = go("/l/unread");
    await screen.findByText("Beta story 1");
    expect(headings(container)).toEqual(["Beta", "Delta", "Alpha", "Gamma"]);
  });

  it("makes a section of each feed when Sections is Feeds", async () => {
    updateDevicePrefs({ digestSections: "feed" });
    const { calls } = routes({ boot: library([3, 3]), counts: [3, 3] });
    const { container } = go("/l/unread");
    await screen.findByText("Beta story 1");
    expect(headings(container)).toEqual(["Alpha Feed", "Beta Feed"]);
    expect(itemCalls(calls).map((c) => c.url.searchParams.get("feed"))).toEqual(["20", "21"]);
  });

  it("shows a picture on the right of a row that has one when Images is on", async () => {
    routes({ boot: library([3]), counts: [3], over: (_n, k) => ({ image: k === 0 ? "/img/x.jpg" : null }) });
    const { container } = go("/l/unread");
    await screen.findByText("Alpha story 1");
    expect(container.querySelectorAll("img")).toHaveLength(1);
    expect(container.querySelector('[data-item-id="1000"] img')).not.toBeNull();
  });

  it("shows text only rows when Images is off", async () => {
    updateDevicePrefs({ digestImages: false });
    routes({ boot: library([3]), counts: [3], over: () => ({ image: "/img/x.jpg" }) });
    const { container } = go("/l/unread");
    await screen.findByText("Alpha story 1");
    expect(container.querySelectorAll("img")).toHaveLength(0);
  });

  it("a section that fails says so and asks again with Retry, while the others show", async () => {
    const fail = new Set([1]);
    routes({ boot: library([3, 3, 3]), counts: [3, 3, 3], fail });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Gamma story 1");
    expect(within(section(container, "Beta")).getByRole("alert")).toHaveTextContent("Couldn't load Beta.");
    expect(storyIds(section(container, "Alpha"))).toHaveLength(3);
    fail.clear();
    await user.click(within(section(container, "Beta")).getByRole("button", { name: "Retry" }));
    await screen.findByText("Beta story 1");
    expect(within(section(container, "Beta")).queryByRole("alert")).toBeNull();
    expect(headings(container)).toEqual(["Alpha", "Beta", "Gamma"]);
  });

  it("a search shows rows, not sections", async () => {
    routes({ boot: library([3]), counts: [3], extra: { "GET /api/saved-searches": () => json({ saved_searches: [] }), "GET /api/items": () => json(pageOf(articlesOf(0, 3))) } });
    const { container } = go("/search?q=story");
    await screen.findByText("Best matches first");
    expect(container.querySelector("[data-digest]")).toBeNull();
    expect(container.querySelectorAll("[data-index]").length).toBeGreaterThan(1);
  });

  it("passes axe", async () => {
    routes({ boot: library([8, 2]), counts: [8, 2], over: (_n, k) => ({ image: k === 0 ? "/img/x.jpg" : null }) });
    const { container } = go("/l/unread");
    await screen.findByText("Alpha story 1");
    expect(await axe(container)).toHaveNoViolations();
  });
});

describe("Digest marking", () => {
  const bodies = (calls: { method: string; url: URL; init?: RequestInit }[]) =>
    calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read").map((c) => JSON.parse(String(c.init?.body)));

  it("Mark below acts within the article's section, bounded by that section's as_of", async () => {
    const { calls } = routes({ boot: library([5, 5]), counts: [5, 5], asOf: (n) => String(900 + n), extra: { "POST /api/items/mark-read": () => json({ changed: ["2001", "2002", "2003", "2004"], restored: [] }) } });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Beta story 1");
    fireEvent.contextMenu(within(container.querySelector<HTMLElement>('[data-item-id="2000"]')!).getByRole("link"), { button: 2 });
    await user.click(await screen.findByRole("menuitem", { name: "Mark below as read" }));
    await waitFor(() => expect(bodies(calls)).toHaveLength(1));
    const b = bodies(calls)[0];
    expect(b.scope).toMatchObject({ folder_id: "11", view: "unread" });
    expect(b.bound).toMatchObject({ side: "below", anchor: { id: "2000" } });
    expect(b.max_id).toBe("901");
    // Only that section's loaded articles below it are shown read; Alpha's are not touched.
    await waitFor(() => expect(container.querySelector('[data-item-id="2002"]')).toHaveAttribute("data-read"));
    expect(container.querySelector('[data-item-id="1002"]')).not.toHaveAttribute("data-read");
    expect(container.querySelector('[data-item-id="2000"]')).not.toHaveAttribute("data-read");
  });

  it("Mark above reaches the articles above it in its section only", async () => {
    const { calls } = routes({ boot: library([5, 5]), counts: [5, 5], extra: { "POST /api/items/mark-read": () => json({ changed: ["2000", "2001"], restored: [] }) } });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Beta story 1");
    fireEvent.contextMenu(within(container.querySelector<HTMLElement>('[data-item-id="2002"]')!).getByRole("link"), { button: 2 });
    await user.click(await screen.findByRole("menuitem", { name: "Mark above as read" }));
    await waitFor(() => expect(bodies(calls)).toHaveLength(1));
    expect(bodies(calls)[0].bound).toMatchObject({ side: "above", anchor: { id: "2002" } });
    await waitFor(() => expect(container.querySelector('[data-item-id="2000"]')).toHaveAttribute("data-read"));
    expect(container.querySelector('[data-item-id="1000"]')).not.toHaveAttribute("data-read");
  });

  it("Mark all is bounded by the oldest as_of of the sections", async () => {
    const { calls } = routes({ boot: library([3, 3, 3]), counts: [3, 3, 3], asOf: (n) => ["700", "650", "800"][n] });
    const user = userEvent.setup();
    go("/l/unread");
    await screen.findByText("Gamma story 1");
    await user.click(screen.getByRole("button", { name: "List actions" }));
    await user.click(await screen.findByRole("menuitem", { name: /Mark all as read/ }));
    await waitFor(() => expect(bodies(calls)).toHaveLength(1));
    expect(bodies(calls)[0].max_id).toBe("650");
  });
});

describe("Digest keys", () => {
  it("the page is one Tab stop, and the arrow keys visit the articles and the More buttons in the order drawn", async () => {
    routes({ boot: library([6, 4]), counts: [6, 4] });
    const { container } = go("/l/unread");
    await screen.findByText("Beta story 1");
    const stops = [...container.querySelectorAll<HTMLElement>("article a")].filter((a) => a.tabIndex !== -1);
    expect(stops).toHaveLength(1);
    expect(stops[0]).toHaveTextContent("Alpha story 1");
    expect(container.querySelectorAll("button[tabindex='-1']").length).toBeGreaterThan(0); // the More buttons are not Tab stops
    const user = userEvent.setup();
    const link = (name: string) => screen.getByRole("link", { name: new RegExp(`^${name}`) });
    link("Alpha story 3").focus();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(screen.getByRole("button", { name: "3 more in Alpha" })).toHaveFocus());
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(link("Beta story 1")).toHaveFocus());
    await user.keyboard("{ArrowUp}");
    await waitFor(() => expect(screen.getByRole("button", { name: "3 more in Alpha" })).toHaveFocus());
    await user.keyboard("{ArrowUp}");
    await waitFor(() => expect(link("Alpha story 3")).toHaveFocus());
    // Enter on the button opens the section out, and focus stays on the button.
    await user.keyboard("{ArrowDown}");
    const more = await screen.findByRole("button", { name: "3 more in Alpha" });
    await waitFor(() => expect(more).toHaveFocus());
    await user.keyboard("{Enter}");
    await waitFor(() => expect(storyIds(section(container, "Alpha"))).toHaveLength(6));
  });
});

describe("Digest article order", () => {
  it("Next and Previous follow the page: the sections in order, each article it shows", async () => {
    const boot = library([5, 2]);
    routes({
      boot,
      counts: [5, 2],
      extra: {
        "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 7 }),
        ...Object.fromEntries(
          [0, 1].flatMap((n) =>
            articlesOf(n, 5).flatMap((c) => [
              [`GET /api/items/${c.id}`, () => json(detail(1, { id: c.id, title: c.title }))],
              [`POST /api/items/${c.id}/open`, () => json({ session_key: "k", item: detail(1, { id: c.id, read: true }) })],
            ]),
          ),
        ),
      },
    });
    const user = userEvent.setup();
    // Opened from the list on a phone: the Digest shows Alpha's first three, then Beta's two.
    go("/l/unread");
    await screen.findByText("Beta story 1");
    await user.click(screen.getByRole("link", { name: /^Alpha story 3/ }));
    await screen.findByTestId("article-body");
    expect(window.location.pathname).toBe("/i/1002");
    await user.click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/2000")); // not Alpha's fourth: it is not on the page
    await user.click(screen.getByRole("button", { name: "Previous article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
  });
});

describe("Digest settings in the list options menu", () => {
  it("offers Order, Sections, Images and Rows per section for this device, and a choice changes the page", async () => {
    routes({ boot: library([3, 3], [5, 1]), counts: [3, 3] });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Beta story 1");
    expect(headings(container)).toEqual(["Alpha", "Beta"]);
    await user.click(screen.getByRole("button", { name: /^List options, Digest layout/ }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByText("Digest, for this device")).toBeInTheDocument();
    for (const name of ["Order of sections", "Sections", "Images", "Rows per section"]) expect(within(menu).getByRole("group", { name })).toBeInTheDocument();
    await user.click(within(menu).getByRole("menuitemradio", { name: /^Quiet Feeds First/ }));
    await waitFor(() => expect(headings(container)).toEqual(["Beta", "Alpha"]));
    await user.click(screen.getByRole("button", { name: /^List options, Digest layout/ }));
    await user.click(await screen.findByRole("menuitemradio", { name: "5" }));
    expect(devicePrefsStore.get().digestRows).toBe(5);
  });

  it("is not in the menu of another layout", async () => {
    updateDevicePrefs({ layout: "compact" });
    routes({ boot: library([3]), counts: [3], extra: { "GET /api/items": () => json(pageOf(articlesOf(0, 3))) } });
    const user = userEvent.setup();
    go("/l/unread");
    await screen.findByText("Alpha story 1");
    await user.click(screen.getByRole("button", { name: /^List options, Compact layout/ }));
    expect(screen.queryByText("Digest, for this device")).toBeNull();
  });
});
