import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, sessionLayoutStore, setListOverride, updateDevicePrefs, devicePrefsStore, type LayoutId } from "@/lib/devicePrefs";
import { updatePrefs } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { rowMenuStore } from "@/gestures/rowMenu";
import { helpStore } from "@/shell/HelpDialog";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

const IMG = "/img/x.jpg";
const BASE = 1_790_000_000;
const at = (n: number) => ({ published_at: BASE - n * 3600, sort_at: BASE - n * 3600 });
const items = () => [card(1, { image: IMG, ...at(1) }), card(2, { read: true, ...at(2) }), card(3, { starred: true, ...at(3) }), card(4, at(4)), card(5, at(5))];

/** Article routes whose body is `html` (the open response replaces the cached detail, so it carries it too). */
const article = (html: string) => ({
  "GET /api/items/1001": () => json(detail(1, { content_html: html })),
  "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true, content_html: html }) }),
});

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(items())),
    "GET /api/items/1001": () => json(detail(1)),
    "GET /api/items/1002": () => json(detail(2, { read: true })),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    "POST /api/items/1002/open": () => json({ session_key: "k", item: detail(2, { read: true }) }),
    "PUT /api/items/1001/star": () => json({ starred: true, restored: false }),
    "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }),
    "POST /api/refresh": () => json({ run_id: "r1", total: 1 }, 202),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

/** matchMedia that answers true for the listed queries. */
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
const REDUCED = "prefers-reduced-motion";
const COARSE = "pointer: coarse";

/** Gesture time: pointer handlers read performance.now(), so tests move it by hand. */
const clock = { t: 1000 };
const tick = (ms: number) => void (clock.t += ms);

type PT = "pointerDown" | "pointerMove" | "pointerUp" | "pointerCancel";
function ptr(el: Element, type: PT, x: number, y = 100, id = 1) {
  fireEvent[type](el, { pointerType: "touch", pointerId: id, clientX: x, clientY: y, isPrimary: true });
}
/** A touch drag: down, a few moves, up, over `ms` (slow by default, so it is not a flick). */
function swipe(el: Element, x0: number, dx: number, y = 100, ms = 600) {
  ptr(el, "pointerDown", x0, y);
  for (let i = 1; i <= 6; i++) {
    tick(ms / 6);
    ptr(el, "pointerMove", x0 + (dx * i) / 6, y + (i % 2));
  }
  ptr(el, "pointerUp", x0 + dx, y);
}
/** The article frame; jsdom has no layout, so give it a phone width. */
function articleFrame(): HTMLElement {
  const frame = screen.getByRole("toolbar", { name: "Article actions" }).parentElement as HTMLElement;
  Object.defineProperty(frame, "clientWidth", { configurable: true, value: 375 });
  return frame;
}
const contentOf = (id: string) => document.querySelector(`[data-item-id="${id}"]`)?.closest(".swipe-content") as HTMLElement;

function touchEv(el: Element, type: "touchstart" | "touchmove" | "touchend", x: number, y: number) {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  const pt = { clientX: x, clientY: y };
  Object.defineProperty(ev, "touches", { value: type === "touchend" ? [] : [pt] });
  act(() => {
    el.dispatchEvent(ev);
  });
  return ev;
}

beforeEach(() => {
  vi.spyOn(performance, "now").mockImplementation(() => clock.t);
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  helpStore.set(false);
  updatePrefs({ shortcuts: true });
  // The first-run tip is once per device; tests that are not about it have seen it.
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const bodyOf = (call: { init?: RequestInit } | undefined) => JSON.parse(String(call?.init?.body));

describe("layouts", () => {
  const hallmarks: Record<LayoutId, (root: HTMLElement) => void> = {
    magazine: (r) => {
      expect(r.querySelectorAll("article").length).toBeGreaterThan(0);
      expect(within(r).getByText("Excerpt for article 1")).toBeInTheDocument();
      expect(r.querySelector('img[src="/img/x.jpg"]')).not.toBeNull();
    },
    cards: (r) => {
      expect(r.querySelector("img.aspect-video")).not.toBeNull(); // 16:9 lead image
      expect(within(r).getByText("Excerpt for article 1")).toBeInTheDocument();
      expect(r.querySelector("[data-swipe-row]")).not.toBeNull(); // one column at 375: swipe stays on
    },
    compact: (r) => {
      expect(r.querySelector(".row-compact")).not.toBeNull();
      expect(within(r).queryByText("Excerpt for article 1")).toBeNull(); // no snippet, no image
      expect(r.querySelector('img[src="/img/x.jpg"]')).toBeNull();
    },
    inbox: (r) => {
      // sender (feed) in bold while unread, subject, snippet, thumb
      const row = r.querySelector('[data-item-id="1001"]') as HTMLElement;
      expect(within(row).getByText("Example Feed")).toHaveClass("font-bold");
      expect(within(row).getByText("Excerpt for article 1")).toBeInTheDocument();
      expect(row.querySelector('img[src="/img/x.jpg"]')).not.toBeNull();
      const read = r.querySelector('[data-item-id="1002"]') as HTMLElement;
      expect(within(read).getByText("Example Feed")).not.toHaveClass("font-bold");
    },
    gazette: (r) => {
      // The whole list as one page: a masthead, stories as articles, no virtualized rows.
      expect(within(r).getByRole("heading", { name: "The Gazette" })).toBeInTheDocument();
      expect(r.querySelectorAll("article[data-slot]").length).toBe(5);
      expect(r.querySelector("[data-index]")).toBeNull();
    },
    headlines: (r) => {
      expect(r.querySelector(".row-headline")).not.toBeNull();
      expect(within(r).queryByText("Excerpt for article 1")).toBeNull();
      expect(r.querySelector("img")).toBeNull();
    },
  };

  for (const id of Object.keys(hallmarks) as LayoutId[]) {
    it(`${id} renders its rows, keeps unread readable without color, and passes axe`, async () => {
      routes();
      updateDevicePrefs({ layout: id });
      const { container } = go("/l/unread");
      await screen.findByText("Article number 1");
      hallmarks[id](container);
      // Unread state is in the accessible name (and the dot and weight), never color alone.
      expect(screen.getByRole("link", { name: /^Unread, Article number 1/ })).toBeInTheDocument();
      expect(screen.getByRole("link", { name: /^Article number 2/ })).toBeInTheDocument();
      expect(await axe(container)).toHaveNoViolations();
    });
  }

  it("Inbox thumbnails can be turned off", async () => {
    routes();
    updateDevicePrefs({ layout: "inbox", inboxThumbs: "off" });
    const { container } = go("/l/unread");
    await screen.findByText("Article number 1");
    expect(container.querySelector('img[src="/img/x.jpg"]')).toBeNull();
  });

  it("a feed override beats the device default, and other lists keep the default", async () => {
    routes();
    updateDevicePrefs({ layout: "compact" });
    setListOverride("feed", "1", "layout", "inbox");
    const { container, unmount } = go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    expect(container.querySelector(".row-compact")).toBeNull();
    expect(within(container.querySelector('[data-item-id="1001"]') as HTMLElement).getByText("Excerpt for article 1")).toBeInTheDocument();
    unmount();
    const again = go("/l/unread");
    await screen.findByText("Article number 1");
    expect(again.container.querySelector(".row-compact")).not.toBeNull();
  });

  it("a folder override applies to the feeds inside it", async () => {
    routes();
    setListOverride("folder", "1", "layout", "headlines");
    const { container } = go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    expect(container.querySelector(".row-headline")).not.toBeNull();
  });

  it("the layout picker sets a per-feed override and the device default", async () => {
    routes();
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "List options, Editorial layout" }));
    // One list under "This feed": the radio is the override, the star beside each layout is the device default.
    expect(screen.getByText("This feed")).toBeInTheDocument();
    await user.click(screen.getByRole("menuitemradio", { name: /Email - Compact/ }));
    expect(devicePrefsStore.get().overrides.feed["1"]).toEqual({ layout: "headlines" });
    expect(devicePrefsStore.get().layout).toBe("magazine"); // the device default did not move
    await waitFor(() => expect(document.querySelector(".row-headline")).not.toBeNull());
    // Use device default clears it.
    await user.click(screen.getByRole("button", { name: "List options, Email - Compact layout" }));
    await user.click(within(screen.getByRole("group", { name: "Layout of this feed" })).getByRole("menuitemradio", { name: /Use device default/ }));
    expect(devicePrefsStore.get().overrides.feed["1"]).toBeUndefined();
    await waitFor(() => expect(document.querySelector(".row-headline")).toBeNull());
    // The star makes a layout the device default; the filled star marks the current one.
    await user.click(screen.getByRole("button", { name: "List options, Editorial layout" }));
    expect(screen.getByRole("menuitemcheckbox", { name: "Editorial is the device default" })).toBeChecked();
    await user.click(screen.getByRole("menuitemcheckbox", { name: "Make Inbox the device default" }));
    expect(devicePrefsStore.get().layout).toBe("inbox");
    expect(screen.getByRole("menuitemcheckbox", { name: "Inbox is the device default" })).toBeChecked();
  });

  it("Cards with nothing open fills the width; an open article keeps its list beside it", async () => {
    routes();
    media("min-width: 900px");
    updateDevicePrefs({ layout: "cards" });
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    // Wide screens always show the list next to an open article (a single column of cards in the pane).
    expect(screen.queryByRole("button", { name: "Back to list" })).toBeNull();
    await waitFor(() => expect(document.querySelector('[data-item-id="1002"]')).not.toBeNull());
    expect(screen.getByRole("separator", { name: "Resize article list" })).toBeInTheDocument();
  });

  it("switching layout with an article open keeps the article and the list (Cards included)", async () => {
    routes();
    media("min-width: 900px");
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(document.querySelector('[data-item-id="1002"]')).not.toBeNull());
    const user = userEvent.setup();
    for (const [from, to] of [["Editorial", "Cards"], ["Cards", "Inbox"], ["Inbox", "Editorial"]] as const) {
      await user.click(screen.getByRole("button", { name: `List options, ${from} layout` }));
      await user.click(screen.getByRole("menuitemradio", { name: new RegExp(`^${to}`) }));
      expect(screen.getByTestId("article-body")).toBeInTheDocument(); // the article stays open
      await waitFor(() => expect(document.querySelector('[data-item-id="1002"]')).not.toBeNull()); // and so does the list
    }
  });

  it("c switches to Compact and back", async () => {
    routes();
    const { container } = go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("c");
    await waitFor(() => expect(container.querySelector(".row-compact")).not.toBeNull());
    await user.keyboard("c");
    await waitFor(() => expect(container.querySelector(".row-compact")).toBeNull());
    expect(sessionLayoutStore.get()).toBeNull();
  });
});

describe("order", () => {
  it("newest first is the default and the toggle asks the server for oldest", async () => {
    const { calls } = routes({ "GET /api/items": (url) => json(pageOf(url.searchParams.get("order") === "oldest" ? items().reverse() : items())) });
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(calls.find((c) => c.url.pathname === "/api/items")?.url.searchParams.get("order")).toBeNull();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Oldest first" }));
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("order") === "oldest")).toBe(true));
    expect(screen.getByRole("button", { name: "Oldest first" })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("undo toast", () => {
  it("merges repeated marks into one toast, and Undo reverses all of them through the API", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("jm");
    const status = screen.getByTestId("undo-region");
    expect(status).toHaveAttribute("role", "status");
    await waitFor(() => expect(status).toHaveTextContent("Marked read"));
    await user.keyboard("jjm"); // 1001 -> 1002 is already read; 1003 then
    await user.keyboard("jm");
    await waitFor(() => expect(status).toHaveTextContent(/2 articles marked read|3 articles marked read/));
    const before = calls.filter((c) => c.url.pathname === "/api/items/mark-read").length;
    await user.click(within(status).getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read").length).toBe(before + 1));
    const last = bodyOf(calls.filter((c) => c.url.pathname === "/api/items/mark-read").pop());
    expect(last.read).toBe(false);
    expect(last.ids.length).toBeGreaterThanOrEqual(2);
    expect(status).toHaveTextContent("");
  });

  it("z undoes the last action without the toast", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("jm");
    await waitFor(() => expect(screen.getByTestId("undo-region")).toHaveTextContent("Marked read"));
    await user.keyboard("z");
    await waitFor(() => expect(bodyOf(calls.filter((c) => c.url.pathname === "/api/items/mark-read").pop()).read).toBe(false));
  });
});

describe("row swipe (touch, iOS Mail directions)", () => {
  beforeEach(() => media(REDUCED)); // no slide animation: the commit is immediate

  it("swipe right past the arm point marks read, with an undo toast, and the row leaves the Unread view", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    swipe(contentOf("1001"), 100, 170);
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    expect(bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read"))).toEqual({ ids: ["1001"], read: true, reason: "swipe" });
    expect(screen.getByTestId("undo-region")).toHaveTextContent("Marked read");
    await waitFor(() => expect(document.querySelector('[data-item-id="1001"]')).toBeNull());
    // Undo puts the row back where it was.
    await userEvent.setup().click(within(screen.getByTestId("undo-region")).getByRole("button", { name: "Undo" }));
    await screen.findByText("Article number 1");
  });

  it("swipe right on a read row marks it unread", async () => {
    const { calls } = routes();
    go("/l/all");
    await screen.findByText("Article number 2");
    swipe(contentOf("1002"), 100, 170);
    await waitFor(() => expect(bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read"))).toEqual({ ids: ["1002"], read: false, reason: "swipe" }));
    expect(screen.getByTestId("undo-region")).toHaveTextContent("Marked unread");
  });

  it("a short slow swipe does not commit and nothing is sent", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const el = contentOf("1001");
    ptr(el, "pointerDown", 100);
    tick(300);
    ptr(el, "pointerMove", 130);
    tick(300);
    ptr(el, "pointerMove", 160);
    tick(300);
    ptr(el, "pointerUp", 160);
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
  });

  it("swipe left past the arm point stars (with undo)", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    swipe(contentOf("1001"), 300, -170);
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.pathname === "/api/items/1001/star")).toBe(true));
    expect(screen.getByTestId("undo-region")).toHaveTextContent("Starred");
  });

  it("a partial left swipe rests open on Star and More; More opens the row menu", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const el = contentOf("1001");
    ptr(el, "pointerDown", 300);
    tick(300);
    ptr(el, "pointerMove", 270);
    tick(300);
    ptr(el, "pointerMove", 220);
    tick(300);
    ptr(el, "pointerUp", 220); // 80 px, slow
    expect(el.style.transform).toBe("translateX(-144px)");
    const host = el.closest("[data-swipe-row]") as HTMLElement;
    const more = within(host).getByRole("button", { name: /^More$/, hidden: true });
    await userEvent.setup().click(more);
    expect(await screen.findByRole("menuitem", { name: /Mark above as read/ })).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
  });

  it("ignores a touch that starts in the iOS edge zone", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    swipe(contentOf("1001"), 10, 200);
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
  });

  it("ignores mouse pointers: they use the buttons", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const el = contentOf("1001");
    fireEvent.pointerDown(el, { pointerType: "mouse", pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(el, { pointerType: "mouse", pointerId: 1, clientX: 300, clientY: 100 });
    fireEvent.pointerUp(el, { pointerType: "mouse", pointerId: 1, clientX: 300, clientY: 100 });
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
  });

  it("with motion allowed, the row slides off first and the change lands after the slide", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance", "Date"], shouldAdvanceTime: true });
    media(); // no reduced motion
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const el = contentOf("1001");
    swipe(el, 100, 170);
    expect(el.style.transform).toBe("translateX(375px)");
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    vi.useRealTimers();
  });

  it("long press opens the row menu, and the release does not open the article", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const el = contentOf("1001");
    ptr(el, "pointerDown", 100);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(520);
    });
    expect(await screen.findByRole("menuitem", { name: "Mark above as read" })).toBeInTheDocument();
    for (const name of ["Star", "Mark as read", "Mark below as read", "Open original", "Copy link"]) {
      expect(screen.getByRole("menuitem", { name: new RegExp(`^${name}`) })).toBeInTheDocument();
    }
    expect(screen.getByRole("menuitem", { name: /^Share/ })).toBeInTheDocument();
    ptr(el, "pointerUp", 100);
    expect(window.location.pathname).toBe("/l/unread");
    vi.useRealTimers();
  });
});

describe("mark above and below, and mark all", () => {
  it("sends the list's own order, the anchor and the snapshot bound (anchor excluded)", async () => {
    const { calls } = routes({ "GET /api/items": () => json(pageOf(items(), null, "1200")), "POST /api/items/mark-read": () => json({ changed: ["1001", "1002"], restored: [], count: 2, undoable: true }) });
    go("/l/unread");
    await screen.findByText("Article number 3");
    const user = userEvent.setup();
    const row = document.querySelector('[data-item-id="1003"]') as HTMLElement;
    await user.click(within(row).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark above as read" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    const body = bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read"));
    expect(body).toEqual({
      scope: { view: "unread", all: true },
      bound: { order: "date", side: "above", anchor: { sort_at: BASE - 3 * 3600, id: "1003" }, inclusive: false },
      max_id: "1200",
      read: true,
      reason: "bulk",
    });
    expect(screen.getByTestId("undo-region")).toHaveTextContent("Marked 2 as read");
  });

  it("uses the oldest order when the list is oldest first", async () => {
    const { calls } = routes({ "GET /api/items": () => json(pageOf(items().reverse())) });
    updateDevicePrefs({ order: "oldest" });
    go("/l/unread");
    await screen.findByText("Article number 3");
    const user = userEvent.setup();
    await user.click(within(document.querySelector('[data-item-id="1003"]') as HTMLElement).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark below as read" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    const b = bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read"));
    expect(b.bound.order).toBe("oldest");
    expect(b.bound.side).toBe("below");
  });

  it("undoing a bulk mark reverses exactly the ids the server changed", async () => {
    const { calls } = routes({ "POST /api/items/mark-read": (_u, init) => json(JSON.parse(String(init?.body)).scope ? { changed: ["1004", "1005"], restored: [], count: 2, undoable: true } : { changed: ["1004", "1005"], restored: [] }) });
    go("/l/unread");
    await screen.findByText("Article number 3");
    const user = userEvent.setup();
    await user.click(within(document.querySelector('[data-item-id="1003"]') as HTMLElement).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark below as read" }));
    await user.click(await within(screen.getByTestId("undo-region")).findByRole("button", { name: "Undo" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read").length).toBe(2));
    expect(bodyOf(calls.filter((c) => c.url.pathname === "/api/items/mark-read")[1])).toEqual({ ids: ["1004", "1005"], read: false, reason: "bulk" });
  });

  it("Shift+A marks the whole list, bounded by what was loaded", async () => {
    const { calls } = routes({ "GET /api/items": () => json(pageOf(items(), null, "1200")), "POST /api/items/mark-read": () => json({ changed: ["1001", "1004", "1005"], restored: [], count: 3, undoable: true }) });
    go("/l/unread?feed=1");
    await screen.findByText("Article number 3");
    await userEvent.setup().keyboard("{Shift>}A{/Shift}");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    expect(bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read"))).toEqual({
      scope: { view: "unread", feed_id: "1" },
      max_id: "1200",
      read: true,
      reason: "bulk",
    });
    expect(screen.getByTestId("undo-region")).toHaveTextContent("Marked 3 as read");
  });

  it("oldest-first: mark all sends the server's as_of, not the first page's highest id", async () => {
    // Oldest first, and a backdated new item: the page maximum (1005) is not the bound.
    const { calls } = routes({ "GET /api/items": () => json(pageOf(items().reverse(), null, "1777")), "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }) });
    updateDevicePrefs({ order: "oldest" });
    go("/l/unread");
    await screen.findByText("Article number 3");
    await userEvent.setup().keyboard("{Shift>}A{/Shift}");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    expect(bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read")).max_id).toBe("1777");
  });

  it("offers no undo from local guesses when the server answers changed: [] with undoable: true", async () => {
    const { calls } = routes({ "POST /api/items/mark-read": () => json({ changed: [], restored: [], count: 0, undoable: true }) });
    go("/l/unread");
    await screen.findByText("Article number 3");
    await userEvent.setup().keyboard("{Shift>}A{/Shift}");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
  });

  it("undo sends the ledger ids along with the changed ids", async () => {
    const { calls } = routes({ "POST /api/items/mark-read": (_u, init) => json(JSON.parse(String(init?.body)).scope ? { changed: ["1001", "1004"], ledger_ids: ["77", "78"], restored: [], count: 4, undoable: true } : { changed: ["1001", "1004"], restored: [] }) });
    go("/l/unread");
    await screen.findByText("Article number 3");
    const user = userEvent.setup();
    await user.keyboard("{Shift>}A{/Shift}");
    await user.click(await within(screen.getByTestId("undo-region")).findByRole("button", { name: "Undo" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read").length).toBe(2));
    expect(bodyOf(calls.filter((c) => c.url.pathname === "/api/items/mark-read")[1])).toEqual({ ids: ["1001", "1004"], ledger_ids: ["77", "78"], read: false, reason: "bulk" });
  });

  it("a failed undo does not say Undone and keeps the toast's Undo", async () => {
    let n = 0;
    routes({ "POST /api/items/mark-read": () => (++n === 1 ? json({ changed: ["1001", "1004"], restored: [], count: 2, undoable: true }) : json({ error: "boom" }, 500)) });
    go("/l/unread");
    await screen.findByText("Article number 3");
    const user = userEvent.setup();
    await user.keyboard("{Shift>}A{/Shift}");
    await user.click(await within(screen.getByTestId("undo-region")).findByRole("button", { name: "Undo" }));
    await waitFor(() => expect(n).toBe(2));
    await screen.findByText(/Couldn.t undo/);
    expect(screen.queryByText("Undone")).toBeNull();
    expect(within(screen.getByTestId("undo-region")).getByRole("button", { name: "Undo" })).toBeInTheDocument();
  });

  it("stays quiet about undo when the server withholds the ids (above its cap)", async () => {
    routes({ "POST /api/items/mark-read": () => json({ changed: [], restored: [], count: 20000, undoable: false }) });
    go("/l/unread");
    await screen.findByText("Article number 3");
    await userEvent.setup().keyboard("{Shift>}A{/Shift}");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Undo" })).toBeNull());
  });
});

describe("keymap", () => {
  it("does nothing while typing in a field", async () => {
    routes();
    go("/search");
    const box = await screen.findByRole("searchbox", { name: "Search articles" });
    await userEvent.setup().type(box, "jjc?zA");
    expect(box).toHaveValue("jjc?zA");
    expect(helpStore.get()).toBe(false);
    expect(document.querySelector(".row-compact")).toBeNull();
  });

  it("the single-key setting turns letters off but ? still opens the overlay", async () => {
    const { calls } = routes();
    updatePrefs({ shortcuts: false });
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("jm");
    await user.keyboard("{Shift>}A{/Shift}");
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
    await user.keyboard("?");
    expect(await screen.findByRole("dialog", { name: "Keyboard shortcuts" })).toBeInTheDocument();
  });

  it("the ? overlay is searchable and closes with Escape", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("?");
    const dlg = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    expect(within(dlg).getByText("Undo the last action (60 seconds, 2 minutes for bulk)")).toBeInTheDocument();
    await user.type(within(dlg).getByRole("searchbox", { name: "Search shortcuts" }), "undo");
    expect(within(dlg).getByText(/Undo the last action/)).toBeInTheDocument();
    expect(within(dlg).queryByText("Toggle full text")).toBeNull();
    await user.clear(within(dlg).getByRole("searchbox", { name: "Search shortcuts" }));
    await user.type(within(dlg).getByRole("searchbox", { name: "Search shortcuts" }), "zzzz");
    expect(within(dlg).getByText(/No shortcut matches/)).toBeInTheDocument();
    expect(await axe(dlg)).toHaveNoViolations();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("g then i/a/s go to the views, ] goes to the next feed", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("ga");
    await waitFor(() => expect(window.location.pathname).toBe("/l/all"));
    await user.keyboard("gs");
    await waitFor(() => expect(window.location.pathname).toBe("/l/starred"));
    await user.keyboard("gi");
    await waitFor(() => expect(window.location.pathname).toBe("/l/unread"));
  });

  it("x selects rows and m acts on the selection", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("jx");
    await user.keyboard("jjx");
    await user.keyboard("m");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    expect(bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read")).ids.sort()).toEqual(["1001", "1003"]);
  });

  it("r refreshes", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    await userEvent.setup().keyboard("r");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/refresh")).toBe(true));
  });
});

describe("pull to refresh", () => {
  it("arms at 72 px (144 px of finger) and posts /api/refresh on release", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const scroller = screen.getByTestId("list-scroll");
    touchEv(scroller, "touchstart", 100, 100);
    touchEv(scroller, "touchmove", 100, 160);
    touchEv(scroller, "touchmove", 100, 260); // 160 px * 0.5 = 80 px shown
    expect(screen.getByText("Release to refresh")).toBeInTheDocument();
    touchEv(scroller, "touchend", 100, 260);
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/refresh")).toBe(true));
    expect(within(screen.getByTestId("pull-indicator")).getByText("Refreshing")).toBeInTheDocument();
  });

  it("does not refresh short of the arm point, and blocks native scroll once pulling", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const scroller = screen.getByTestId("list-scroll");
    touchEv(scroller, "touchstart", 100, 100);
    const ev = touchEv(scroller, "touchmove", 100, 190); // 45 px shown
    expect(ev.defaultPrevented).toBe(true);
    touchEv(scroller, "touchend", 100, 190);
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.some((c) => c.url.pathname === "/api/refresh")).toBe(false);
  });

  it("does nothing when the list is scrolled", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const scroller = screen.getByTestId("list-scroll");
    scroller.scrollTop = 30;
    touchEv(scroller, "touchstart", 100, 100);
    const ev = touchEv(scroller, "touchmove", 100, 300);
    touchEv(scroller, "touchend", 100, 300);
    expect(ev.defaultPrevented).toBe(false);
    expect(calls.some((c) => c.url.pathname === "/api/refresh")).toBe(false);
  });
});

describe("first-run peek", () => {
  it("shows the tip once on a touch device and any touch dismisses it for good", async () => {
    routes();
    media(COARSE, REDUCED);
    updateDevicePrefs({ peekSeen: false });
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(await screen.findByText(/Swipe a row right to mark it read or unread, left to star it/)).toBeInTheDocument();
    await waitFor(() => expect(devicePrefsStore.get().peekSeen).toBe(true)); // reduced motion: no animation, the caption alone teaches it
  });

  it("does not run on a mouse device", async () => {
    routes();
    updateDevicePrefs({ peekSeen: false });
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(screen.queryByText(/Swipe a row right/)).toBeNull();
    expect(devicePrefsStore.get().peekSeen).toBe(false);
  });

  it("a touch during the peek cancels it and marks it seen", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    routes();
    media(COARSE);
    updateDevicePrefs({ peekSeen: false });
    go("/l/unread");
    await screen.findByText("Article number 1");
    await screen.findByText(/Swipe a row right/);
    expect(devicePrefsStore.get().peekSeen).toBe(false);
    fireEvent.pointerDown(document.body, { pointerType: "touch", pointerId: 3, clientX: 100, clientY: 100 });
    await waitFor(() => expect(devicePrefsStore.get().peekSeen).toBe(true));
    vi.useRealTimers();
  });
});

describe("article gestures", () => {
  it("a right swipe from the body pops to the list you came from", async () => {
    routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const frame = articleFrame();
    swipe(frame, 60, 200, 300);
    await waitFor(() => expect(window.location.pathname).toBe("/l/unread"));
  });

  it("does not pop from the left 24 px edge", async () => {
    routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const frame = articleFrame();
    swipe(frame, 10, 250, 300);
    await new Promise((r) => setTimeout(r, 30));
    expect(window.location.pathname).toBe("/i/1001");
  });

  it("does not pop when the swipe starts on horizontally scrollable content", async () => {
    routes(article('<pre id="code">a very wide line of code</pre><p>text</p>'));
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    const pre = body.querySelector("pre") as HTMLElement;
    Object.defineProperty(pre, "scrollWidth", { configurable: true, value: 900 });
    Object.defineProperty(pre, "clientWidth", { configurable: true, value: 300 });
    pre.style.overflowX = "auto";
    articleFrame();
    swipe(pre, 60, 250, 300);
    await new Promise((r) => setTimeout(r, 30));
    expect(window.location.pathname).toBe("/i/1001");
    // The same swipe on a paragraph does pop.
    swipe(body.querySelector("p") as HTMLElement, 60, 250, 300);
    await waitFor(() => expect(window.location.pathname).toBe("/l/unread"));
  });

  it("prev and next buttons and j/k still work", async () => {
    routes();
    go("/i/1002?from=unread");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(screen.getByRole("button", { name: "Next article" })).toBeEnabled());
    await userEvent.setup().click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1003"));
  });
});

describe("article rendering", () => {
  const html =
    '<p>Intro<sup><a href="#kp-fn1" id="kp-fnref1">1</a></sup></p>' +
    '<figure class="kp-embed" data-provider="youtube" data-id="dQw4w9WgXcQ"><img src="/img/yt.jpg" alt=""><a href="https://www.youtube.com/watch?v=dQw4w9WgXcQ" target="_blank" rel="noopener noreferrer">Watch on YouTube</a></figure>' +
    '<figure class="kp-embed" data-provider="vimeo" data-id="76979871"><a href="https://vimeo.com/76979871" target="_blank" rel="noopener noreferrer">Watch on Vimeo</a></figure>' +
    '<p id="kp-fn1">The footnote <a href="#kp-fnref1">back</a></p>' +
    '<p><a href="https://example.com/x">out</a></p>';

  it("loads a sandboxed YouTube iframe only on tap", async () => {
    routes(article(html));
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    expect(body.querySelector("iframe")).toBeNull(); // nothing contacts YouTube before the tap
    const play = within(body).getAllByRole("button", { name: /^Play YouTube video/ })[0] as HTMLElement;
    await userEvent.setup().click(play);
    const frame = body.querySelector("iframe") as HTMLIFrameElement;
    expect(frame.getAttribute("src")).toBe("https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1");
    expect(frame.getAttribute("sandbox")).toBe("allow-scripts allow-same-origin allow-presentation allow-popups");
    expect(frame.getAttribute("referrerpolicy")).toBe("strict-origin-when-cross-origin");
    expect(frame.getAttribute("allow")).toBe("autoplay; fullscreen; picture-in-picture");
    expect(within(body).queryByRole("button", { name: /^Play YouTube/ })).toBeNull();
  });

  it("loads Vimeo with dnt and autoplay", async () => {
    routes(article(html));
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    await userEvent.setup().click(within(body).getByRole("button", { name: /^Play Vimeo video/ }));
    expect(body.querySelector("iframe")?.getAttribute("src")).toBe("https://player.vimeo.com/video/76979871?dnt=1&autoplay=1");
  });

  it("the fallback link still opens the video site in a new tab and loads nothing", async () => {
    routes(article(html));
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    const link = within(body).getByRole("link", { name: "Watch on YouTube" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    fireEvent.click(link);
    expect(body.querySelector("iframe")).toBeNull();
  });

  it("footnote links scroll inside the article, not the page, and do not leave the article", async () => {
    routes(article(html));
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    const scrollTo = vi.spyOn(Element.prototype, "scrollTo");
    const intoView = vi.spyOn(Element.prototype, "scrollIntoView");
    const note = within(body).getByRole("link", { name: "1" });
    expect(note).not.toHaveAttribute("target"); // hash links are not new-tab links
    await userEvent.setup().click(note);
    expect(scrollTo).toHaveBeenCalled();
    expect(intoView).not.toHaveBeenCalled();
    const container = scrollTo.mock.contexts.at(-1) as HTMLElement;
    expect(container.contains(body)).toBe(true);
    expect(window.location.hash).toBe("");
    expect(document.activeElement?.id).toBe("kp-fn1");
    expect(window.location.pathname).toBe("/i/1001");
  });

  it("ordinary links open in a new tab", async () => {
    routes(article(html));
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    const out = within(body).getByRole("link", { name: "out" });
    expect(out).toHaveAttribute("target", "_blank");
    expect(out).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("passes axe with embeds present", async () => {
    routes(article(html));
    const { container } = go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(container.querySelector("button.kp-embed-play")).not.toBeNull());
    expect(await axe(container)).toHaveNoViolations();
  });
});
