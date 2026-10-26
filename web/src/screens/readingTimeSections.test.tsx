import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { Card } from "@/api/types";
import { devicePrefsStore, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory, resetScrollReadForTests } from "./ListPane";
import { clearDigestPlaces } from "./digestSource";
import { updatePrefs } from "@/lib/prefs";

// The Digest cut by reading time as the list screen draws it: the unread list, a section for each length.

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;
const NAMES = ["Short", "Middle", "Long", "Unknown"];
const HEADINGS = ["Under 3 minutes", "3 to 7 minutes", "8 minutes and up", "Unknown length"];

/** Which of the four sections a request is for (-1 for a list that is not one of them). */
function sectionOf(u: URL): number {
  const p = u.searchParams;
  if (p.get("unknown_length") === "1") return 3;
  if (p.get("max_minutes") === "2") return 0;
  if (p.get("min_minutes") === "3" && p.get("max_minutes") === "7") return 1;
  if (p.get("min_minutes") === "8" && !p.has("max_minutes")) return 2;
  return -1;
}

/** Articles of section n: ids 1000 * (n + 1) + k, newest first. */
const articlesOf = (n: number, count: number): Card[] =>
  Array.from({ length: count }, (_, k) => card(1, { id: String(1000 * (n + 1) + k), title: `${NAMES[n]} story ${k + 1}`, published_at: BASE - k * 3600, sort_at: BASE - k * 3600 }));

function routes(counts: number[], extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": (u) => {
      const n = sectionOf(u);
      if (n < 0) return json(pageOf([]));
      const all = articlesOf(n, counts[n]!);
      const limit = Number(u.searchParams.get("limit") ?? 50);
      const from = Number(u.searchParams.get("cursor")?.slice(1) ?? 0);
      return json(pageOf(all.slice(from, from + limit), from + limit < all.length ? `c${from + limit}` : null, "500"));
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
const sectionCalls = (calls: { method: string; url: URL }[]) => calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items" && sectionOf(c.url) >= 0);
const headings = (root: ParentNode) => [...root.querySelectorAll("section[data-section] > h2")].map((h) => h.textContent);
const storyIds = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>("article[data-item-id]")].map((a) => a.dataset.itemId);
const bodies = (calls: { method: string; url: URL; init?: RequestInit }[]) =>
  calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read").map((c) => JSON.parse(String(c.init?.body)));

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
  updateDevicePrefs({ peekSeen: true, layout: "digest", digestSections: "length" });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Digest reading-time sections", () => {
  it("draws the four lengths in order, each asking the server for its own reading time", async () => {
    const { calls } = routes([5, 2, 4, 1]);
    const { container } = go("/l/unread");
    await screen.findByText("Short story 1");
    expect(headings(container)).toEqual(HEADINGS);
    const asked = sectionCalls(calls).map((c) => [c.url.searchParams.get("min_minutes"), c.url.searchParams.get("max_minutes"), c.url.searchParams.get("unknown_length")]);
    expect(asked).toEqual(expect.arrayContaining([[null, "2", null], ["3", "7", null], ["8", null, null], [null, null, "1"]]));
    expect(storyIds(container)).toEqual(["1000", "1001", "1002", "2000", "2001", "3000", "3001", "3002", "4000"]);
  });

  it("says how many more once a section has loaded whole, and drops one that has nothing", async () => {
    routes([8, 0, 3, 0]);
    const { container } = go("/l/unread");
    await screen.findByText("Short story 1");
    // Unknown until loaded, exact after: the bootstrap has no count for a length.
    expect(headings(container)).toEqual(["Under 3 minutes", "8 minutes and up"]);
    expect(screen.getByRole("button", { name: "5 more, Under 3 minutes" })).toHaveTextContent("5 more");
    expect(screen.getAllByRole("button", { name: /^(\d+ more|More), / })).toHaveLength(1); // the 8 minute section has exactly three: no button
  });

  it("says More with no number while a section has pages left to load", async () => {
    routes([45, 0, 0, 0]);
    go("/l/unread");
    await screen.findByText("Short story 1");
    const more = await screen.findByRole("button", { name: "More, Under 3 minutes" });
    expect(more).toHaveTextContent(/^More$/);
  });

  it("opens a section out in place", async () => {
    routes([8, 0, 0, 0]);
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Short story 1");
    await user.click(screen.getByRole("button", { name: "5 more, Under 3 minutes" }));
    await waitFor(() => expect(storyIds(container)).toHaveLength(8));
    expect(screen.queryByRole("button", { name: /^(\d+ more|More), / })).toBeNull();
  });

  it("scopes each section to the folder or feed the list is", async () => {
    const { calls } = routes([2, 2, 2, 2]);
    go("/l/unread?folder=1");
    await screen.findByText("Short story 1");
    for (const c of sectionCalls(calls)) expect(c.url.searchParams.get("folder")).toBe("1");
  });

  it("falls back to folders in All, Starred and Muted, and when the list is already filtered by length", async () => {
    const { calls } = routes([2, 2, 2, 2], { "GET /api/items": () => json(pageOf(articlesOf(0, 2))) });
    for (const path of ["/l/all", "/l/starred", "/l/unread?len=short"]) {
      const { container, unmount } = go(path);
      await screen.findByText("Short story 1");
      expect(container.querySelector("[data-digest]")).not.toBeNull();
      expect(headings(container)).not.toContain("Under 3 minutes");
      unmount();
    }
    expect(calls.some((c) => c.url.searchParams.get("unknown_length"))).toBe(false);
  });

  it("is a plain list in a search", async () => {
    routes([2, 2, 2, 2], { "GET /api/saved-searches": () => json({ saved_searches: [] }), "GET /api/items": () => json(pageOf(articlesOf(0, 3))) });
    const { container } = go("/search?q=story");
    await screen.findByText("Best matches first");
    expect(container.querySelector("[data-digest]")).toBeNull();
    expect(container.querySelectorAll("[data-index]").length).toBeGreaterThan(1);
  });

  it("shows text only rows when Images is off, and the rows per section setting", async () => {
    updateDevicePrefs({ digestImages: false, digestRows: 5 });
    routes([8, 0, 0, 0], { "GET /api/items": (u) => json(pageOf(sectionOf(u) === 0 ? articlesOf(0, 8).map((c) => ({ ...c, image: "/img/x.jpg" })) : [], null, "500")) });
    const { container } = go("/l/unread");
    await screen.findByText("Short story 1");
    expect(storyIds(container)).toHaveLength(5);
    expect(container.querySelector("img")).toBeNull();
  });

  it("passes axe", async () => {
    routes([4, 4, 4, 4]);
    const { container } = go("/l/unread");
    await screen.findByText("Unknown story 1");
    expect(await axe(container)).toHaveNoViolations();
  });
});

describe("Digest reading-time marking", () => {
  it("Mark below acts within the article's section: its reading time and its bound", async () => {
    const { calls } = routes([5, 5, 5, 5], { "POST /api/items/mark-read": () => json({ changed: ["2001", "2002"], restored: [] }) });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Middle story 1");
    fireEvent.contextMenu(within(container.querySelector<HTMLElement>('[data-item-id="2000"]')!).getByRole("link"), { button: 2 });
    await user.click(await screen.findByRole("menuitem", { name: "Mark below as read" }));
    await waitFor(() => expect(bodies(calls)).toHaveLength(1));
    const b = bodies(calls)[0];
    expect(b.scope).toMatchObject({ all: true, view: "unread", min_minutes: 3, max_minutes: 7 });
    expect(b.bound).toMatchObject({ side: "below", anchor: { id: "2000" } });
    expect(b.max_id).toBe("500");
    expect(container.querySelector('[data-item-id="1002"]')).not.toHaveAttribute("data-read");
  });

  it("Mark above in the unknown length sends unknown_length, never a range", async () => {
    const { calls } = routes([5, 5, 5, 5], { "POST /api/items/mark-read": () => json({ changed: ["4000"], restored: [] }) });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Unknown story 1");
    fireEvent.contextMenu(within(container.querySelector<HTMLElement>('[data-item-id="4002"]')!).getByRole("link"), { button: 2 });
    await user.click(await screen.findByRole("menuitem", { name: "Mark above as read" }));
    await waitFor(() => expect(bodies(calls)).toHaveLength(1));
    const scope = bodies(calls)[0].scope;
    expect(scope).toMatchObject({ all: true, view: "unread", unknown_length: true });
    expect(scope.min_minutes).toBeUndefined();
    expect(scope.max_minutes).toBeUndefined();
  });

  it("Mark all marks the whole list once, not a request for each length", async () => {
    const { calls } = routes([3, 3, 3, 3], { "POST /api/items/mark-read": () => json({ changed: [0, 1, 2, 3].flatMap((n) => articlesOf(n, 3).map((c) => c.id)), restored: [] }) });
    const user = userEvent.setup();
    const { container } = go("/l/unread");
    await screen.findByText("Unknown story 1");
    await user.click(screen.getByRole("button", { name: "List actions" }));
    await user.click(await screen.findByRole("menuitem", { name: /^Mark (all|everything)/ }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: /^Mark (all|everything)/ }));
    await waitFor(() => expect(bodies(calls).filter((b) => b.read)).toHaveLength(1));
    expect(bodies(calls)[0].scope).toEqual({ all: true, view: "unread" });
    await waitFor(() => expect(container.querySelectorAll("article[data-item-id]:not([data-read])")).toHaveLength(0));
  });
});

describe("Digest reading-time menu", () => {
  it("offers Reading time as a Sections choice on Unread, beside Images and Rows per section", async () => {
    routes([3, 3, 3, 3]);
    const user = userEvent.setup();
    go("/l/unread");
    await screen.findByText("Short story 1");
    await user.click(screen.getByRole("button", { name: /^List options, Digest layout/ }));
    const menu = await screen.findByRole("menu");
    const sections = within(menu).getByRole("group", { name: "Sections" });
    expect(within(sections).getByRole("menuitemradio", { name: "Reading time" })).toBeChecked();
    expect(within(sections).getByRole("menuitemradio", { name: "Reading time" })).not.toHaveAttribute("aria-disabled", "true");
    for (const name of ["Images", "Rows per section"]) expect(within(menu).getByRole("group", { name })).toBeInTheDocument();
    await user.click(within(sections).getByRole("menuitemradio", { name: "Folders" }));
    expect(devicePrefsStore.get().digestSections).toBe("folder");
  });

  it("greys Reading time with the reason where the list is not Unread, and shows Folders", async () => {
    routes([3, 3, 3, 3], { "GET /api/items": () => json(pageOf(articlesOf(0, 3))) });
    const user = userEvent.setup();
    go("/l/all");
    await screen.findByText("Short story 1");
    await user.click(screen.getByRole("button", { name: /^List options, Digest layout/ }));
    const sections = within(await screen.findByRole("menu")).getByRole("group", { name: "Sections" });
    const reading = within(sections).getByRole("menuitemradio", { name: /^Reading time/ });
    expect(reading).toHaveAttribute("aria-disabled", "true");
    expect(reading).toHaveTextContent("Reading time groups Unread only.");
    expect(within(sections).getByRole("menuitemradio", { name: "Folders" })).toBeChecked();
    // The choice is kept for Unread.
    expect(devicePrefsStore.get().digestSections).toBe("length");
  });
});
