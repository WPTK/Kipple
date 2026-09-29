import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
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
});
