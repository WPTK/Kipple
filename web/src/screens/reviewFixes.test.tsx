import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Virtualizer } from "@tanstack/virtual-core";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, setListOverride, updateDevicePrefs } from "@/lib/devicePrefs";
import { updatePrefs } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { gestureLock } from "@/gestures/lock";
import { rowMenuStore } from "@/gestures/rowMenu";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;
const items = () => [1, 2, 3, 4, 5].map((n) => card(n, { published_at: BASE - n * 3600, sort_at: BASE - n * 3600 }));
const bodyOf = (call: { init?: RequestInit } | undefined) => JSON.parse(String(call?.init?.body));

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(items(), null, "2000")),
    "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }),
    "PUT /api/items/1001/star": () => json({ starred: true, restored: false }),
    "PUT /api/items/1002/star": () => json({ starred: true, restored: false }),
    ...extra,
  });
}
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
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
const clock = { t: 1000 };
const ptr = (el: Element, type: "pointerDown" | "pointerMove" | "pointerUp", x: number, y = 100) =>
  fireEvent[type](el, { pointerType: "touch", pointerId: 1, clientX: x, clientY: y, isPrimary: true });
const contentOf = (id: string) => document.querySelector(`[data-item-id="${id}"]`)?.closest(".swipe-content") as HTMLElement;

beforeEach(() => {
  vi.spyOn(performance, "now").mockImplementation(() => clock.t);
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updatePrefs({ shortcuts: true });
  updateDevicePrefs({ peekSeen: true });
  gestureLock.rowSwipe = false;
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("review fixes", () => {
  it("a row that unmounts mid-swipe releases the pull-to-refresh lock", async () => {
    media("pointer: coarse", "prefers-reduced-motion");
    routes();
    const { unmount } = go("/l/unread");
    await screen.findByText("Article number 1");
    const el = contentOf("1001");
    ptr(el, "pointerDown", 100);
    for (let i = 1; i <= 4; i++) {
      clock.t += 50;
      ptr(el, "pointerMove", 100 + i * 20, 100 + (i % 2));
    }
    expect(gestureLock.rowSwipe).toBe(true);
    unmount();
    expect(gestureLock.rowSwipe).toBe(false);
  });

  it("losing window focus mid-swipe releases the lock", async () => {
    media("pointer: coarse", "prefers-reduced-motion");
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const el = contentOf("1001");
    ptr(el, "pointerDown", 100);
    for (let i = 1; i <= 4; i++) {
      clock.t += 50;
      ptr(el, "pointerMove", 100 + i * 20, 100 + (i % 2));
    }
    expect(gestureLock.rowSwipe).toBe(true);
    act(() => void window.dispatchEvent(new Event("blur")));
    expect(gestureLock.rowSwipe).toBe(false);
  });

  it("re-measures the virtualizer when the layout changes", async () => {
    // `measure` is an instance field assigned in the constructor: a prototype setter sees each assignment.
    const spy = vi.fn();
    Object.defineProperty(Virtualizer.prototype, "measure", {
      configurable: true,
      set(fn: () => void) {
        Object.defineProperty(this, "measure", {
          configurable: true,
          writable: true,
          value: () => {
            spy();
            fn();
          },
        });
      },
    });
    try {
      routes();
      go("/l/unread?feed=1");
      await screen.findByText("Article number 1");
      spy.mockClear();
      act(() => setListOverride("feed", "1", "layout", "headlines"));
      await waitFor(() => expect(spy).toHaveBeenCalled());
    } finally {
      delete (Virtualizer.prototype as unknown as Record<string, unknown>).measure;
    }
  });

  it("a held key does not repeat an action, but j still repeats", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    await userEvent.setup().keyboard("j");
    const before = calls.filter((c) => c.url.pathname === "/api/items/mark-read").length;
    fireEvent.keyDown(document.body, { key: "m", repeat: true });
    fireEvent.keyDown(document.body, { key: "m", repeat: true });
    await new Promise((r) => setTimeout(r, 30));
    expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read").length).toBe(before);
    fireEvent.keyDown(document.body, { key: "m" });
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read").length).toBe(before + 1));
  });

  it("} with rows ticked marks below the last ticked row", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.keyboard("jxjxk"); // tick rows 1001 and 1002, move the cursor back to 1001
    await user.keyboard("}");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(true));
    expect(bodyOf(calls.find((c) => c.url.pathname === "/api/items/mark-read")).bound.anchor.id).toBe("1002");
  });
});
