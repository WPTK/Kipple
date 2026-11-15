import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { HealthFeed, HealthResponse } from "@/api/admin";
import type { Feed } from "@/api/types";
import { REVEAL_STEP } from "@/lib/reveal";
import { bootstrap, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

// Feed health draws a long library a step at a time, like the Feeds screen (issue #463).

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

const N = REVEAL_STEP + 50;
const name = (n: number) => `Feed ${String(n).padStart(3, "0")}`;
const row = (n: number): HealthFeed => ({
  id: String(n),
  title: name(n),
  url: `https://feed${n}.test/rss`,
  url_original: null,
  status: "ok",
  redirect_pending: false,
  notices: [],
  enabled: true,
  disabled_reason: null,
  last_success_at: 1000,
  last_fetch_at: 1000,
  last_error_at: null,
  last_error_class: null,
  last_error: null,
  last_status: 200,
  consecutive_failures: 0,
  current_delay_s: 1800,
  next_fetch_at: 5000,
  redirect_to: null,
  redirect_kind: null,
  redirect_count: 0,
  redirect_owner: null,
  last_new_items_at: 1000,
  trimmed_unread_count: 0,
  trimmed_unread_since: null,
  host_throttled_until: null,
});
const HEALTH: HealthResponse = {
  feeds: Array.from({ length: N }, (_, i) => row(i + 1)),
  reader_last_seen_at: null,
  snapshot: { last_at: null, last_error: null },
  clock: { ahead_s: 0 },
  db: { db_bytes: 1000, wal_bytes: 0, backup_bytes: 0, imgcache_bytes: 0 },
  unread_total: 0,
};
const BOOT_FEEDS = HEALTH.feeds.map(
  (f) => ({ id: f.id, folder_id: "1", title: f.title, site_url: f.url, icon: null, unread: 0, status: f.status, fulltext: false, retention: null, interval_minutes: null, is_archive: false, starred_count: 0 }) as unknown as Feed,
);

function go() {
  mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, feeds: BOOT_FEEDS }),
    "GET /api/items": () => json(pageOf([])),
    "GET /api/health/feeds": () => json(HEALTH),
  });
  window.history.replaceState({ idx: 0 }, "", "/health");
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  vi.stubGlobal("IntersectionObserver", HeldIO);
  watched.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe.each([
  ["a wide screen", true, "tr"],
  ["a phone", false, "li"],
])("Feed health on %s", (_, isWide, tag) => {
  const rows = (root: ParentNode) => root.querySelectorAll(`${tag}[data-reveal-key]`);
  /** The page once its search box is there (a wide screen also draws the sidebar's 300 rows first, which takes a while). */
  const ready = async () => {
    const main = await screen.findByRole("main");
    await within(main).findByRole("searchbox", { name: "Search feeds" }, { timeout: 20_000 });
    return main;
  };

  it("draws a long library a step at a time; a button and the end of the page bring the rest", async () => {
    if (isWide) wide();
    const { container } = go();
    const main = await ready();
    expect(rows(container)).toHaveLength(REVEAL_STEP);
    expect(container.querySelector(`${tag}[data-reveal-key="${N}"]`)).toBeNull();
    // A press draws the next step and puts the focus on the first row it added.
    await userEvent.setup().click(within(main).getByRole("button", { name: `Show more feeds (${N - REVEAL_STEP} not shown yet)` }));
    expect(rows(container)).toHaveLength(N);
    expect(document.activeElement).toBe(container.querySelector(`${tag}[data-reveal-key="${REVEAL_STEP + 1}"]`));
    expect(within(main).queryByRole("button", { name: /^Show more feeds/ })).toBeNull();
  }, 60_000);

  // The rest is the same list on either layout, so it is checked on the phone's (the wide page also draws the sidebar).
  it.skipIf(isWide)("the end of the page brings the rest without a press", async () => {
    const { container } = go();
    const main = await ready();
    expect(rows(container)).toHaveLength(REVEAL_STEP);
    scrollIntoView();
    await waitFor(() => expect(rows(container)).toHaveLength(N));
    expect(within(main).queryByRole("button", { name: /^Show more feeds/ })).toBeNull();
  }, 60_000);

  it.skipIf(isWide)("a search draws the first step of the rows that match, and Select all still takes every match", async () => {
    const { container } = go();
    const user = userEvent.setup();
    const main = await ready();
    await user.type(within(main).getByRole("searchbox", { name: "Search feeds" }), "Feed 0");
    // Feeds 001 to 099 match: one step draws them all.
    await waitFor(() => expect(rows(container)).toHaveLength(99));
    await user.clear(within(main).getByRole("searchbox", { name: "Search feeds" }));
    await waitFor(() => expect(rows(container)).toHaveLength(REVEAL_STEP));
    await user.click(within(main).getByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("button", { name: "Select all" }));
    expect(within(screen.getByRole("region", { name: "Selected feeds" })).getByText(`${N} selected`)).toBeInTheDocument();
  }, 60_000);
});
