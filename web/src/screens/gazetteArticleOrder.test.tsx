import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { planGazette, readingOrder } from "@/layouts/gazettePlan";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { wide } from "@/test/media";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

// An open article's previous and next in the Gazette follow the page's printed order (#315), on a phone (no list is
// mounted) and beside the list.

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;
const at = (n: number) => ({ published_at: BASE - n * 3600, sort_at: BASE - n * 3600 });
const many = (from: number, n: number) => Array.from({ length: n }, (_, i) => card(from + i, { image: (from + i) % 2 === 0 ? "/img/x.jpg" : null, ...at(from + i) }));
const newestFirst = (cards: { id: string }[]) => cards.map((c) => c.id);
const printed = (cards: ReturnType<typeof many>, more = false) =>
  readingOrder(planGazette({ articles: cards, feeds: bootstrap.feeds ?? [], folders: bootstrap.folders ?? [], favorites: [], scope: {}, screen: "wide", more }));

function routes(first: ReturnType<typeof many>, second: ReturnType<typeof many> = []) {
  const all = [...first, ...second];
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": (u) => json(u.searchParams.get("cursor") ? pageOf(second) : pageOf(first, second.length ? "c2" : null)),
    "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
    "POST /api/items/mark-read": () => json({ changed: [], restored: [] }),
    ...Object.fromEntries(all.map((c, i) => [`GET /api/items/${c.id}`, () => json(detail(i + 1, { id: c.id, title: c.title }))])),
    ...Object.fromEntries(all.map((c, i) => [`POST /api/items/${c.id}/open`, () => json({ session_key: "k", item: detail(i + 1, { id: c.id, read: true }) })])),
  });
}
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
const path = () => window.location.pathname;

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updateDevicePrefs({ peekSeen: true, layout: "gazette" });
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Gazette article previous and next", () => {
  it("steps through the stories in the order the list prints them", async () => {
    wide();
    const cards = many(1, 8);
    routes(cards);
    go("/l/unread");
    await screen.findByRole("heading", { name: "The Gazette" });
    const dom = [...document.querySelectorAll<HTMLElement>("article[data-item-id]")].map((a) => a.dataset.itemId);
    expect(dom).toEqual(printed(cards));
    expect(dom).not.toEqual(newestFirst(cards)); // a busy day moves the photo row ahead of the stories
  });

  it("on a phone, with no list mounted, Next and Previous visit the stories in printed order", async () => {
    const cards = many(1, 8);
    const order = printed(cards);
    routes(cards);
    // The list is in the cache a phone keeps while an article is open: it is loaded first, then the article is opened.
    go(`/i/${order[0]}?from=unread`);
    const user = userEvent.setup();
    await screen.findByTestId("article-body");
    expect(document.querySelector("[data-gazette]")).toBeNull();
    const seen = [path()];
    for (let i = 1; i < order.length; i++) {
      await user.click(screen.getByRole("button", { name: "Next article" }));
      await waitFor(() => expect(path()).not.toBe(seen[seen.length - 1]));
      seen.push(path());
    }
    expect(seen).toEqual(order.map((id) => `/i/${id}`));
    for (let i = order.length - 2; i >= 0; i--) {
      await user.click(screen.getByRole("button", { name: "Previous article" }));
      await waitFor(() => expect(path()).toBe(`/i/${order[i]}`));
    }
    expect(screen.getByRole("button", { name: "Previous article" })).toHaveAttribute("aria-disabled", "true");
  });

  it("on a wide screen, Next beside the list follows the printed order too", async () => {
    wide();
    const cards = many(1, 8);
    const order = printed(cards);
    routes(cards);
    go(`/i/${order[0]}?from=unread`);
    const user = userEvent.setup();
    await screen.findByTestId("article-body");
    await user.click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(path()).toBe(`/i/${order[1]}`));
  });

  it("past the last story laid out, Next loads more and goes on in the printed order", async () => {
    const first = many(1, 100);
    const second = many(101, 30);
    const before = printed(first, true);
    const after = printed([...first, ...second]);
    expect(after.slice(0, before.length)).toEqual(before); // pages never reflow
    expect(after.length).toBeGreaterThan(before.length);
    routes(first, second);
    const last = before[before.length - 1]!;
    go(`/i/${last}?from=unread`);
    const user = userEvent.setup();
    await screen.findByTestId("article-body");
    await user.click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(path()).toBe(`/i/${after[before.length]}`));
  });
});
