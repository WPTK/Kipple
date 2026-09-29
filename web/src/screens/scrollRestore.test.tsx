import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { keys } from "@/api/queries";
import { DEFAULT_PREFS, prefsStore, updatePrefs } from "@/lib/prefs";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  prefsStore.set(DEFAULT_PREFS);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
  prefsStore.set(DEFAULT_PREFS);
});

describe("mark-read-on-scroll only marks rows actually seen", () => {
  it("a jump that skips straight past rows never renders them does not mark those rows read", async () => {
    updatePrefs({ markReadOnScroll: true });
    const cards = Array.from({ length: 60 }, (_, i) => card(i + 1));
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf(cards)),
      "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
    });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      go("/l/unread");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(50);
      });
      const scroller = screen.getByTestId("list-scroll");
      // Jump straight to the far end, the way a restored stale offset (or any other programmatic jump) would,
      // without ever rendering the rows in between.
      act(() => {
        scroller.scrollTop = 50_000;
        scroller.dispatchEvent(new Event("scroll"));
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(50);
      });
      act(() => {
        scroller.dispatchEvent(new Event("scroll"));
      });
      // Let the 700ms settle timer fire.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(800);
      });
      const marked = calls
        .filter((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")
        .flatMap((c) => JSON.parse(String(c.init?.body)).ids as string[]);
      // The middle of the list was never on screen at any point (mount rendered only the top handful, then the
      // jump landed at the bottom): none of those ids may have been sent as "scrolled past".
      const middleIds = cards.slice(15, 45).map((c) => c.id);
      expect(marked.some((id) => middleIds.includes(id))).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("navigating away before the 700ms settle still marks genuinely-seen rows read (flush on unmount)", async () => {
    updatePrefs({ markReadOnScroll: true });
    const cards = Array.from({ length: 30 }, (_, i) => card(i + 1));
    const detailRoutes: Record<string, () => Response> = {};
    for (const c of cards) {
      detailRoutes[`GET /api/items/${c.id}`] = () =>
        json({ ...c, content_html: "<p>body</p>", fulltext: { mode: null, effective: 0, available: false, error: null }, enclosures: [], feed: { id: "1", title: "Example Feed", site_url: "https://example.com" }, trimmed: false });
      detailRoutes[`POST /api/items/${c.id}/open`] = () => json({ session_key: "k", item: { ...c, read: true } });
    }
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf(cards)),
      ...detailRoutes,
      "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
    });
    const { container } = go("/l/unread");
    await screen.findByText("Article number 1");
    const scroller = screen.getByTestId("list-scroll");
    act(() => {
      scroller.scrollTop = 6000;
      scroller.dispatchEvent(new Event("scroll"));
    });
    // Well under the 700ms settle: nothing has been sent yet.
    expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")).toBe(false);
    // Navigate to an article (on a narrow layout, ListPane unmounts) before the settle timer ever fires. Click
    // whichever row is actually rendered after the scroll (the exact index depends on the layout's row height
    // estimate); the point is that ANY navigation away must flush the pending settle, not just one specific id.
    const link = container.querySelector<HTMLElement>("[data-item-id] a");
    expect(link).not.toBeNull();
    act(() => link?.click());
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")).toBe(true));
  });
});

describe("stale scroll offset over refetched data", () => {
  it("resets to the top once the list actually refetches, instead of leaving a restored offset over new rows", async () => {
    const cards = Array.from({ length: 30 }, (_, i) => card(i + 1));
    let asOf = "100";
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf(cards, null, asOf)),
      "GET /api/feeds": () => json([]),
    });
    const qc = makeQueryClient({ retry: false });
    window.history.replaceState({ idx: 0 }, "", "/l/unread");
    render(<App client={qc} />);
    await screen.findByText("Article number 1");
    const scroller = screen.getByTestId("list-scroll");
    act(() => {
      scroller.scrollTop = 900;
      scroller.dispatchEvent(new Event("scroll"));
    });

    // Leave the list (in-app navigation, not a full remount — ListPane unmounts, the module-scope memory of the
    // scroll offset does not) while a background invalidation elsewhere (a mute, mark-all-read, a resync) marks
    // it stale without an immediate refetch, the way dropFromLists/invalidateLists do it: the cache keeps the
    // old as_of right up until something remounts the query and its own refetch-on-mount lands.
    const user = userEvent.setup();
    await user.click(screen.getByRole("link", { name: /Feeds/ }));
    await act(async () => {
      await qc.invalidateQueries({ queryKey: keys.items({ view: "unread" }), refetchType: "none" });
    });
    asOf = "200";

    // Element.prototype.scrollTo is a no-op stub in jsdom (src/test/setup.ts), so a real scrollTop pixel check
    // can't tell a restored offset from a reset one; spying on it directly proves whether the list asked to
    // scroll back to the top.
    const scrollTo = vi.spyOn(Element.prototype, "scrollTo");
    await user.click(screen.getByRole("link", { name: /Unread/ }));
    // First paint still serves the stale cached page (as_of "100", matching what the offset was saved against),
    // so the pixel offset is restored optimistically: the list comes back scrolled down, not at the top.
    await screen.findByTestId("list-scroll");
    // ...but the remount's own refetch-on-mount (the query was marked stale) lands moments later with as_of
    // "200", which must reset the scroll to the top rather than leaving the old offset over the fresh rows.
    await waitFor(() => {
      expect(scrollTo.mock.calls.some((c) => (c[0] as { top?: number })?.top === 0)).toBe(true);
    });
  });
});
