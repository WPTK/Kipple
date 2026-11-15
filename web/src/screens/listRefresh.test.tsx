import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore, reduceEvent } from "@/api/events";
import { rowMenuStore } from "@/gestures/rowMenu";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function open(items: () => ReturnType<typeof card>[]) {
  const fetched = mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(items())),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
  });
  window.history.replaceState({ idx: 0 }, "", "/l/unread");
  render(<App client={makeQueryClient({ retry: false })} />);
  return fetched;
}

/** A refresh the reader asked for ends. */
const refreshEnds = () =>
  act(() => {
    liveStore.set((s) => reduceEvent(reduceEvent(s, { type: "run.start", data: { run_id: "5", kind: "manual", total: 1 } }), { type: "run.done", data: { run_id: "5", new_items: 1, errors: 0 } }));
  });

const scrollTo = (top: number) => Object.defineProperty(screen.getByTestId("list-scroll"), "scrollTop", { configurable: true, writable: true, value: top });

describe("a manual refresh that ends while a list is open", () => {
  it("leaves a reader part-way down where they are and offers the new articles in the pill", async () => {
    let items = [card(1), card(2), card(3)];
    open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(240);
    items = [card(9), ...items];
    refreshEnds();
    const pill = await screen.findByRole("button", { name: "1 new article" });
    expect(pill).toHaveAttribute("data-open", "true");
    expect(screen.queryByText("Article number 9")).toBeNull();
    expect(screen.getByText("Article number 1")).toBeInTheDocument();
    await userEvent.setup().click(pill);
    await screen.findByText("Article number 9");
  });

  it("loads the new articles by itself for a reader at the top", async () => {
    let items = [card(1), card(2), card(3)];
    open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(0);
    items = [card(9), ...items];
    refreshEnds();
    await screen.findByText("Article number 9");
    expect(screen.queryByRole("button", { name: /new article/ })?.getAttribute("data-open")).not.toBe("true");
  });

  it("waits for the tab to be shown before looking, then asks once", async () => {
    let items = [card(1), card(2), card(3)];
    const { calls } = open(() => items);
    await screen.findByText("Article number 1");
    scrollTo(240);
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    items = [card(9), ...items];
    const loads = () => calls.filter((c) => c.url.pathname === "/api/items").length;
    const before = loads();
    refreshEnds();
    expect(loads()).toBe(before);
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    await screen.findByRole("button", { name: "1 new article" });
    await waitFor(() => expect(loads()).toBe(before + 1));
  });
});
