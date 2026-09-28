import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import App, { makeQueryClient } from "@/App";
import type { SettingMeta } from "@/api/admin";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { keys } from "@/api/queries";
import type { Bootstrap } from "@/api/types";
import { rowMenuStore } from "@/gestures/rowMenu";
import { resetDevicePrefs, devicePrefsStore, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetFavoritesMode } from "@/lib/favorites";
import { itemActions } from "@/lib/itemActions";
import { DEFAULT_PREFS, applyPrefs, prefsStore, updatePrefs } from "@/lib/prefs";
import { resetUndo, undoLast } from "@/lib/undo";
import { helpStore } from "@/shell/HelpDialog";
import { INFO_MS, ACTION_MS, clearToasts, toast, toastMs, Toasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory, NewArticlesPill } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
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
const WIDE = "min-width: 900px";

const feedBase = bootstrap.feeds[0] as Bootstrap["feeds"][number];
const feed = (id: string, folder: string, title: string, over: Partial<Bootstrap["feeds"][number]> = {}) => ({ ...feedBase, id, folder_id: folder, title, ...over });
const boot3: Bootstrap = {
  ...bootstrap,
  folders: [
    { id: "1", name: "News", position: 0, is_default: true, unread: 3 },
    { id: "2", name: "Tech", position: 1, is_default: false, unread: 2 },
  ],
  feeds: [feed("1", "1", "Alpha", { starred_count: 2 }), feed("2", "1", "Bravo", { starred_count: 1 }), feed("3", "1", "Charlie"), feed("4", "2", "Delta")],
};

function routes(extra: Parameters<typeof mockFetch>[0] = {}, boot: Bootstrap = bootstrap) {
  return mockFetch({
    "GET /api/bootstrap": () => json(boot),
    "GET /api/items": () => json(pageOf([card(1), card(2), card(3)])),
    "GET /api/items/1001": () => json(detail(1)),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });
}

beforeEach(() => {
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  resetFavoritesMode();
  helpStore.set(false);
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('the "N new articles" pill', () => {
  it("stays mounted and only toggles its state, keeping the last count while it fades", () => {
    const click = vi.fn();
    const { rerender } = render(<NewArticlesPill count={2} onClick={click} />);
    const pill = screen.getByRole("button", { name: "2 new articles" });
    expect(pill).toHaveAttribute("data-open", "true");
    rerender(<NewArticlesPill count={0} onClick={click} />);
    // Same node (no unmount, no layout change), hidden from assistive tech and the tab order, text kept for the fade.
    const hidden = screen.getByText("2 new articles");
    expect(hidden).toBe(pill);
    expect(hidden).toHaveAttribute("data-open", "false");
    expect(hidden).toHaveAttribute("aria-hidden", "true");
    expect(hidden).toHaveAttribute("tabindex", "-1");
    rerender(<NewArticlesPill count={1} onClick={click} />);
    expect(screen.getByRole("button", { name: "1 new article" })).toBe(pill);
  });
});

describe("toasts", () => {
  it("info toasts last 8 s, an action toast 15 s, an error until dismissed", () => {
    expect(toastMs({ kind: "info" })).toBe(INFO_MS);
    expect(INFO_MS).toBeGreaterThanOrEqual(8000);
    expect(toastMs({ kind: "info", action: { label: "Undo", run() {} } })).toBe(ACTION_MS);
    expect(ACTION_MS).toBe(15000);
    expect(toastMs({ kind: "error" })).toBeNull();
  });

  it("dismisses on the clock, holds while hovered or focused, and errors need a click", async () => {
    vi.useFakeTimers();
    render(<Toasts />);
    act(() => void toast("Link copied"));
    expect(screen.getByText("Link copied")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(7900));
    expect(screen.getByText("Link copied")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(200));
    expect(screen.queryByText("Link copied")).toBeNull();

    act(() => void toast("Hold me"));
    const box = screen.getByText("Hold me").parentElement as HTMLElement;
    fireEvent.pointerEnter(box);
    act(() => vi.advanceTimersByTime(60_000));
    expect(screen.getByText("Hold me")).toBeInTheDocument();
    fireEvent.pointerLeave(box);
    act(() => vi.advanceTimersByTime(8100));
    expect(screen.queryByText("Hold me")).toBeNull();

    act(() => void toast("Something failed", "error"));
    act(() => vi.advanceTimersByTime(120_000));
    expect(screen.getByText("Something failed")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Something failed")).toBeNull();
  });

  it("uses the themed toast surface, not the plain card look", () => {
    render(<Toasts />);
    act(() => void toast("Styled"));
    const box = screen.getByText("Styled").parentElement as HTMLElement;
    expect(box.className).toContain("var(--kp-toast-bg)");
    expect(box.className).toContain("border-accent");
  });
});

describe("read state in the Unread list", () => {
  const menuFor = async (user: ReturnType<typeof userEvent.setup>, n: number) => {
    const row = document.querySelector(`[data-item-id="${1000 + n}"]`) as HTMLElement;
    await user.click(within(row).getByRole("button", { name: "More actions" }));
  };

  it("labels the row menu and the article button by their action", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await menuFor(user, 1);
    expect(await screen.findByRole("menuitem", { name: "Mark as read" })).toBeInTheDocument();
  });

  it("a row marked read from its menu leaves the Unread list after about 1.5 s, and Undo brings it back", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: (ms) => vi.advanceTimersByTime(ms) });
    await menuFor(user, 1);
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" }));
    await waitFor(() => expect(screen.getByText("Marked read")).toBeInTheDocument());
    // Still there, dimmed, for a moment (with the undo toast up)...
    await act(async () => void (await vi.advanceTimersByTimeAsync(1000)));
    expect(screen.getByText("Article number 1")).toBeInTheDocument();
    // ...then gone.
    await act(async () => void (await vi.advanceTimersByTimeAsync(900)));
    await waitFor(() => expect(screen.queryByText("Article number 1")).toBeNull());
    expect(screen.getByText("Article number 2")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Undo" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
  });

  it("Undo inside the 1.5 s keeps the row from ever leaving", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: (ms) => vi.advanceTimersByTime(ms) });
    await menuFor(user, 1);
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" }));
    await user.click(await screen.findByRole("button", { name: "Undo" }));
    await act(async () => void (await vi.advanceTimersByTimeAsync(3000)));
    expect(screen.getByText("Article number 1")).toBeInTheDocument();
  });

  it("phone: marked read, then an article is opened (the list unmounts), then Undo: the row is back when the list returns", async () => {
    routes();
    const first = go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await menuFor(user, 1);
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" }));
    await waitFor(() => expect(screen.getByText("Marked read")).toBeInTheDocument());
    // Within 1.5 s the list goes away (a phone shows the article as a new screen): the row is remembered as gone.
    first.unmount();
    await act(async () => void (await undoLast()));
    go("/l/unread");
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(screen.getByText("Article number 2")).toBeInTheDocument();
  });

  it("a remembered-hidden row that comes back unread (not marked on purpose) is shown again on return", async () => {
    routes();
    const first = go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await menuFor(user, 1);
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" }));
    first.unmount();
    // Undone somewhere the list could not hear (its intent is gone): the server serves it unread again.
    const { readIntent } = await import("@/lib/itemActions");
    act(() => readIntent.set(new Set()));
    go("/l/unread");
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
  });

  it("an article marked unread again is a normal unread row (and menu items say what they do)", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await menuFor(user, 1);
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" }));
    await menuFor(user, 1);
    await user.click(await screen.findByRole("menuitem", { name: "Mark as unread" }));
    expect(document.querySelector('[data-item-id="1001"] [data-unread="true"]')).not.toBeNull();
  });

  it("marking unread makes the article show up in the Unread list even if it was not in that snapshot", async () => {
    const qc = new QueryClient();
    const unread = { view: "unread" as const };
    const all = { view: "all" as const };
    qc.setQueryData(keys.items(unread), { pages: [pageOf([card(2)])], pageParams: [""] });
    qc.setQueryData(keys.items(all), { pages: [pageOf([card(1), card(2, { read: true })])], pageParams: [""] });
    mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["1002"], restored: [] }) });
    await itemActions(qc).setRead(["1002"], false, "key");
    // The read article is unread now, so the stale Unread snapshot must reload the next time it is shown.
    expect(qc.getQueryState(keys.items(unread))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(keys.items(all))?.isInvalidated).toBe(false);
  });

  it("the read/unread button in the article toolbar is labelled by its action and shows the state", async () => {
    routes();
    media(WIDE);
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const state = await screen.findByTestId("read-state");
    await waitFor(() => expect(state).toHaveTextContent("Read"));
    expect(screen.getByRole("button", { name: "Mark as unread" })).toBeInTheDocument();
  });
});

describe("sidebar", () => {
  it("has no Feed health entry, keeps Settings at the bottom, and puts a Settings gear by the list controls", async () => {
    routes();
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(within(nav).queryByRole("link", { name: /Feed health/ })).toBeNull();
    const links = within(nav).getAllByRole("link");
    expect(links[links.length - 1]).toHaveAccessibleName("Settings"); // pinned at the bottom
    const header = screen.getByRole("heading", { level: 1, name: "Unread" }).closest("header") as HTMLElement;
    expect(within(header).getByRole("link", { name: "Settings" })).toHaveAttribute("href", "/settings");
    // The controls sit in a box capped at 25rem and left-aligned, so a full-width grid layout cannot push them right.
    expect(header.querySelector(".max-w-\\[25rem\\]")).not.toBeNull();
  });

  it("folders collapse and expand, and the choice is kept on this device", async () => {
    routes({}, boot3);
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const nav = screen.getByRole("navigation", { name: "Primary" });
    const user = userEvent.setup();
    expect(within(nav).getByRole("link", { name: /Alpha/ })).toBeInTheDocument();
    await user.click(within(nav).getByRole("button", { name: "Collapse News" }));
    expect(within(nav).queryByRole("link", { name: /Alpha/ })).toBeNull();
    expect(within(nav).getByRole("link", { name: /Delta/ })).toBeInTheDocument(); // other folders unaffected
    expect(within(nav).getByRole("button", { name: "Expand News" })).toHaveAttribute("aria-expanded", "false");
    expect(devicePrefsStore.get().collapsedFolders).toEqual(["1"]);
    await user.click(within(nav).getByRole("button", { name: "Expand News" }));
    expect(within(nav).getByRole("link", { name: /Alpha/ })).toBeInTheDocument();
    expect(devicePrefsStore.get().collapsedFolders).toEqual([]);
  });

  it("favorites are pinned in a Favorites section (kept on this device when the server has no such setting)", async () => {
    routes({}, boot3);
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const nav = screen.getByRole("navigation", { name: "Primary" });
    const user = userEvent.setup();
    expect(within(nav).queryByRole("region", { name: "Favorites" })).toBeNull();
    await user.click(within(nav).getByRole("button", { name: "Favorite Delta" }));
    await user.click(within(nav).getByRole("button", { name: "Favorite News" }));
    const fav = await within(nav).findByRole("region", { name: "Favorites" });
    expect(within(fav).getAllByRole("link").map((l) => l.textContent)).toEqual(["Delta", "News"].map((t) => expect.stringContaining(t)));
    expect(devicePrefsStore.get().favoritesLocal).toEqual([
      { t: "feed", id: "4" },
      { t: "folder", id: "1" },
    ]);
    await user.click(within(fav).getByRole("button", { name: "Favorite Delta" }));
    expect(devicePrefsStore.get().favoritesLocal).toEqual([{ t: "folder", id: "1" }]);
  });

  it("with a server that has library.favorites, a star PATCHes the setting", async () => {
    const boot = { ...boot3, settings: { "library.favorites": [] } };
    const { calls } = routes(
      {
        "PATCH /api/settings": (_u, init) => json({ settings: [], values: { "library.favorites": JSON.parse(String(init?.body))["library.favorites"] } }),
      },
      boot,
    );
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const nav = screen.getByRole("navigation", { name: "Primary" });
    await userEvent.setup().click(within(nav).getByRole("button", { name: "Favorite Bravo" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH" && c.url.pathname === "/api/settings")).toBe(true));
    const patch = calls.find((c) => c.method === "PATCH");
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ "library.favorites": [{ t: "feed", id: "2" }] });
    expect(await within(nav).findByRole("region", { name: "Favorites" })).toBeInTheDocument();
    expect(devicePrefsStore.get().favoritesLocal).toEqual([]); // not duplicated on the device
  });

  it("falls back to this device when the server rejects the setting", async () => {
    const boot = { ...boot3, settings: { "library.favorites": [] } };
    routes({ "PATCH /api/settings": () => json({ error: "invalid_settings", issues: [{ key: "library.favorites", message: "unknown setting" }], keys: ["library.favorites"] }, 400) }, boot);
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const nav = screen.getByRole("navigation", { name: "Primary" });
    await userEvent.setup().click(within(nav).getByRole("button", { name: "Favorite Bravo" }));
    await waitFor(() => expect(devicePrefsStore.get().favoritesLocal).toEqual([{ t: "feed", id: "2" }]));
    expect(await within(nav).findByRole("region", { name: "Favorites" })).toBeInTheDocument();
  });

  it("the unread badge follows the device setting and caps at 99+", async () => {
    routes({}, { ...boot3, counts: { unread: 4321, starred: 0 } });
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(within(nav).getAllByTestId("unread-count")[0]).toHaveTextContent("99+");
    act(() => updateDevicePrefs({ unreadBadge: "off" }));
    expect(within(nav).queryAllByTestId("unread-count")).toHaveLength(0);
    act(() => updateDevicePrefs({ unreadBadge: "dot" }));
    expect(within(nav).getAllByTestId("unread-dot").length).toBeGreaterThan(0);
  });

  it("the tab bar on a phone shows the same badge setting", async () => {
    routes({}, { ...boot3, counts: { unread: 1500, starred: 0 } });
    go("/l/unread");
    await screen.findByText("Article number 1");
    const tabs = screen.getByRole("navigation", { name: "Primary" });
    expect(within(tabs).getByTestId("unread-count")).toHaveTextContent("99+");
    act(() => updateDevicePrefs({ unreadBadge: "off" }));
    expect(within(tabs).queryByTestId("unread-count")).toBeNull();
  });

  it("at the 900 px breakpoint a wide saved sidebar is capped so the list and article keep their room, and the handle says so", async () => {
    routes();
    media(WIDE);
    vi.stubGlobal("innerWidth", 900);
    updateDevicePrefs({ sidebarWidth: 420 });
    go("/l/unread");
    await screen.findByText("Article number 1");
    const side = screen.getByRole("separator", { name: "Resize sidebar" });
    expect(side).toHaveAttribute("aria-valuemax", "320");
    expect(side).toHaveAttribute("aria-valuenow", "320");
    expect((side.parentElement as HTMLElement).style.width).toBe("320px");
  });

  it("the list handle never offers more than leaves the article 320 px, and its value is the width on screen", async () => {
    routes();
    media(WIDE);
    boxWidth(700);
    updateDevicePrefs({ listWidth: 600 });
    go("/l/unread");
    await screen.findByText("Article number 1");
    const list = screen.getByRole("separator", { name: "Resize article list" });
    expect(list).toHaveAttribute("aria-valuemax", "380");
    expect(list).toHaveAttribute("aria-valuenow", "380");
    expect((list.parentElement as HTMLElement).style.width).toBe("380px");
  });

  it("dragging the article list does not write the preference until release", async () => {
    routes();
    media(WIDE);
    boxWidth(1400);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const list = screen.getByRole("separator", { name: "Resize article list" });
    const before = devicePrefsStore.get().listWidth;
    fireEvent.pointerDown(list, { pointerId: 1, clientX: 300, button: 0 });
    fireEvent.pointerMove(list, { pointerId: 1, clientX: 340 });
    fireEvent.pointerMove(list, { pointerId: 1, clientX: 380 });
    expect(devicePrefsStore.get().listWidth).toBe(before);
    fireEvent.pointerUp(list, { pointerId: 1, clientX: 380 });
    expect(devicePrefsStore.get().listWidth).not.toBe(before);
  });

  it("the sidebar and the article list are resizable and remember their widths", async () => {
    routes();
    media(WIDE);
    boxWidth(1400);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const side = screen.getByRole("separator", { name: "Resize sidebar" });
    fireEvent.keyDown(side, { key: "ArrowRight" });
    expect(devicePrefsStore.get().sidebarWidth).toBe(256);
    const list = screen.getByRole("separator", { name: "Resize article list" });
    fireEvent.keyDown(list, { key: "ArrowRight", shiftKey: true });
    expect(devicePrefsStore.get().listWidth).toBeGreaterThan(300);
    fireEvent.doubleClick(list);
    expect(devicePrefsStore.get().listWidth).toBeNull();
  });
});

/** The test setup reports every element as 375 px wide; a wide layout needs a wide box. */
const boxWidth = (w: number) =>
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue({ x: 0, y: 0, top: 0, left: 0, right: w, bottom: 800, width: w, height: 800, toJSON() {} } as DOMRect);

describe("manage feeds", () => {
  const reorderCalls = (calls: { method: string; url: URL; init?: RequestInit }[]) =>
    calls.filter((c) => c.url.pathname === "/api/reorder").map((c) => JSON.parse(String(c.init?.body)));

  it("the grip moves a feed with the arrow keys, saves in one reorder call and says Saved", async () => {
    const { calls } = routes({ "POST /api/reorder": () => json({ changed_feeds: ["1", "2"], changed_folders: [] }) }, boot3);
    go("/feeds");
    await screen.findByText("Alpha");
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const grip = screen.getByRole("button", { name: /Reorder Alpha/ });
    fireEvent.keyDown(grip, { key: "ArrowDown" });
    await waitFor(() => expect(reorderCalls(calls)).toEqual([{ feeds: [{ folder_id: "1", ids: ["2", "1", "3"] }] }]));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("keyboard reorder keeps focus on the moved row's grip (the keyed row is moved, which drops focus)", async () => {
    routes({ "POST /api/reorder": () => json({ changed_feeds: ["1", "2"], changed_folders: [] }) }, boot3);
    go("/feeds");
    await screen.findByText("Alpha");
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const grip = screen.getByRole("button", { name: /Reorder Alpha/ });
    grip.focus();
    fireEvent.keyDown(grip, { key: "ArrowDown" });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: /Reorder Alpha/ })));
    // ...and again, from the new place, so a second press works without touching the mouse.
    fireEvent.keyDown(document.activeElement as Element, { key: "ArrowDown" });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: /Reorder Alpha/ })));
  });

  it("the Move buttons keep focus on the same button, or on the grip once it is disabled at the end", async () => {
    // A server that remembers the new order, so the refetch after the save agrees with what was moved.
    const live = { ...boot3, feeds: [...boot3.feeds] };
    routes(
      {
        "GET /api/bootstrap": () => json(live),
        "POST /api/reorder": (_u, init) => {
          const ids: string[] = JSON.parse(String(init?.body)).feeds[0].ids;
          live.feeds = [...ids.map((id) => boot3.feeds.find((f) => f.id === id)!), ...boot3.feeds.filter((f) => !ids.includes(f.id))];
          return json({ changed_feeds: ids, changed_folders: [] });
        },
      },
      boot3,
    );
    go("/feeds");
    await screen.findByText("Alpha");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show move buttons" }));
    await user.click(screen.getByRole("button", { name: "Move Alpha down" }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Move Alpha down" })));
    await user.click(screen.getByRole("button", { name: "Move Alpha down" }));
    // Alpha is now last: its Down button is disabled, so focus goes to its grip rather than the page.
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: /Reorder Alpha/ })));
  });

  it("stars a feed and a folder into Favorites, at the top, in the order they are kept", async () => {
    routes({}, boot3);
    go("/feeds");
    await screen.findByText("Alpha");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Favorite Delta" }));
    await user.click(screen.getByRole("button", { name: "Favorite News" }));
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const fav = await screen.findByRole("region", { name: "Favorites" });
    expect(within(fav).getAllByRole("link").map((l) => l.textContent)).toEqual([expect.stringContaining("Delta"), expect.stringContaining("News")]);
    // Move the second favorite up with its keyboard handle.
    fireEvent.keyDown(within(fav).getByRole("button", { name: /Reorder favorite News/ }), { key: "ArrowUp" });
    await waitFor(() => expect(devicePrefsStore.get().favoritesLocal[0]).toEqual({ t: "folder", id: "1" }));
  });

  it("selects with checkboxes, shift-click ranges and whole folders, then bulk-moves in one reorder call", async () => {
    const { calls } = routes({ "POST /api/reorder": () => json({ changed_feeds: ["2", "3"], changed_folders: [] }) }, boot3);
    go("/feeds");
    await screen.findByText("Alpha");
    const user = userEvent.setup();
    // The toggle keeps its name below 400px, where its text is hidden (issue #47).
    const toggle = screen.getByRole("button", { name: /Select/ });
    expect(toggle).toHaveAttribute("aria-label", "Select");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-label", "Done");
    expect(toggle).not.toHaveAttribute("aria-pressed"); // the name carries the state
    expect(screen.getByText("0 selected")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "Select Bravo" }));
    await user.keyboard("{Shift>}");
    await user.click(screen.getByRole("checkbox", { name: "Select Charlie" }));
    await user.keyboard("{/Shift}");
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    // A folder checkbox ticks all its feeds (and is tri-state).
    const folder = screen.getByRole("checkbox", { name: "Select all feeds in News" }) as HTMLInputElement;
    expect(folder.indeterminate).toBe(true);
    await user.click(folder);
    expect(screen.getByText("3 selected")).toBeInTheDocument();
    await user.click(folder);
    expect(screen.getByText("0 selected")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "Select Bravo" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Charlie" }));
    await user.click(screen.getByRole("button", { name: "Move to folder" }));
    const dlg = await screen.findByRole("dialog", { name: "Move 2 feeds" });
    await user.selectOptions(within(dlg).getByLabelText("Move to folder"), "Tech");
    await user.click(within(dlg).getByRole("button", { name: "Move" }));
    await waitFor(() => expect(reorderCalls(calls)).toEqual([{ feeds: [{ folder_id: "2", ids: ["4", "2", "3"] }] }]));
  });

  it("bulk delete confirms the count and the starred total, offers deleting starred too, and reports each failure", async () => {
    const del: string[] = [];
    routes(
      {
        "DELETE /api/feeds/1": (u) => (del.push("1" + u.search), new Response(null, { status: 204 })),
        "DELETE /api/feeds/2": () => json({ error: "internal", message: "database is busy" }, 500),
      },
      boot3,
    );
    go("/feeds");
    await screen.findByText("Alpha");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /Select/ }));
    await user.click(screen.getByRole("checkbox", { name: "Select Alpha" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Bravo" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));
    const dlg = await screen.findByRole("dialog", { name: "Delete 2 feeds?" });
    expect(within(dlg).getByText(/hold 3 starred articles in total/)).toBeInTheDocument(); // 2 + 1
    await user.click(within(dlg).getByRole("switch", { name: /Delete starred articles too/ }));
    await user.click(within(dlg).getByRole("button", { name: "Delete 2 feeds" }));
    const report = await screen.findByRole("dialog", { name: "Some feeds were not deleted" });
    expect(within(report).getByText(/1 deleted, 1 failed/)).toBeInTheDocument();
    expect(within(report).getByText(/Bravo/)).toBeInTheDocument();
    expect(within(report).getByText(/The server returned an error/)).toBeInTheDocument();
    expect(del).toEqual(["1?delete_starred=1"]);
  });

  it("Feed health stays reachable from the Feeds menu", async () => {
    routes({}, boot3);
    go("/feeds");
    await screen.findByText("Alpha");
    await userEvent.setup().click(screen.getByRole("button", { name: "Feed actions" }));
    expect(await screen.findByRole("menuitem", { name: "Feed health" })).toBeInTheDocument();
  });

  it("the feed editor says 'On for all feeds' when the global full-text switch is on", async () => {
    const boot = { ...boot3, settings: { "fetch.fulltext_all": true } };
    routes(
      {
        "GET /api/feeds/1": () =>
          json({ ...boot.feeds[0], url: "https://a.example/feed", url_original: null, custom_title: null, position: 0, enabled: true, disabled_reason: null, dedup_mode: "auto", rekey_pending: false, user_agent: null, has_http_auth: false, ignore_http_cache: false, disable_http2: false, allow_insecure_tls: false, allow_private_net: false, next_fetch_at: 0 }),
      },
      boot,
    );
    go("/feeds");
    await screen.findByText("Alpha");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Edit Alpha" }));
    const dlg = await screen.findByRole("dialog", { name: "Edit feed" });
    const sw = await within(dlg).findByRole("switch", { name: /Fetch full article text/ });
    expect(sw).toBeChecked();
    expect(sw).toBeDisabled();
    expect(within(dlg).getByText(/On for all feeds/)).toBeInTheDocument();
  });
});

describe("Settings and the Aa menu", () => {
  const meta = (m: Partial<SettingMeta> & Pick<SettingMeta, "key" | "kind">): SettingMeta => ({ value: null, default: null, label: m.key, description: "", group: "library", surface: "settings", ...m });

  it("shows the full-article setting with its help about performance", async () => {
    const s = meta({
      key: "fetch.fulltext_all",
      kind: "bool",
      label: "Fetch the full article for every feed",
      description: "Download the page of each new article and show its full text. This uses a little more bandwidth and time on each refresh.",
      value: false,
      default: false,
    });
    routes({ "GET /api/settings": () => json({ settings: [s], values: { [s.key]: false } }) });
    go("/settings/sync");
    const sw = await screen.findByRole("switch", { name: /Fetch the full article for every feed/ });
    expect(sw).not.toBeChecked();
    expect(screen.getByText(/more bandwidth and time on each refresh/)).toBeInTheDocument();
  });

  it("has the new device settings, narrower on a wide screen, and no second font control", async () => {
    routes();
    media(WIDE);
    go("/settings");
    const user = userEvent.setup();
    // Wide: the rail and the first group, Appearance & Reading, side by side.
    await screen.findByRole("heading", { name: "Settings" });
    expect(await screen.findByRole("heading", { level: 2, name: "Appearance & Reading" })).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Settings sections" })).getByRole("link", { name: "Appearance & Reading" })).toHaveAttribute("aria-current", "page");
    expect(screen.queryByRole("combobox", { name: /font/i })).toBeNull();
    expect(document.querySelector(".ui-font")).not.toBeNull(); // Settings keeps the system UI font
    expect(document.querySelector(".max-w-\\[720px\\]")).not.toBeNull();
    await user.click(screen.getByRole("radio", { name: "Wide" }));
    expect(devicePrefsStore.get().articleWidth).toBe("wide");
    await user.click(screen.getByRole("radio", { name: "Same tab" }));
    expect(devicePrefsStore.get().linkTarget).toBe("same");
    await user.click(screen.getByRole("radio", { name: "Dot only" }));
    expect(devicePrefsStore.get().unreadBadge).toBe("dot");
    // Text spacing is not a second density picker: its own name and one-line explanation.
    expect(screen.getByRole("group", { name: "Text spacing" })).toBeInTheDocument();
    expect(screen.getByText("Adds extra space between letters, words and lines")).toBeInTheDocument();
    expect(screen.queryByText("Reading spacing")).toBeNull();
  });

  it("a font chosen in the Aa menu applies to the whole app the moment it is picked", async () => {
    routes();
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(screen.getAllByRole("button", { name: "Reading appearance" })[0] as HTMLElement);
    const dlg = await screen.findByRole("dialog", { name: "Reading appearance" });
    await user.selectOptions(within(dlg).getByLabelText(/Reading font|Font/), "inter");
    expect(prefsStore.get().font).toBe("inter"); // one store write: nothing to refresh
    applyPrefs(prefsStore.get()); // what initPrefs' subscription does on every change
    const root = document.documentElement;
    expect(root.style.getPropertyValue("--kp-app-font")).toContain("Inter");
    expect(root.style.getPropertyValue("--kp-reading-font")).toContain("Inter");
    // Text spacing lives in Settings, so the Aa menu has one density control only.
    expect(within(dlg).queryByRole("group", { name: /spacing/i })).toBeNull();
  });

  it("the article column follows the Article width setting", async () => {
    routes();
    media(WIDE);
    updateDevicePrefs({ articleWidth: "wide" });
    go("/i/1001?from=unread");
    const article = (await screen.findByTestId("article-body")).closest("article") as HTMLElement;
    expect(article.style.getPropertyValue("--kp-col")).toBe("62rem");
    act(() => updateDevicePrefs({ articleWidth: "full" }));
    expect(article.style.getPropertyValue("--kp-col")).toBe("100%");
    act(() => updateDevicePrefs({ articleWidth: "narrow" }));
    expect(article.style.getPropertyValue("--kp-col")).toBe("34rem");
  });
});

describe("share", () => {
  it("the article toolbar's Share button opens the share sheet, or copies the link with a toast", async () => {
    routes();
    media(WIDE);
    const share = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, share });
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Share" }));
    expect(share).toHaveBeenCalledWith({ title: "Article number 1", url: "https://example.com/a/1" });
  });

  it("copies the link where there is no share sheet", async () => {
    routes();
    media(WIDE);
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { userAgent: "test", clipboard: { writeText } });
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    fireEvent.click(screen.getByRole("button", { name: "Share" })); // (user-event would install its own clipboard stub)
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("https://example.com/a/1"));
    expect(await screen.findByText("Link copied")).toBeInTheDocument();
  });
});

describe("keyboard shortcut default and layouts", () => {
  it("labels the layouts Editorial and Email - Compact, ids unchanged", async () => {
    routes();
    media(WIDE);
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(screen.getByRole("button", { name: "Layout: Editorial" })).toBeInTheDocument();
    act(() => updatePrefs({ shortcuts: true }));
    act(() => updateDevicePrefs({ layout: "headlines" }));
    expect(await screen.findByRole("button", { name: "Layout: Email - Compact" })).toBeInTheDocument();
    expect(devicePrefsStore.get().layout).toBe("headlines");
  });
});
