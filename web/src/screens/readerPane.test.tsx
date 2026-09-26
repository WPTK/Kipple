import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { updatePrefs } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;
const items = () => [1, 2, 3, 4, 5].map((n) => card(n, { published_at: BASE - n * 3600, sort_at: BASE - n * 3600 }));

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  const open = (n: number) => () => json({ session_key: "k", item: detail(n, { read: true }) });
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(items(), null, "2000")),
    "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
    "POST /api/refresh": () => json({ run_id: "r1", total: 1 }, 202),
    "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }),
    ...Object.fromEntries([1, 2, 3, 4, 5].map((n) => [`GET /api/items/${1000 + n}`, () => json(detail(n))])),
    ...Object.fromEntries([1, 2, 3, 4, 5].map((n) => [`POST /api/items/${1000 + n}/open`, open(n)])),
    ...extra,
  });
}
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
function media(...on: string[]) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: on.some((q) => query.includes(q)),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}
const rowOf = (n: number) => document.querySelector(`[data-item-id="${1000 + n}"]`);
const rowLink = (n: number) => rowOf(n)?.querySelector("a") as HTMLElement;
const bodyOf = (call: { init?: RequestInit } | undefined) => JSON.parse(String(call?.init?.body));

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updatePrefs({ shortcuts: true });
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
  media("min-width: 900px");
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("reader pane: one persistent list", () => {
  it("opening an article does not remount the list: rows swiped away stay away", async () => {
    routes({ "POST /api/items/mark-read": () => json({ changed: ["1002", "1003", "1004", "1005"], restored: [], count: 4, undoable: true }) });
    go("/l/unread");
    await screen.findByText("Article number 5");
    const user = userEvent.setup();
    await user.keyboard("j"); // select row 1, opens it in the pane (replace)
    await screen.findByTestId("article-body");
    await user.keyboard("}"); // mark below as read: rows 2-5 leave the Unread list
    await waitFor(() => expect(rowOf(5)).toBeNull());
    // Go to the list route and back into an article: the list must not come back to life.
    await user.click(rowLink(1));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1001"));
    expect(rowOf(5)).toBeNull();
    expect(rowOf(2)).toBeNull();
  });

  it("keeps the same list element across the list and article routes", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const before = screen.getByTestId("list-scroll");
    await userEvent.setup().click(screen.getByText("Article number 2"));
    await screen.findByTestId("article-body");
    expect(screen.getByTestId("list-scroll")).toBe(before);
  });

  it("hidden rows survive leaving the list on a phone", async () => {
    media();
    routes({ "POST /api/items/mark-read": () => json({ changed: ["1002", "1003", "1004", "1005"], restored: [], count: 4, undoable: true }) });
    go("/l/unread");
    await screen.findByText("Article number 5");
    const user = userEvent.setup();
    await user.keyboard("j}");
    await waitFor(() => expect(screen.queryByText("Article number 5")).toBeNull());
    await user.click(screen.getByText("Article number 1"));
    await screen.findByTestId("article-body");
    await user.click(screen.getByRole("button", { name: "Back to list" }));
    await screen.findByText("Article number 1");
    expect(screen.queryByText("Article number 5")).toBeNull();
  });
});

describe("reader pane: keys", () => {
  it("list keys stay active with an article open (Shift+A marks the list read)", async () => {
    const { calls } = routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await screen.findByText("Article number 3");
    await userEvent.setup().keyboard("{Shift>}A{/Shift}");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read" && bodyOf(c).scope)).toBe(true));
  });

  it("j moves exactly one article (list and article keys do not both fire)", async () => {
    const { calls } = routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await screen.findByText("Article number 3");
    await userEvent.setup().keyboard("j");
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
    await new Promise((r) => setTimeout(r, 100));
    expect(window.location.pathname).toBe("/i/1002");
    expect(calls.filter((c) => c.url.pathname === "/api/items/1003/open")).toHaveLength(0);
  });

  it("Esc returns focus to the list row of the open article", async () => {
    routes();
    go("/i/1002?from=unread");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(rowOf(2)).not.toBeNull());
    await userEvent.setup().keyboard("{Escape}");
    await waitFor(() => expect(document.activeElement?.closest('[data-item-id="1002"]')).not.toBeNull());
    expect(window.location.pathname).toBe("/i/1002");
  });

  it("u does the same", async () => {
    routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(rowOf(1)).not.toBeNull());
    await userEvent.setup().keyboard("u");
    await waitFor(() => expect(document.activeElement?.closest('[data-item-id="1001"]')).not.toBeNull());
  });

  it("f still toggles full text in the pane", async () => {
    const { calls } = routes({ "POST /api/items/1001/fulltext": () => json({ mode: 1, effective: 1, status: "ok", content_html: "<p>x</p>", word_count: 1, error: null }) });
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await userEvent.setup().keyboard("f");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/1001/fulltext")).toBe(true));
  });
});

describe("reader pane: sort order", () => {
  it("the Oldest first toggle re-sorts the list while an article is open", async () => {
    const { calls } = routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await screen.findByText("Article number 2");
    await userEvent.setup().click(screen.getByRole("button", { name: "Oldest first" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items" && c.url.searchParams.get("order") === "oldest")).toBe(true));
    expect(screen.getByRole("button", { name: "Oldest first" })).toHaveAttribute("aria-pressed", "true");
  });

  it("an article opened from a newest-first list follows the device preference when it is oldest", async () => {
    updateDevicePrefs({ order: "oldest" });
    const { calls } = routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items" && c.url.searchParams.get("order") === "oldest")).toBe(true));
  });
});

describe("refresh key", () => {
  it("r refreshes once from the list", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    await userEvent.setup().keyboard("r");
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/refresh")).toHaveLength(1));
  });

  it("r refreshes from Settings too, as the shortcut overlay says (Everywhere)", async () => {
    const { calls } = routes({ "GET /api/settings": () => json({ settings: [], values: {} }) });
    go("/settings");
    await screen.findByRole("heading", { name: "Settings" });
    await userEvent.setup().keyboard("r");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/refresh")).toBe(true));
  });
});

describe("CapsLock", () => {
  it("g a with CapsLock on goes to All and does not mark the list read", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    // CapsLock: key is upper case, shiftKey is false.
    fireEvent.keyDown(window, { key: "G", shiftKey: false });
    fireEvent.keyDown(window, { key: "A", shiftKey: false });
    await waitFor(() => expect(window.location.pathname).toBe("/l/all"));
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
  });
});
