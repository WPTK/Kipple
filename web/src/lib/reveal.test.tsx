import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, renderHook, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { Bootstrap, Feed, Folder } from "@/api/types";
import { REVEAL_STEP, useReveal, type Reveal } from "@/lib/reveal";
import { bootstrap, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

// Follow-ups to the reveal budget (issue #516): a hold on a far row ends when the row is scrolled away, the arrow key
// asks for the next row by its key, one traversal builds the key lists, and a focus move or a tick does not rebuild them.

const walk = vi.hoisted(() => ({ calls: 0 }));
vi.mock("@/lib/folderTree", async (orig) => {
  const mod = await orig<typeof import("@/lib/folderTree")>();
  return { ...mod, walkRows: (...a: Parameters<typeof mod.walkRows>) => (walk.calls++, mod.walkRows(...a)) };
});

class NoES {
  addEventListener() {}
  close() {}
}

/** An IntersectionObserver the test drives: `see(el, true|false)` reports that element in or out of view. */
const observers: { cb: IntersectionObserverCallback; el: Element }[] = [];
class DrivenIO {
  private mine: { cb: IntersectionObserverCallback; el: Element }[] = [];
  constructor(private cb: IntersectionObserverCallback) {}
  observe(el: Element) {
    const w = { cb: this.cb, el };
    this.mine.push(w);
    observers.push(w);
  }
  unobserve() {}
  disconnect() {
    for (const w of this.mine) observers.splice(observers.indexOf(w), 1);
  }
  takeRecords() {
    return [];
  }
}
/** Reports `el` in view, or out of it above (`"above"`) or below (`"below"`) a view 600 px tall. */
const see = (el: Element, where: "in" | "above" | "below") =>
  act(() => {
    const top = where === "in" ? 100 : where === "above" ? -200 : 700;
    const entry = { target: el, isIntersecting: where === "in", boundingClientRect: { top }, rootBounds: { bottom: 600 } };
    for (const w of observers.filter((o) => o.el === el)) w.cb([entry as unknown as IntersectionObserverEntry], {} as IntersectionObserver);
  });

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  vi.stubGlobal("IntersectionObserver", DrivenIO);
  observers.length = 0;
  walk.calls = 0;
});
afterEach(() => vi.unstubAllGlobals());

const keys = Array.from({ length: 700 }, (_, i) => `feed:${i}`);

describe("the hold on a far row", () => {
  /** The rows of the list that are drawn, as a screen draws them (each marked for the focus request and the hold). */
  function Rows({ k, out }: { k: readonly string[]; out: { reveal?: Reveal } }) {
    const reveal = useReveal(k);
    out.reveal = reveal;
    return <div data-reveal-root="">{k.map((key) => (reveal.shown(key) ? <div key={key} data-reveal-key={key} tabIndex={-1} /> : null))}</div>;
  }
  const drawnCount = () => document.querySelectorAll("[data-reveal-key]").length;

  it("draws the row and everything above it, and lets go once the row was seen and the reader is above every held row", () => {
    const out: { reveal?: Reveal } = {};
    render(<Rows k={keys} out={out} />);
    expect(drawnCount()).toBe(REVEAL_STEP);
    act(() => out.reveal!.ensure("feed:650"));
    expect(drawnCount()).toBe(651);
    const row = document.querySelector('[data-reveal-key="feed:650"]')!;
    // The first row only the hold draws: once it is below the view, every row the hold draws is.
    const first = document.querySelector(`[data-reveal-key="feed:${REVEAL_STEP}"]`)!;

    // Not on screen yet: the held rows being below the view is not the reader leaving them.
    see(first, "below");
    expect(drawnCount()).toBe(651);
    see(row, "in");
    see(first, "above");
    expect(drawnCount()).toBe(651);
    // Scrolled down past the row, or up only part of the way: the reader may be looking at held rows, so they stay.
    see(row, "above");
    see(row, "below");
    expect(drawnCount()).toBe(651);
    see(first, "below");
    expect(drawnCount()).toBe(REVEAL_STEP);
    expect(out.reveal!.left).toBe(700 - REVEAL_STEP);
  });

  it("is not needed once the list has been drawn past the row", () => {
    const out: { reveal?: Reveal } = {};
    render(<Rows k={keys} out={out} />);
    act(() => out.reveal!.ensure("feed:650"));
    act(() => out.reveal!.more());
    expect(drawnCount()).toBe(700);
    expect(out.reveal!.left).toBe(0);
  });
});

describe("what the reveal says about the end of a list", () => {
  it("names the last drawn and the first not drawn of the items given, in the order given", () => {
    const { result } = renderHook(() => useReveal(keys));
    const items = [3, 299, 300, 450].map((n) => ({ id: n }));
    const key = (x: { id: number }) => `feed:${x.id}`;
    expect(result.current.lastDrawn(items, key)).toEqual({ id: 299 });
    expect(result.current.firstPast(items, key)).toEqual({ id: 300 });
    expect(result.current.lastDrawn([{ id: 400 }], key)).toBeNull();
    expect(result.current.firstPast(items.slice(0, 2), key)).toBeNull();
    expect(result.current.has("feed:5")).toBe(true);
    expect(result.current.has("feed:5000")).toBe(false);
  });

  it("keeps its identity until the list or the drawn count changes", () => {
    const { result, rerender } = renderHook(({ k }) => useReveal(k), { initialProps: { k: keys } });
    const first = result.current;
    rerender({ k: keys });
    expect(result.current).toBe(first);
    act(() => result.current.more());
    expect(result.current).not.toBe(first);
  });
});

const folder: Folder = { id: "1", name: "Uncategorized", position: 0, is_default: true, unread: 0 };
const news: Folder = { id: "2", name: "News", position: 1, is_default: false, unread: 0 };
const feed = (n: number, over: Partial<Feed> = {}): Feed => ({ ...bootstrap.feeds[0]!, id: String(n), folder_id: "2", title: `Feed ${n}`, unread: 0, ...over });
function go(path: string, feeds: Feed[]) {
  const boot: Bootstrap = { ...bootstrap, folders: [folder, news], feeds, counts: { unread: 0, starred: 0 } };
  mockFetch({ "GET /api/bootstrap": () => json(boot), "GET /api/items": () => json(pageOf([])) });
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
const many = Array.from({ length: 700 }, (_, i) => feed(i + 1));

describe("the sidebar tree", () => {
  it("answers ArrowDown past the drawn rows by the next row's key, and End by the last row's", async () => {
    wide();
    go("/l/unread", many);
    const tree = await screen.findByRole("tree", { name: "Feeds" });
    await within(tree).findByRole("treeitem", { name: /^Feed 1\b/ });
    const items = within(tree).getAllByRole("treeitem");
    expect(items).toHaveLength(REVEAL_STEP);
    const last = items.at(-1)!;
    const user = userEvent.setup();
    act(() => last.focus());
    await user.keyboard("{ArrowDown}");
    // The key after the last drawn row is the first row the step adds, whatever the DOM looked like at the key press.
    expect(document.activeElement).toBe(within(tree).getAllByRole("treeitem")[REVEAL_STEP]);
    expect(document.activeElement).toHaveAttribute("data-tree-key", "feed-300");
    // The tab stop follows the focus.
    expect(document.activeElement).toHaveAttribute("tabindex", "0");

    await user.keyboard("{End}");
    expect(document.activeElement).toHaveAttribute("data-tree-key", "feed-700");
  });

  it("does not rebuild its key lists when focus moves", async () => {
    wide();
    go("/l/unread", many);
    const tree = await screen.findByRole("tree", { name: "Feeds" });
    await within(tree).findByRole("treeitem", { name: /^Feed 1\b/ });
    const user = userEvent.setup();
    act(() => within(tree).getByRole("treeitem", { name: /^News/ }).focus());
    const before = walk.calls;
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}");
    expect(walk.calls).toBe(before);
  });
});

describe("Feeds", () => {
  it("does not rebuild its key list when a feed is ticked", async () => {
    go("/feeds", many);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Select feeds" }));
    const boxes = screen.getAllByRole("checkbox");
    const before = walk.calls;
    await user.click(boxes[1]!);
    await user.click(boxes[2]!);
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    expect(walk.calls).toBe(before);
  });
});
