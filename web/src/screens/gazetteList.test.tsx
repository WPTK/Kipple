import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PAPER_NAME } from "@/layouts/gazette";
import { LEAD_WINDOW } from "@/layouts/gazettePlan";
import { devicePrefsStore, resetDevicePrefs, setListOverride, updateDevicePrefs } from "@/lib/devicePrefs";
import { profileOf } from "@/lib/deviceSync";
import { updatePrefs, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { themeStore } from "@/theme/theme";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { LEAVE_MS, NO_RANGE_IN_PAGES, clearListMemory, resetScrollReadForTests } from "./ListPane";

// The Gazette as the list screen draws it: the fetch, the paper's name, reading in place, keys and loading more.

class NoES {
  addEventListener() {}
  close() {}
}

const IMG = "/img/x.jpg";
const BASE = 1_790_000_000;
const at = (n: number) => ({ published_at: BASE - n * 3600, sort_at: BASE - n * 3600 });
const many = (from: number, n: number) => Array.from({ length: n }, (_, i) => card(from + i, { image: (from + i) % 3 === 0 ? IMG : null, ...at(from + i) }));
const few = () => many(1, 8);

function routes(items: (url: URL) => ReturnType<typeof pageOf> = () => pageOf(few()), extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": (u) => json(items(u)),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

/** Story ids in DOM order, which is the paper's reading order. */
const storyIds = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>("article[data-item-id]")].map((a) => a.dataset.itemId);
const itemCalls = (calls: { method: string; url: URL }[]) => calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items");

beforeEach(() => {
  clearToasts();
  clearListMemory();
  resetScrollReadForTests();
  updatePrefs({ markReadOnScroll: false });
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updatePrefs({ shortcuts: true });
  updateDevicePrefs({ peekSeen: true, layout: "gazette" });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Gazette fetch order", () => {
  it("asks for newest first even when this device reads oldest first", async () => {
    updateDevicePrefs({ order: "oldest" });
    const { calls } = routes();
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const reqs = itemCalls(calls);
    expect(reqs.length).toBeGreaterThan(0);
    for (const r of reqs) expect(r.url.searchParams.get("order")).toBeNull();
    // The order toggle is not offered: it would do nothing here.
    expect(screen.queryByRole("button", { name: "Oldest first" })).toBeNull();
  });

  it("asks for newest first when the feed itself is set to oldest first", async () => {
    setListOverride("feed", "1", "order", "oldest");
    setListOverride("feed", "1", "layout", "gazette");
    updateDevicePrefs({ layout: "magazine" });
    const { calls } = routes();
    go("/l/all?feed=1");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    for (const r of itemCalls(calls)) expect(r.url.searchParams.get("order")).toBeNull();
  });

  it("the same settings in another layout still ask for oldest first", async () => {
    updateDevicePrefs({ order: "oldest", layout: "magazine" });
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(itemCalls(calls).every((r) => r.url.searchParams.get("order") === "oldest")).toBe(true);
    expect(screen.getByRole("button", { name: "Oldest first" })).toHaveAttribute("aria-pressed", "true");
  });

  it("the list options menu says the Gazette is always newest first", async () => {
    routes();
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    await userEvent.click(screen.getByRole("button", { name: /^List options, Gazette layout/ }));
    expect(await screen.findByText(/The Gazette is always newest first/)).toBeInTheDocument();
  });
});

describe("Gazette on the list screen", () => {
  it("loads more until the front page can be planned, then prints it", async () => {
    const first = many(1, 50);
    const second = many(51, LEAD_WINDOW - 50 + 30);
    const { calls } = routes((u) => (u.searchParams.get("cursor") === "c1" ? pageOf(second) : pageOf(first, "c1")));
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    expect(itemCalls(calls).filter((c) => c.url.searchParams.get("cursor") === "c1")).toHaveLength(1);
    expect(new Set(storyIds(container)).size).toBe(first.length + second.length);
    expect(screen.getByText("That's the Gazette.")).toBeInTheDocument();
  });

  it("prints the name this device gave the paper", async () => {
    updateDevicePrefs({ paperName: "Morning Notes" });
    routes();
    go("/l/unread");
    expect(await screen.findByRole("heading", { name: "Morning Notes" })).toBeInTheDocument();
    expect(screen.getByText("That's Morning Notes.")).toBeInTheDocument();
  });

  it("j and k follow the reading order, and a story marked read stays where it is", async () => {
    routes();
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const order = storyIds(container);
    const list = screen.getByTestId("list-scroll");
    const press = (key: string) => act(() => void fireEvent.keyDown(list, { key }));
    press("j");
    press("j");
    await waitFor(() => expect(container.querySelector("[data-selected]")?.getAttribute("data-item-id")).toBe(order[1]));
    press("k");
    await waitFor(() => expect(container.querySelector("[data-selected]")?.getAttribute("data-item-id")).toBe(order[0]));
    press("m");
    const story = () => container.querySelector(`article[data-item-id="${order[0]}"]`);
    await waitFor(() => expect(story()).toHaveAttribute("data-read", "true"));
    // In the Unread list a row marked read leaves after LEAVE_MS; a story of the paper fades in place instead.
    await act(() => new Promise((r) => setTimeout(r, LEAVE_MS + 300)));
    expect(story()).not.toBeNull();
    expect(storyIds(container)).toEqual(order);
  });

  it("mark above and below are off: the page is not in date order", async () => {
    const { calls } = routes();
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const list = screen.getByTestId("list-scroll");
    const press = (key: string) => act(() => void fireEvent.keyDown(list, { key }));
    press("j");
    await waitFor(() => expect(container.querySelector("[data-selected]")).not.toBeNull());
    press("{");
    press("}");
    await waitFor(() => expect(screen.getByTestId("live-region")).toHaveTextContent(NO_RANGE_IN_PAGES));
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    expect(storyIds(container).every((id) => !container.querySelector(`article[data-item-id="${id}"]`)?.hasAttribute("data-read"))).toBe(true);
  });

  it("a story ticked with x shows its tick", async () => {
    routes();
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const order = storyIds(container);
    const list = screen.getByTestId("list-scroll");
    act(() => void fireEvent.keyDown(list, { key: "j" }));
    act(() => void fireEvent.keyDown(list, { key: "x" }));
    const story = await waitFor(() => {
      const s = container.querySelector(`article[data-item-id="${order[0]}"]`);
      expect(s).toHaveAttribute("data-checked", "true");
      return s!;
    });
    expect(story.querySelector("svg.lucide-check")).not.toBeNull();
    // Mark above and below mark nothing here, so they leave the tick alone too.
    act(() => void fireEvent.keyDown(list, { key: "{" }));
    act(() => void fireEvent.keyDown(list, { key: "}" }));
    await waitFor(() => expect(screen.getByTestId("live-region")).toHaveTextContent(NO_RANGE_IN_PAGES));
    expect(story).toHaveAttribute("data-checked", "true");
    act(() => void fireEvent.keyDown(list, { key: "x" }));
    await waitFor(() => expect(story).not.toHaveAttribute("data-checked"));
  });

  it("a search shows rows, not a paper", async () => {
    routes(() => pageOf(few()), { "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    const { container } = go("/search?q=article");
    // Relevance order: Editorial rows under one "Best matches first" header.
    await screen.findByText("Best matches first");
    expect(container.querySelectorAll("[data-index]").length).toBeGreaterThan(1);
    expect(container.querySelector("[data-gazette]")).toBeNull();
  });

  it("passes axe in a dark and a very light theme", async () => {
    routes();
    for (const scheme of ["midnight", "paper"]) {
      themeStore.set((t) => ({ ...t, mode: "fixed", fixed: scheme }));
      const { container, unmount } = go("/l/unread");
      await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
      expect(await axe(container)).toHaveNoViolations();
      unmount();
    }
  });
});

describe("coming back to the Gazette", () => {
  // jsdom has no layout: the page's height is what the test says it is.
  let tall = 20_000;
  beforeEach(() => {
    tall = 20_000;
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", { configurable: true, get: () => tall });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 800 });
  });
  afterEach(() => {
    delete (HTMLElement.prototype as { scrollHeight?: number }).scrollHeight;
    delete (HTMLElement.prototype as { clientHeight?: number }).clientHeight;
  });

  /** Read the paper down to 5000 px, leave, and come back to a page that is only `short` px tall. */
  async function leaveAt5000AndReturn(short: number) {
    routes();
    const first = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const s1 = screen.getByTestId("list-scroll");
    act(() => {
      s1.scrollTop = 5000;
      s1.dispatchEvent(new Event("scroll"));
    });
    first.unmount();
    tall = short;
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    return screen.getByTestId("list-scroll");
  }

  it("puts the offset back once the pages are tall enough to hold it", async () => {
    const scroller = await leaveAt5000AndReturn(1000);
    expect(scroller.scrollTop).toBe(0);
    tall = 20_000; // more pages loaded
    act(() => updateDevicePrefs({ paperName: "Later" }));
    await screen.findByRole("heading", { name: "Later" });
    expect(scroller.scrollTop).toBe(5000);
  });

  it("never jumps after a scroll that came from elsewhere (the article's next button, find in page)", async () => {
    const scroller = await leaveAt5000AndReturn(1000);
    act(() => {
      scroller.scrollTop = 200;
      scroller.dispatchEvent(new Event("scroll"));
    });
    tall = 20_000;
    act(() => updateDevicePrefs({ paperName: "Later" }));
    await screen.findByRole("heading", { name: "Later" });
    expect(scroller.scrollTop).toBe(200);
  });

  it("never jumps once the reader has scrolled the shorter page", async () => {
    const scroller = await leaveAt5000AndReturn(1000);
    act(() => {
      fireEvent.wheel(scroller, { deltaY: 300 });
      scroller.scrollTop = 300;
      scroller.dispatchEvent(new Event("scroll"));
    });
    tall = 20_000;
    act(() => updateDevicePrefs({ paperName: "Later" }));
    await screen.findByRole("heading", { name: "Later" });
    expect(scroller.scrollTop).toBe(300);
  });
});

describe("mark as read while scrolling in the Gazette", () => {
  // Stories `story` px tall, one under the other in DOM order; the list is 800 px tall from y = 0.
  let story = 100;
  beforeEach(() => {
    story = 100;
    updatePrefs({ markReadOnScroll: true });
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const all = [...document.querySelectorAll("article[data-item-id]")];
      const i = all.indexOf(this);
      const scroll = document.querySelector<HTMLElement>('[data-testid="list-scroll"]')?.scrollTop ?? 0;
      // The pages' box is as tall as its stories.
      const pages = this instanceof HTMLElement && this.hasAttribute("data-page-box");
      const top = i < 0 ? (pages ? -scroll : 0) : i * story - scroll;
      const bottom = i < 0 ? (pages ? top + all.length * story : 800) : top + story;
      return { x: 0, y: top, top, bottom, left: 0, right: 375, width: 375, height: bottom - top, toJSON() {} } as DOMRect;
    });
    // A ResizeObserver the test can fire again, as a browser does whenever an observed box changes size.
    observers.clear();
    vi.stubGlobal(
      "ResizeObserver",
      class {
        el: Element | null = null;
        constructor(public cb: ResizeObserverCallback) {}
        observe(el: Element) {
          this.el = el;
          observers.add(this);
          this.fire();
        }
        fire() {
          if (this.el) this.cb([{ target: this.el, contentRect: this.el.getBoundingClientRect() } as ResizeObserverEntry], this as unknown as ResizeObserver);
        }
        unobserve() {}
        disconnect() {
          observers.delete(this);
        }
      },
    );
  });
  const observers = new Set<{ fire: () => void }>();
  /** The page's boxes changed size (it was laid out, more pages came, the text size changed). */
  const resized = () => act(() => observers.forEach((o) => o.fire()));
  /** One animation frame, in which the list looks at the stories after a scroll. */
  const frame = () => act(() => new Promise((r) => requestAnimationFrame(() => r(undefined))));

  const marked = (calls: { method: string; url: URL; init?: RequestInit }[]) =>
    calls
      .filter((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")
      .map((c) => JSON.parse(String(c.init?.body)) as { ids: string[]; reason: string });
  /** The reader scrolls the list to `y`. */
  const scrollTo = (y: number) =>
    act(() => {
      const s = screen.getByTestId("list-scroll");
      s.scrollTop = y;
      s.dispatchEvent(new Event("scroll"));
    });
  const settle = () => act(() => new Promise((r) => setTimeout(r, 900)));

  it("marks the stories that were on screen and then scrolled above the top, and no others", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const order = storyIds(container);
    expect(order).toHaveLength(20);
    resized();
    // A jump straight to 1500 px: stories 0 to 7 were on screen, 8 to 14 never were, and all of them are above the top.
    scrollTo(1500);
    await waitFor(() => expect(marked(calls)).toHaveLength(1), { timeout: 3000 });
    expect(marked(calls)[0]?.reason).toBe("scroll");
    expect(new Set(marked(calls)[0]?.ids)).toEqual(new Set(order.slice(0, 8)));
  });

  it("marks what was scrolled past just before the list closes", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    const view = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const order = storyIds(view.container);
    resized();
    scrollTo(1500);
    await frame();
    view.unmount(); // a phone opening a story, well within the settle time
    await waitFor(() => expect(marked(calls)).toHaveLength(1));
    expect(new Set(marked(calls)[0]?.ids)).toEqual(new Set(order.slice(0, 8)));
  });

  it("a page that reflowed while away marks nothing when its position comes back", async () => {
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", { configurable: true, get: () => 20_000 });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 800 });
    try {
      const { calls } = routes(() => pageOf(many(1, 20)));
      const first = go("/l/unread");
      await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
      const order = storyIds(first.container);
      // The reader stops at 1000 px: stories 10 to 17 are on screen, unread.
      resized();
      scrollTo(1000);
      await settle();
      first.unmount();
      // A rotation or a text size change while away: the stories are half as tall, so 10 to 17 now sit above 1000 px.
      story = 50;
      go("/l/unread");
      await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
      const scroller = screen.getByTestId("list-scroll");
      expect(scroller.scrollTop).toBe(1000);
      // The browser reports the restore's own scroll.
      act(() => void scroller.dispatchEvent(new Event("scroll")));
      await settle();
      const ids = marked(calls).flatMap((m) => m.ids);
      expect(ids.some((id) => order.slice(10, 18).includes(id!))).toBe(false);
    } finally {
      delete (HTMLElement.prototype as { scrollHeight?: number }).scrollHeight;
      delete (HTMLElement.prototype as { clientHeight?: number }).clientHeight;
    }
  });

  it("switching to the Gazette with `c` and back starts from what is on screen", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    resized();
    const list = screen.getByTestId("list-scroll");
    act(() => void fireEvent.keyDown(list, { key: "c" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: DEFAULT_PAPER_NAME })).toBeNull());
    act(() => {
      list.scrollTop = 1500; // where the rows left the list
    });
    act(() => void fireEvent.keyDown(list, { key: "c" }));
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    resized();
    // A scroll event with no movement by the reader: the stories seen at the top before the switch are not passed.
    act(() => void list.dispatchEvent(new Event("scroll")));
    await settle();
    expect(marked(calls)).toHaveLength(0);
  });

  it("switching from the Gazette to rows with `c` after scrolling marks nothing the rows put above the top", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    resized();
    // The reader scrolls a little and stops: stories 0 to 8 are seen, none is above the top.
    scrollTo(50);
    await frame();
    await settle();
    const list = screen.getByTestId("list-scroll");
    act(() => {
      list.scrollTop = 1500; // the rows are laid out differently: the seen stories' rows land above the top
    });
    act(() => void fireEvent.keyDown(list, { key: "c" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: DEFAULT_PAPER_NAME })).toBeNull());
    await settle();
    expect(marked(calls)).toHaveLength(0);
  });

  it("a story pushed just past the top and pulled back before scrolling stops is not marked", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    resized();
    scrollTo(150); // the first story goes above the top
    await frame();
    scrollTo(0); // and comes back, within the same gesture
    await frame();
    await settle();
    expect(marked(calls)).toHaveLength(0);
  });

  it("a text size change while the list is open starts from what is on screen", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const order = storyIds(container);
    resized();
    // The reader stops at 450 px: stories 0 to 3 are scrolled past (and marked), 4 to 11 are on screen.
    scrollTo(450);
    await frame();
    await settle();
    expect(new Set(marked(calls).flatMap((m) => m.ids))).toEqual(new Set(order.slice(0, 4)));
    // Smaller text from the open article's controls: the stories halve, so 4 to 8 now sit above 450 px.
    story = 50;
    resized();
    scrollTo(451); // the next small scroll
    await frame();
    await settle();
    expect(new Set(marked(calls).flatMap((m) => m.ids))).toEqual(new Set(order.slice(0, 4)));
  });

  it("a reflow during a fling, looked at before the ResizeObserver reports, marks nothing it moved", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    resized();
    scrollTo(50); // stories 0 to 8 seen, none above the top
    await frame();
    // The page reflows between two frames of the fling (the stories halve), and the browser runs the next scroll and
    // its animation frame before it delivers the resize. The settle comes first here too: a re-plan that keeps the
    // page's size is never reported at all.
    story = 50;
    scrollTo(60);
    await frame();
    await settle();
    expect(marked(calls)).toHaveLength(0);
    resized();
    expect(marked(calls)).toHaveLength(0);
  });

  it("turning the setting off with a settle pending, then on again, marks nothing from before", async () => {
    const { calls } = routes(() => pageOf(many(1, 20)));
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    resized();
    scrollTo(150); // the first story is scrolled past, its settle pending
    await frame();
    act(() => updatePrefs({ markReadOnScroll: false }));
    await settle();
    act(() => updatePrefs({ markReadOnScroll: true }));
    scrollTo(151); // any later scroll
    await frame();
    await settle();
    expect(marked(calls)).toHaveLength(0);
  });
});

describe("the paper's name setting", () => {
  it("is a text field in Settings, saved for this device and synced with its profile", async () => {
    routes();
    go("/settings/appearance");
    const field = await screen.findByRole("textbox", { name: "Name of the Gazette" });
    expect(field).toHaveAttribute("placeholder", DEFAULT_PAPER_NAME);
    await userEvent.type(field, "The Daily Kipple");
    expect(devicePrefsStore.get().paperName).toBe("The Daily Kipple");
    expect(profileOf({ theme: themeStore.get(), prefs: prefsStore.get(), dp: devicePrefsStore.get() })["client.paper_name"]).toBe("The Daily Kipple");
    expect(within(field.parentElement!).getByText(/Leave it empty for The Gazette/)).toBeInTheDocument();
    // A pasted line separator would break the masthead and the server would refuse it: it is not saved, and says so.
    fireEvent.change(field, { target: { value: "The Daily\u2028Kipple" } });
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(await within(field.parentElement!).findByRole("alert")).toHaveTextContent(/must fit on one line/);
    expect(devicePrefsStore.get().paperName).toBe("The Daily Kipple");
    fireEvent.change(field, { target: { value: "Morning Notes" } });
    expect(field).not.toHaveAttribute("aria-invalid");
    expect(devicePrefsStore.get().paperName).toBe("Morning Notes");
  });
});
