import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PREFS, prefsStore, updatePrefs } from "@/lib/prefs";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

// The article list is one Tab stop (web/src/layouts/rowTabStop.ts, #369): a list that loads more rows as focus moves
// down must not put every row in the Tab order, or nothing after it can be reached.

class NoES {
  addEventListener() {}
  close() {}
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
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
  prefsStore.set(DEFAULT_PREFS);
});

async function list() {
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(Array.from({ length: 30 }, (_, i) => card(i + 1)))),
    "GET /api/feeds": () => json([]),
  });
  window.history.replaceState({ idx: 0 }, "", "/l/unread");
  render(<App client={makeQueryClient({ retry: false })} />);
  await screen.findByText("Article number 1");
  return screen.getByTestId("list-scroll");
}

/** The row controls in the Tab order, by the id of the row they are in. */
function tabbable(scroller: HTMLElement): Map<string, number> {
  const out = new Map<string, number>();
  for (const el of scroller.querySelectorAll<HTMLElement>("[data-item-id] a, [data-item-id] button")) {
    if (el.tabIndex < 0) continue;
    const id = el.closest<HTMLElement>("[data-item-id]")!.dataset.itemId!;
    out.set(id, (out.get(id) ?? 0) + 1);
  }
  return out;
}

const linkOf = (scroller: HTMLElement, id: string) => scroller.querySelector<HTMLElement>(`[data-item-id="${id}"] a`)!;

describe("the article list is one Tab stop", () => {
  it("puts only the first row's controls in the Tab order when nothing is selected", async () => {
    const scroller = await list();
    const rows = scroller.querySelectorAll("[data-item-id]");
    expect(rows.length).toBeGreaterThan(3);
    const stops = tabbable(scroller);
    expect([...stops.keys()]).toHaveLength(1);
    const first = rows[0] as HTMLElement;
    expect(stops.get(first.dataset.itemId!)).toBeGreaterThan(0);
  });

  it("moves between rows with the arrow keys, and the Tab stop follows the row moved to", async () => {
    const scroller = await list();
    const [a, b] = [...scroller.querySelectorAll<HTMLElement>("[data-item-id]")].map((r) => r.dataset.itemId!);
    linkOf(scroller, a!).focus();
    // Single-key shortcuts off: the arrows still move.
    act(() => updatePrefs({ shortcuts: false }));
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    await waitFor(() => expect(document.activeElement).toBe(linkOf(scroller, b!)));
    expect([...tabbable(scroller).keys()]).toEqual([b]);
    fireEvent.keyDown(document.activeElement!, { key: "ArrowUp" });
    await waitFor(() => expect(document.activeElement).toBe(linkOf(scroller, a!)));
    expect([...tabbable(scroller).keys()]).toEqual([a]);
  });

  it("leaves arrows with a modifier, and arrows outside a row, to the browser", async () => {
    const scroller = await list();
    const [a] = [...scroller.querySelectorAll<HTMLElement>("[data-item-id]")].map((r) => r.dataset.itemId!);
    linkOf(scroller, a!).focus();
    expect(fireEvent.keyDown(document.activeElement!, { key: "ArrowDown", shiftKey: true })).toBe(true);
    expect(fireEvent.keyDown(scroller, { key: "ArrowDown" })).toBe(true);
    expect(document.activeElement).toBe(linkOf(scroller, a!));
  });
});
