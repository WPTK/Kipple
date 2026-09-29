import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { onlineManager } from "@tanstack/react-query";
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

describe("reader pane: per-article state", () => {
  it("a full-text request still running for one article does not disable the button on the next", async () => {
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    routes({
      "POST /api/items/1001/fulltext": async () => {
        await held;
        return json({ status: "skipped", mode: 1, effective: 0 });
      },
    });
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const user = userEvent.setup();
    const ft = () => screen.getByRole("button", { name: "Full text" });
    await user.click(ft());
    await waitFor(() => expect(ft()).toBeDisabled());
    await user.click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
    await screen.findByRole("heading", { level: 1, name: "Article number 2" });
    expect(ft()).toBeEnabled();
    release();
  });
});

describe("addresses from the feed", () => {
  it("a non-http article or site address is never opened or linked, from the list or the article", async () => {
    const bad = "javascript:alert(1)";
    const feed = { id: "1", title: "Example Feed", site_url: bad };
    routes({
      "GET /api/items": () => json(pageOf([card(1, { url: bad }), card(2)], null, "2000")),
      "GET /api/items/1001": () => json(detail(1, { url: bad, feed })),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true, url: bad, feed }) }),
    });
    const open = vi.fn();
    vi.stubGlobal("open", open);
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    await user.keyboard("j"); // selects and opens row 1 beside the list
    await screen.findByTestId("article-body");
    await user.keyboard("o"); // open original (the list drives it in the pane)
    await user.keyboard("v"); // open in the background
    await user.click(within(screen.getByRole("toolbar", { name: "Article actions" })).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Open original" }));
    expect(open).not.toHaveBeenCalled();
    // The source name is plain text, not a link to a script.
    const source = screen.getByText("Example Feed", { selector: "header a" });
    expect(source).not.toHaveAttribute("href");

    // An ordinary address still opens.
    await user.keyboard("j");
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
    await user.keyboard("o");
    expect(open).toHaveBeenCalledWith("https://example.com/a/2", "_blank", "noopener,noreferrer");
  });
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

describe("an article that fails to load", () => {
  it("still offers the original link from the list's own cached card, and Try again refetches", async () => {
    let fail = true;
    const { calls } = routes({
      "GET /api/items/1001": () => (fail ? new Response("", { status: 500 }) : json(detail(1))),
    });
    go("/l/unread");
    await screen.findByText("Article number 1");
    await userEvent.setup().click(rowLink(1));
    await screen.findByRole("heading", { name: "Couldn't open this article" });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    await userEvent.setup().click(screen.getByRole("button", { name: "Read the original" }));
    expect(open).toHaveBeenCalledWith("https://example.com/a/1", "_blank", "noopener,noreferrer");

    fail = false;
    const before = calls.filter((c) => c.url.pathname === "/api/items/1001").length;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/items/1001").length).toBeGreaterThan(before));
    await screen.findByText("Body of article 1");
  });

  it("offline on a phone: the error screen with Back and Try again, not a skeleton forever (#95)", async () => {
    media();
    let offline = false;
    routes({
      "GET /api/items/1001": () => {
        if (offline) throw new TypeError("Failed to fetch");
        return json(detail(1));
      },
    });
    go("/l/unread");
    await screen.findByText("Article number 1");
    offline = true;
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    onlineManager.setOnline(false);
    try {
      await userEvent.setup().click(screen.getByText("Article number 1"));
      await screen.findByRole("heading", { name: "Couldn't open this article" });
      expect(screen.getByRole("button", { name: "Back to list" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
      // The original is on the web: offline it cannot open either, so it is not offered.
      expect(screen.queryByRole("button", { name: "Read the original" })).toBeNull();
    } finally {
      onlineManager.setOnline(true);
      vi.restoreAllMocks();
    }
  });

  it("on a phone the Back button is there while the article is still loading", async () => {
    media();
    routes({ "GET /api/items/1001": () => new Promise<Response>(() => {}) });
    go("/i/1001");
    await screen.findByText("Loading article");
    expect(screen.getByRole("button", { name: "Back to list" })).toBeInTheDocument();
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
