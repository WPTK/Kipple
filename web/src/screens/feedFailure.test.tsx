import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { Feed } from "@/api/types";
import { bootstrap, json, mockFetch, pageOf } from "@/test/mockApi";
import { emptyCopy } from "./ListPane";

// A feed Kipple cannot fetch says so, with the reason, where the person meets it (issue #476).

class NoES {
  addEventListener() {}
  close() {}
}

const failing: Feed = { ...bootstrap.feeds[0]!, unread: 0, status: "erroring", last_error: "lookup feeds.example.test: no such host" };

function go(path: string, feed: Feed = failing) {
  mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [feed], counts: { unread: 0, starred: 0 } }),
    "GET /api/items": () => json(pageOf([])),
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
});
