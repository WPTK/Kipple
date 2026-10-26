import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WIDE_QUERY } from "@/lib/useMedia";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

// Keyboard focus stays on the control that was pressed when what it acts on changes (issues #492, #494, #495, #390):
// a control that turns off is aria-disabled, not disabled, and the chrome around the article and the list is not
// remounted with them.

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
    ...Object.fromEntries([1, 2, 3, 4, 5].map((n) => [`GET /api/items/${1000 + n}`, () => json(detail(n))])),
    ...Object.fromEntries([1, 2, 3, 4, 5].map((n) => [`POST /api/items/${1000 + n}/open`, open(n)])),
    ...extra,
  });
}
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
function wide() {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes(WIDE_QUERY),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}
const rowLink = (n: number) => document.querySelector<HTMLElement>(`[data-item-id="${1000 + n}"] a`) as HTMLElement;
const toolbar = () => within(screen.getByRole("toolbar", { name: "Article actions" }));
const live = () => screen.getByTestId("live-region").textContent ?? "";

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
  wide();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("the reader beside the list keeps focus", () => {
  it("Enter on Next keeps focus on Next while the next article loads, and announces it", async () => {
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    routes({
      "GET /api/items/1002": async () => {
        await held;
        return json(detail(2));
      },
    });
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    await user.click(rowLink(1));
    await screen.findByTestId("article-body");
    toolbar().getByRole("button", { name: "Next article" }).focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
    await screen.findByText("Loading article");
    expect(toolbar().getByRole("button", { name: "Next article" })).toHaveFocus();
    release();
    await screen.findByRole("heading", { level: 1, name: "Article number 2" });
    expect(toolbar().getByRole("button", { name: "Next article" })).toHaveFocus();
    await waitFor(() => expect(live()).toContain("Article number 2"));
    // And Enter again goes on.
    await user.keyboard("{Enter}");
    await screen.findByRole("heading", { level: 1, name: "Article number 3" });
  });

  it("Previous onto the first article is switched off but keeps focus", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    await user.click(rowLink(2));
    await screen.findByRole("heading", { level: 1, name: "Article number 2" });
    toolbar().getByRole("button", { name: "Previous article" }).focus();
    await user.keyboard("{Enter}");
    await screen.findByRole("heading", { level: 1, name: "Article number 1" });
    const prev = toolbar().getByRole("button", { name: "Previous article" });
    expect(prev).toHaveAttribute("aria-disabled", "true");
    expect(prev).toHaveFocus();
    await user.keyboard("{Enter}"); // does nothing
    expect(window.location.pathname).toBe("/i/1001");
  });

  it("after Next the open article, not the row last focused, is the list's Tab stop (#390)", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    await user.click(rowLink(1));
    await screen.findByTestId("article-body");
    await user.click(toolbar().getByRole("button", { name: "Next article" }));
    await user.click(toolbar().getByRole("button", { name: "Next article" }));
    await screen.findByRole("heading", { level: 1, name: "Article number 3" });
    const stops = [...document.querySelectorAll('[data-testid="list-scroll"] [data-item-id] a:not([tabindex="-1"])')].map((a) => a.closest("[data-item-id]")?.getAttribute("data-item-id"));
    expect(stops).toEqual(["1003"]);
  });
});

describe("the list header keeps focus", () => {
  it("Refresh is switched off while its request runs, without losing focus", async () => {
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    routes({
      "POST /api/refresh": async () => {
        await held;
        return json({ run_id: "r1", total: 1 }, 202);
      },
    });
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    const trigger = screen.getByRole("button", { name: "List actions" });
    await user.click(trigger);
    await user.click(await screen.findByRole("menuitem", { name: "Refresh all feeds" }));
    // From the tap the trigger spins and the menu item says Refreshing (#482); it is switched off while the request runs.
    await waitFor(() => expect(trigger.querySelector("svg")).toHaveClass("animate-spin"));
    await waitFor(() => expect(trigger).toHaveFocus());
    await user.click(trigger);
    const item = await screen.findByRole("menuitem", { name: "Refreshing" });
    expect(item).toHaveAttribute("aria-disabled", "true");
    await user.keyboard("{Escape}");
    release();
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("Oldest first keeps focus though the list is rebuilt in the other order", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    screen.getByRole("button", { name: "Oldest first" }).focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.getByRole("button", { name: "Oldest first" })).toHaveAttribute("aria-pressed", "true"));
    expect(screen.getByRole("button", { name: "Oldest first" })).toHaveFocus();
  });
});

describe("list keys (#494)", () => {
  it("End, Home and PageDown move focus between rows; Space opens the focused row", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    rowLink(1).focus();
    await user.keyboard("{End}");
    await waitFor(() => expect(rowLink(5)).toHaveFocus());
    await user.keyboard("{Home}");
    await waitFor(() => expect(rowLink(1)).toHaveFocus());
    await user.keyboard("{PageDown}");
    await waitFor(() => expect(document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id")).not.toBe("1001"));
    rowLink(3).focus();
    await user.keyboard(" ");
    await waitFor(() => expect(window.location.pathname).toBe("/i/1003"));
  });
});

describe("sidebar tree (#495)", () => {
  it("Space on a folder opens its list and focus stays on the folder, which is announced", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    const tree = screen.getByRole("tree", { name: /feeds/i });
    const folder = within(tree).getByRole("treeitem", { name: /^News/ });
    folder.focus();
    await user.keyboard(" ");
    await waitFor(() => expect(window.location.search).toBe("?folder=1"));

    await waitFor(() => expect(live()).toContain("News"));
    expect(within(screen.getByRole("tree", { name: /feeds/i })).getByRole("treeitem", { name: /^News/ })).toHaveFocus();
  });
});

describe("sidebar tree, by mouse", () => {
  it("a click on a feed still moves focus to the new list's heading", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    const tree = screen.getByRole("tree", { name: /feeds/i });
    await user.click(within(tree).getByRole("link", { name: /Example Feed/ }));
    await waitFor(() => expect(window.location.search).toBe("?feed=1"));
    await waitFor(() => expect(document.activeElement?.hasAttribute("data-route-heading")).toBe(true));
  });
});

describe("search (#495)", () => {
  it("ArrowDown in the search field goes into the results", async () => {
    routes();
    go("/search?q=article");
    await waitFor(() => expect(rowLink(2)).not.toBeNull());
    const user = userEvent.setup();
    const field = screen.getByRole("searchbox", { name: "Search articles" });
    field.focus();
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement?.closest("[data-item-id]")).not.toBeNull();
  });
});
