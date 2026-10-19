import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { Feed } from "@/api/types";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { emptyCopy } from "./ListPane";
import { failureNoteFeeds, failureNoteText, showsFailure } from "@/lib/feedStatus";

// A feed Kipple cannot fetch says so, with the reason, where the person meets it (issue #476).

class NoES {
  addEventListener() {}
  close() {}
}

const failing: Feed = { ...bootstrap.feeds[0]!, unread: 0, status: "erroring", last_error: "lookup feeds.example.test: no such host" };

function go(path: string, feed: Feed = failing, items: ReturnType<typeof card>[] = [], extra: Feed[] = [], itemsHandler?: () => Response | Promise<Response>) {
  mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [feed, ...extra], counts: { unread: 0, starred: 0 } }),
    "GET /api/items": itemsHandler ?? (() => json(pageOf(items))),
  });
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("a failing feed", () => {
  it("says it can't be fetched, and why, instead of All caught up", () => {
    expect(emptyCopy({ view: "unread", feed: "1" }, 0, failing)).toEqual({
      title: "The last fetch of this feed failed",
      body: "Kipple will retry with longer waits. Last error: lookup feeds.example.test: no such host",
      health: true,
    });
    expect(emptyCopy({ view: "all", feed: "1" }, 0, { ...failing, status: "dead", last_error: "HTTP 410" }).title).toBe("This feed is gone");
    expect(emptyCopy({ view: "all", feed: "1" }, 0, { ...failing, status: "failing" }).title).toBe("Kipple can't fetch this feed");
    // New articles waiting mean a fetch worked since: the list says so, not the old failure.
    expect(emptyCopy({ view: "unread", feed: "1" }, 2, failing).body).toBe("2 new articles arrived. Load them above to continue reading.");
  });

  it("keeps the usual copy for a working feed, a search and the starred list", () => {
    expect(emptyCopy({ view: "unread", feed: "1" }, 0, { ...failing, status: "ok", last_error: null }).title).toBe("All caught up");
    expect(emptyCopy({ view: "unread", feed: "1" }, 0, { ...failing, status: "throttled", last_error: null }).title).toBe("All caught up");
    expect(emptyCopy({ view: "unread", feed: "1", q: "zzz" }, 0, failing).title).toBe('No results for "zzz"');
    expect(emptyCopy({ view: "starred", feed: "1" }, 0, failing).title).toBe("No starred articles");
  });

  it("shows the reason and a way to Feed health in the feed's empty list", async () => {
    go("/l?feed=1");
    const box = (await screen.findByText("The last fetch of this feed failed")).closest("[role=status]") as HTMLElement;
    expect(box).toHaveTextContent("Last error: lookup feeds.example.test: no such host");
    expect(within(box).getByRole("link", { name: "Open Feed health" })).toHaveAttribute("href", "/health");
    expect(screen.queryByText("All caught up")).not.toBeInTheDocument();
  });

  it("shows the reason on its Manage feeds row", async () => {
    go("/feeds");
    const row = await screen.findByRole("link", { name: /Example Feed/ });
    expect(row).toHaveTextContent("Having trouble");
    expect(within(row).getByText("lookup feeds.example.test: no such host")).toHaveAttribute("title", "lookup feeds.example.test: no such host");
  });

  it("puts the reason and a Feed health link above a failing feed's list that still has articles", async () => {
    go("/l/all?feed=1", failing, [card(1), card(2)]);
    const note = await screen.findByTestId("feed-failure-note");
    expect(note).toHaveTextContent("The last fetch of this feed failed: lookup feeds.example.test: no such host");
    expect(within(note).getByRole("link", { name: "Open Feed health" })).toHaveAttribute("href", "/health");
  });

  it("stays out of a working feed's list", async () => {
    go("/l/all?feed=1", { ...failing, status: "ok", last_error: null }, [card(1)]);
    await screen.findByText(card(1).title);
    expect(screen.queryByTestId("feed-failure-note")).not.toBeInTheDocument();
  });

  it("names the failing feeds in a folder's list", async () => {
    go(`/l/all?folder=${failing.folder_id}`, failing, [card(1)]);
    const note = await screen.findByTestId("feed-failure-note");
    expect(note).toHaveTextContent(`${failing.title} failed its last fetch: lookup feeds.example.test: no such host`);
  });

  it("is not a live region, and is one clamped line with the full text as its tooltip", async () => {
    go("/l/all?feed=1", failing, [card(1)]);
    const note = await screen.findByTestId("feed-failure-note");
    expect(note.closest("[role=status],[aria-live]")).toBeNull();
    const p = note.querySelector("p") as HTMLElement;
    expect(p.className).toContain("line-clamp-1");
    expect(p.getAttribute("title")).toContain("lookup feeds.example.test: no such host");
  });

  it("stays out of a search, Starred and Muted", async () => {
    for (const path of ["/search?q=zzz&feed=1", "/l/starred?feed=1", "/l/muted?feed=1"]) {
      const { unmount } = go(path, failing, [card(1)]);
      await screen.findByText(card(1).title);
      expect(screen.queryByTestId("feed-failure-note"), path).not.toBeInTheDocument();
      unmount();
    }
  });

  it("is not drawn while the list is loading, and not over a feed's empty list", async () => {
    const first = go("/l/all?feed=1", failing, [], [], () => new Promise<Response>(() => {}));
    await screen.findByLabelText("Articles");
    expect(screen.queryByTestId("feed-failure-note")).not.toBeInTheDocument();
    first.unmount();
    go("/l/all?feed=1");
    await screen.findByText("The last fetch of this feed failed");
    expect(screen.queryByTestId("feed-failure-note")).not.toBeInTheDocument();
  });

  it("stays quiet in a feed's unread list when new articles are waiting", async () => {
    liveStore.set({ ...initialLive, pendingByFeed: { "1": 2 } });
    go("/l/unread?feed=1", failing, [card(1)]);
    await screen.findByText(card(1).title);
    expect(screen.queryByTestId("feed-failure-note")).not.toBeInTheDocument();
  });
});

describe("which failures are current", () => {
  const dead: Feed = { ...failing, id: "2", title: "Gone Feed", status: "dead", last_error: "HTTP 410" };
  const fine: Feed = { ...failing, id: "3", title: "Fine Feed", status: "ok", last_error: null };
  const all = [failing, dead, fine];
  const folders = bootstrap.folders;

  it("is one rule: an error, unless new articles wait in a feed's unread list", () => {
    expect(showsFailure({ view: "unread", feed: "1" }, 0, failing)).toBe(true);
    expect(showsFailure({ view: "unread", feed: "1" }, 2, failing)).toBe(false);
    expect(showsFailure({ view: "all", feed: "1" }, 2, failing)).toBe(true);
    expect(showsFailure({ view: "unread", folder: "1" }, 2, failing)).toBe(true);
    expect(showsFailure({ view: "unread", feed: "3" }, 0, fine)).toBe(false);
  });

  const pick = (scope: Parameters<typeof failureNoteFeeds>[0]["scope"], over: Partial<Parameters<typeof failureNoteFeeds>[0]> = {}) =>
    failureNoteFeeds({ scope, pendingNew: 0, feeds: all, folders, loaded: true, empty: false, ...over }).map((x) => x.title);

  it("counts and names only current failures in a folder, the worst first", () => {
    expect(pick({ view: "all", folder: failing.folder_id })).toEqual(["Gone Feed", failing.title]);
  });

  it("decides the whole note: loaded, not a search, Starred or Muted, not a feed's empty list", () => {
    const feed = { view: "all" as const, feed: "1" };
    expect(pick(feed)).toEqual([failing.title]);
    expect(pick(feed, { loaded: false })).toEqual([]);
    expect(pick(feed, { empty: true })).toEqual([]);
    expect(pick({ view: "all", feed: "1", q: "x" })).toEqual([]);
    expect(pick({ view: "starred", feed: "1" })).toEqual([]);
    expect(pick({ view: "muted", feed: "1" })).toEqual([]);
    expect(pick({ view: "unread", feed: "1" }, { pendingNew: 2 })).toEqual([]);
    // An empty folder has no empty-state sentence about failures, so the note is the only place it is said.
    expect(pick({ view: "all", folder: failing.folder_id }, { empty: true })).toEqual(["Gone Feed", failing.title]);
    expect(emptyCopy({ view: "all", folder: failing.folder_id }, 0).title).toBe("No articles yet");
  });

  it("says the count once and names up to three feeds", () => {
    const five = ["A", "B", "C", "D", "E"].map((t, i) => ({ ...failing, id: `x${i}`, title: t }));
    expect(failureNoteText(five, true)).toBe("5 feeds in this folder have fetch problems: A, B, C and 2 more.");
    expect(failureNoteText(five.slice(0, 3), true)).toBe("3 feeds in this folder have fetch problems: A, B, C.");
    expect(failureNoteText([dead], true)).toBe("Gone Feed is gone: HTTP 410");
    expect(failureNoteText([dead], false)).toBe("This feed is gone: HTTP 410");
  });

  it("a folder's note names the dead feed first", async () => {
    go(`/l/all?folder=${failing.folder_id}`, failing, [card(1)], [dead, fine]);
    const note = await screen.findByTestId("feed-failure-note");
    expect(note).toHaveTextContent(`2 feeds in this folder have fetch problems: Gone Feed, ${failing.title}.`);
  });

  it("a folder whose feeds all recovered has no note", async () => {
    go(`/l/all?folder=${failing.folder_id}`, fine, [card(1)]);
    await screen.findByText(card(1).title);
    expect(screen.queryByTestId("feed-failure-note")).not.toBeInTheDocument();
  });
});
