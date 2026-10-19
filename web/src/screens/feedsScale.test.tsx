import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App, { makeQueryClient } from "@/App";
import { DeleteDialog } from "./feeds/BulkActions";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { Bootstrap, Feed, Folder } from "@/api/types";
import { REVEAL_STEP, useReveal } from "@/lib/reveal";
import { bootstrap, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

// The Feeds screen and the sidebar draw a long library a step at a time (issue #463), feeds that share a title show
// their address (issue #484), and an empty default folder is not shown outside Edit (issue #469).

class NoES {
  addEventListener() {}
  close() {}
}

/** An IntersectionObserver that reports nothing until the test says the watched elements came into view. */
const watched = new Set<{ cb: IntersectionObserverCallback; el: Element }>();
class HeldIO {
  private mine: { cb: IntersectionObserverCallback; el: Element }[] = [];
  constructor(private cb: IntersectionObserverCallback) {}
  observe(el: Element) {
    const w = { cb: this.cb, el };
    this.mine.push(w);
    watched.add(w);
  }
  unobserve() {}
  disconnect() {
    for (const w of this.mine) watched.delete(w);
  }
  takeRecords() {
    return [];
  }
}
const scrollIntoView = () =>
  act(() => {
    for (const w of [...watched]) w.cb([{ target: w.el, isIntersecting: true } as unknown as IntersectionObserverEntry], {} as IntersectionObserver);
  });

const folder: Folder = { id: "1", name: "Uncategorized", position: 0, is_default: true, unread: 0 };
const news: Folder = { id: "2", name: "News", position: 1, is_default: false, unread: 0 };
const feed = (n: number, over: Partial<Feed> = {}): Feed => ({
  ...bootstrap.feeds[0]!,
  id: String(n),
  folder_id: "2",
  title: `Feed ${n}`,
  unread: 0,
  ...over,
});

function go(path: string, feeds: Feed[], folders: Folder[] = [folder, news]) {
  const boot: Bootstrap = { ...bootstrap, folders, feeds, counts: { unread: 0, starred: 0 } };
  mockFetch({ "GET /api/bootstrap": () => json(boot), "GET /api/items": () => json(pageOf([])) });
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  watched.clear();
});
afterEach(() => vi.unstubAllGlobals());

describe("a long library", () => {
  const many = Array.from({ length: 700 }, (_, i) => feed(i + 1));

  it("draws the Feeds screen a step at a time, and more as its end comes into view", async () => {
    vi.stubGlobal("IntersectionObserver", HeldIO);
    go("/feeds", many);
    const main = await screen.findByRole("main");
    await within(main).findByRole("link", { name: /^Feed 1\b/ });
    // The folder row is the first of the step, so it draws REVEAL_STEP - 1 feeds.
    expect(within(main).getAllByRole("link", { name: /^Feed \d+/ })).toHaveLength(REVEAL_STEP - 1);
    expect(within(main).queryByRole("link", { name: /^Feed 700\b/ })).toBeNull();
    expect(within(main).getByRole("button", { name: "Show more feeds (401 not shown yet)" })).toBeInTheDocument();

    // A press draws the next step and puts the focus on the first row it added.
    await userEvent.setup().click(within(main).getByRole("button", { name: /^Show more feeds/ }));
    expect(within(main).getAllByRole("link", { name: /^Feed \d+/ })).toHaveLength(2 * REVEAL_STEP - 1);
    expect(document.activeElement).toBe(within(main).getByRole("link", { name: new RegExp(`^Feed ${REVEAL_STEP}\\b`) }));

    // Scrolling near the end draws the rest.
    scrollIntoView();
    await waitFor(() => expect(within(main).getByRole("link", { name: /^Feed 700\b/ })).toBeInTheDocument());
    expect(within(main).queryByRole("button", { name: /^Show more feeds/ })).toBeNull();
  });

  it("selects every feed, drawn or not, with Select all", async () => {
    vi.stubGlobal("IntersectionObserver", HeldIO);
    go("/feeds", many);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("button", { name: "Select all" }));
    expect(screen.getByText("700 selected")).toBeInTheDocument();
  });

  it("draws the sidebar a step at a time; End draws the rest and goes to the last feed", async () => {
    wide();
    vi.stubGlobal("IntersectionObserver", HeldIO);
    go("/l/unread", many);
    const tree = await screen.findByRole("tree", { name: "Feeds" });
    await within(tree).findByRole("treeitem", { name: /^Feed 1\b/ });
    expect(within(tree).getAllByRole("treeitem")).toHaveLength(REVEAL_STEP);
    within(tree).getByRole("treeitem", { name: /^News/ }).focus();
    await userEvent.setup().keyboard("{End}");
    await waitFor(() => expect(document.activeElement).toBe(within(tree).getByRole("treeitem", { name: /^Feed 700\b/ })));
  });
});

describe("feeds that share a title", () => {
  it("show their address on the Feeds screen", async () => {
    const same = [feed(1, { title: "Same title", address_hint: "a.example/feed" }), feed(2, { title: "same title ", address_hint: "b.example/rss" }), feed(3)];
    go("/feeds", same);
    const main = await screen.findByRole("main");
    expect(await within(main).findByRole("link", { name: /^Same title ?a\.example\/feed/ })).toBeInTheDocument();
    expect(within(main).getByRole("link", { name: /^same title ?b\.example\/rss/ })).toBeInTheDocument();
  });

  it("tell them apart in the sidebar, by the text and the names (item and star)", async () => {
    wide();
    const same = [feed(1, { title: "Same title", address_hint: "a.example/feed" }), feed(2, { title: "same title ", address_hint: "b.example/rss" }), feed(3)];
    go("/l/unread", same);
    const tree = await screen.findByRole("tree", { name: "Feeds" });
    const a = await within(tree).findByRole("treeitem", { name: "Same title, a.example/feed" });
    const b = within(tree).getByRole("treeitem", { name: "same title , b.example/rss" });
    expect(a).toHaveTextContent("a.example/feed");
    expect(b).toHaveTextContent("b.example/rss");
    expect(within(a).getByRole("button", { name: /Same title, a\.example\/feed/ })).toBeInTheDocument();
    // A feed with a title of its own stays a single line.
    expect(within(tree).getByRole("treeitem", { name: "Feed 3" })).not.toHaveTextContent("example");
  });
  it("show their address in the delete confirmation", () => {
    const same = [feed(1, { title: "Same title", address_hint: "a.example/feed" }), feed(2, { title: "same title ", address_hint: "b.example/rss" }), feed(3)];
    mockFetch({});
    render(
      <QueryClientProvider client={new QueryClient()}>
        <DeleteDialog feeds={same} onClose={() => undefined} onDone={() => undefined} />
      </QueryClientProvider>,
    );
    const list = screen.getByRole("list", { name: "Feeds to delete" });
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Same title (a.example/feed)", "same title  (b.example/rss)", "Feed 3"]);
  });
});

describe("the reveal budget", () => {
  const keys = Array.from({ length: 2 * REVEAL_STEP + 10 }, (_, i) => `feed:${i}`);

  it("keeps a row drawn that moved or was made past the drawn rows, and says which rows are past them", () => {
    vi.stubGlobal("IntersectionObserver", HeldIO);
    const { result, rerender } = renderHook(({ k }) => useReveal(k), { initialProps: { k: keys } });
    const last = `feed:${REVEAL_STEP - 1}`;
    expect(result.current.shown(last)).toBe(true);
    expect(result.current.past(`feed:${REVEAL_STEP}`)).toBe(true);
    expect(result.current.past("folder:collapsed")).toBe(false);

    // The last drawn row moves down a place: it stays drawn.
    act(() => result.current.ensure(last));
    const moved = [...keys.slice(0, REVEAL_STEP - 1), `feed:${REVEAL_STEP}`, last, ...keys.slice(REVEAL_STEP + 1)];
    rerender({ k: moved });
    expect(result.current.shown(last)).toBe(true);
    expect(result.current.left).toBe(keys.length - REVEAL_STEP - 1);

    // A folder made at the end is drawn once the list has it.
    act(() => result.current.ensure("folder:new"));
    expect(result.current.left).toBe(keys.length - REVEAL_STEP);
    rerender({ k: [...moved, "folder:new"] });
    expect(result.current.shown("folder:new")).toBe(true);
    expect(result.current.left).toBe(0);
  });
});

describe("the default folder", () => {
  it("is not shown on the Feeds screen while empty, except in Edit, where it takes dropped feeds", async () => {
    go("/feeds", [feed(1)]);
    const main = await screen.findByRole("main");
    await within(main).findByRole("link", { name: /^News/ });
    expect(within(main).queryByRole("link", { name: /^Uncategorized/ })).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: "Edit" }));
    expect(within(main).getByRole("link", { name: /^Uncategorized/ })).toBeInTheDocument();
    expect(within(main).getByText("Empty. Feeds added without a folder go here.")).toBeInTheDocument();
  });

  it("is shown when it holds a feed", async () => {
    go("/feeds", [feed(1, { folder_id: "1" })]);
    const main = await screen.findByRole("main");
    expect(await within(main).findByRole("link", { name: /^Uncategorized/ })).toBeInTheDocument();
  });
});
