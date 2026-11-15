import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { applyCounts, initialLive, liveStore } from "@/api/events";
import type { Bootstrap, Feed, Folder } from "@/api/types";
import * as articleDom from "@/lib/articleDom";
import * as routes from "@/lib/routes";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

// A count that moves while a screen is open replaces the bootstrap. Each screen below must redraw only what that count
// touches: the sidebar's other feeds, an open article's body check, and the virtual list's key function.

const seen = vi.hoisted(() => ({ keyFns: [] as unknown[] }));

vi.mock("@/lib/articleDom", async (orig) => {
  const m = await orig<typeof import("@/lib/articleDom")>();
  return { ...m, hasNothingToRead: vi.fn(m.hasNothingToRead) };
});
vi.mock("@/lib/routes", async (orig) => {
  const m = await orig<typeof import("@/lib/routes")>();
  return { ...m, openListTo: vi.fn(m.openListTo) };
});
vi.mock("@tanstack/react-virtual", async (orig) => {
  const m = await orig<typeof import("@tanstack/react-virtual")>();
  return {
    ...m,
    useVirtualizer: ((options: { getItemKey?: unknown }) => {
      seen.keyFns.push(options.getItemKey);
      return (m.useVirtualizer as (o: unknown) => unknown)(options);
    }) as typeof m.useVirtualizer,
  };
});

class NoES {
  addEventListener() {}
  close() {}
}

const folder: Folder = { id: "2", name: "News", position: 0, is_default: false, unread: 0 };
const feed = (n: number): Feed => ({ ...bootstrap.feeds[0]!, id: String(n), folder_id: "2", title: `Feed ${n}`, unread: 0 });

function open(path: string, boot: Bootstrap, extra: Parameters<typeof mockFetch>[0] = {}) {
  mockFetch({ "GET /api/bootstrap": () => json(boot), "GET /api/items": () => json(pageOf([card(1), card(2), card(3)])), ...extra });
  window.history.replaceState({ idx: 0 }, "", path);
  const client = makeQueryClient({ retry: false });
  render(<App client={client} />);
  return client;
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  seen.keyFns.length = 0;
  vi.mocked(articleDom.hasNothingToRead).mockClear();
  vi.mocked(routes.openListTo).mockClear();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("a count that moves while a screen is open", () => {
  it("redraws the sidebar row of the feed it changes and no other", async () => {
    wide();
    const feeds = [feed(1), feed(2), feed(3)];
    const client = open("/l/unread", { ...bootstrap, folders: [folder], feeds, counts: { unread: 0, starred: 0 } });
    const tree = await screen.findByRole("tree", { name: "Feeds" });
    await within(tree).findByRole("treeitem", { name: /^Feed 3\b/ });
    const drawn = (id: string) => vi.mocked(routes.openListTo).mock.calls.filter(([to]) => "feed" in to && to.feed === id).length;
    // Let the first load settle before counting.
    await waitFor(() => expect(within(tree).getAllByRole("treeitem").length).toBeGreaterThan(3));
    const before = { 1: drawn("1"), 2: drawn("2"), 3: drawn("3") };
    expect(before[2]).toBeGreaterThan(0);

    act(() => applyCounts(client, { unread_total: 4, feeds: { "1": 0, "2": 4, "3": 0 } }));
    await waitFor(() => expect(drawn("2")).toBeGreaterThan(before[2]));
    expect(drawn("2")).toBeGreaterThan(before[2]);
    expect(drawn("1")).toBe(before[1]);
    expect(drawn("3")).toBe(before[3]);
  });

  it("leaves the check for an article with no text alone while the article is open", async () => {
    const body = detail(1, { content_html: "<p>Short text.</p>" });
    const client = open("/i/1001", bootstrap, {
      "GET /api/items/1001": () => json(body),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: { ...body, read: true } }),
    });
    expect(await screen.findByTestId("article-body")).toBeInTheDocument();
    await waitFor(() => expect(vi.mocked(articleDom.hasNothingToRead)).toHaveBeenCalled());
    const before = vi.mocked(articleDom.hasNothingToRead).mock.calls.length;

    act(() => applyCounts(client, { unread_total: 1, feeds: { "1": 1 } }));
    act(() => applyCounts(client, { unread_total: 0, feeds: { "1": 0 } }));
    await screen.findByTestId("article-body");
    expect(vi.mocked(articleDom.hasNothingToRead)).toHaveBeenCalledTimes(before);
  });

  it("gives the virtual list the same key function until its rows change", async () => {
    const client = open("/l/unread", bootstrap);
    await screen.findAllByRole("article");
    await waitFor(() => expect(seen.keyFns.length).toBeGreaterThan(1));
    const settled = seen.keyFns.length;
    const last = seen.keyFns[settled - 1];

    act(() => applyCounts(client, { unread_total: 2, feeds: { "1": 2 } }));
    await waitFor(() => expect(seen.keyFns.length).toBeGreaterThan(settled));
    expect(new Set(seen.keyFns.slice(settled - 1))).toEqual(new Set([last]));
  });
});
