import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { RowTabStop } from "@/layouts/rowTabStop";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory, keepMounted, MutedStrip, windowReach } from "./ListPane";

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
  document.body.replaceChildren();
  prefsStore.set(DEFAULT_PREFS);
});

async function list(count = 30) {
  const api = mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(Array.from({ length: count }, (_, i) => card(i + 1)))),
    "GET /api/feeds": () => json([]),
    "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
  });
  window.history.replaceState({ idx: 0 }, "", "/l/unread");
  render(<App client={makeQueryClient({ retry: false })} />);
  await screen.findByText("Article number 1");
  const scroller = screen.getByTestId("list-scroll");
  return { scroller, api, ids: () => [...scroller.querySelectorAll<HTMLElement>("article[data-item-id]")].map((r) => r.dataset.itemId!) };
}

/** The ids of the rows whose controls are in the Tab order. */
function stops(scroller: HTMLElement): string[] {
  const out = new Set<string>();
  for (const el of scroller.querySelectorAll<HTMLElement>("[data-item-id] a, [data-item-id] button")) {
    if (el.tabIndex >= 0) out.add(el.closest<HTMLElement>("[data-item-id]")!.dataset.itemId!);
  }
  return [...out];
}

const linkOf = (scroller: HTMLElement, id: string) => scroller.querySelector<HTMLElement>(`[data-item-id="${id}"] a`)!;
const rowOfFocus = () => document.activeElement?.closest<HTMLElement>("[data-item-id]")?.dataset.itemId;
const selectedRows = (scroller: HTMLElement) => [...scroller.querySelectorAll('[data-selected="true"],[aria-current="true"]')].length;

describe("the article list is one Tab stop", () => {
  it("puts only the first row's controls in the Tab order when nothing is selected", async () => {
    const { scroller, ids } = await list();
    expect(ids().length).toBeGreaterThan(3);
    expect(stops(scroller)).toEqual([ids()[0]]);
    expect(linkOf(scroller, ids()[0]!).getAttribute("aria-keyshortcuts")).toBe("ArrowDown ArrowUp");
  });

  it("moves focus, and only focus, between row links with the arrow keys", async () => {
    const { scroller, ids, api } = await list();
    const [a, b] = ids();
    linkOf(scroller, a!).focus();
    const path = window.location.pathname;
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    await waitFor(() => expect(rowOfFocus()).toBe(b));
    expect(stops(scroller)).toEqual([b]);
    // Nothing selected, nothing opened, nothing marked read.
    expect(selectedRows(scroller)).toBe(0);
    expect(window.location.pathname).toBe(path);
    expect(api.calls.some((c) => /mark-read|\/open/.test(String(c.url)))).toBe(false);
    fireEvent.keyDown(document.activeElement!, { key: "ArrowUp" });
    await waitFor(() => expect(rowOfFocus()).toBe(a));
    expect(stops(scroller)).toEqual([a]);
  });

  it("moves between rows from a row's star and menu buttons too, and leaves modified presses alone", async () => {
    const { scroller, ids } = await list();
    const [a, b, c] = ids();
    const rowOf = (id: string) => scroller.querySelector<HTMLElement>(`[data-item-id="${id}"]`)!;
    const star = within(rowOf(a!)).getAllByRole("button")[0]!;
    star.focus();
    expect(fireEvent.keyDown(star, { key: "ArrowDown" })).toBe(false); // taken: focus goes to the next row
    await waitFor(() => expect(rowOfFocus()).toBe(b));
    const menu = within(rowOf(b!)).getAllByRole("button").at(-1)!;
    menu.focus();
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    await waitFor(() => expect(rowOfFocus()).toBe(c));
    expect(screen.queryByRole("menu")).toBeNull(); // ArrowDown did not open the row's menu
    linkOf(scroller, c!).focus();
    expect(fireEvent.keyDown(document.activeElement!, { key: "ArrowDown", shiftKey: true })).toBe(true);
    expect(document.activeElement).toBe(linkOf(scroller, c!));
  });

  it("follows the row that has focus, however it got there", async () => {
    const { scroller, ids } = await list();
    const third = ids()[2]!;
    act(() => within(scroller.querySelector<HTMLElement>(`[data-item-id="${third}"]`)!).getAllByRole("button")[0]!.focus());
    await waitFor(() => expect(stops(scroller)).toEqual([third]));
  });

  it("goes back to the open article when the reader changes it, not to a row focused before", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query.includes("min-width: 900px"),
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }));
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf(Array.from({ length: 5 }, (_, i) => card(i + 1)))),
      "GET /api/items/1001": () => json(detail(1)),
      "GET /api/items/1002": () => json(detail(2)),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
      "POST /api/items/1002/open": () => json({ session_key: "k", item: detail(2, { read: true }) }),
      "POST /api/items/mark-read": () => json({ changed: [], restored: [] }),
    });
    window.history.replaceState({ idx: 0 }, "", "/i/1001?from=unread");
    render(<App client={makeQueryClient({ retry: false })} />);
    const scroller = await screen.findByTestId("list-scroll");
    await waitFor(() => expect(stops(scroller)).toEqual(["1001"]));
    act(() => linkOf(scroller, "1001").focus()); // a row focused earlier
    await waitFor(() => expect(screen.getByRole("button", { name: "Next article" })).toBeEnabled());
    await userEvent.setup().click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
    await waitFor(() => expect(stops(scroller)).toEqual(["1002"]));
  });
  it("brings Shift+Tab back to the row last focused with the arrow keys, from the control after the list", async () => {
    const { scroller, ids } = await list();
    const user = userEvent.setup();
    const b = ids()[1]!;
    act(() => scroller.querySelector<HTMLElement>(`[data-item-id="${ids()[0]}"] a`)!.focus());
    await user.keyboard("{ArrowDown}"); // focus the second row
    await waitFor(() => expect(rowOfFocus()).toBe(b));
    for (let i = 0; i < 5 && scroller.contains(document.activeElement); i++) await user.tab();
    expect(scroller.contains(document.activeElement)).toBe(false);
    await user.tab({ shift: true });
    expect(rowOfFocus()).toBe(b);
  });

  it("keeps the Tab stop row mounted when the list scrolls far away from it", async () => {
    const { scroller, ids } = await list(80);
    const first = ids()[0]!;
    act(() => {
      scroller.scrollTop = 50_000;
      scroller.dispatchEvent(new Event("scroll"));
    });
    await waitFor(() => expect(ids()).not.toContain("1002"));
    expect(ids()).toContain(first);
    expect(stops(scroller)).toEqual([first]);
  });

  it("moves focus to the next row when the focused row leaves the list (nothing selected)", async () => {
    const { scroller, ids } = await list();
    const user = userEvent.setup();
    const [a, b] = ids();
    const row = scroller.querySelector<HTMLElement>(`[data-item-id="${a}"]`)!;
    // From the keyboard: Tab onto the row's More actions button, open its menu, choose Mark as read.
    act(() => within(row).getByRole("button", { name: "More actions" }).focus());
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" }));
    await waitFor(() => expect(ids()).not.toContain(a));
    await waitFor(() => expect(rowOfFocus()).toBe(b), { onTimeout: (e) => new Error(`${e.message}; focus on ${document.activeElement?.outerHTML.slice(0, 200)}`) });
  });
});

describe("the Tab stop row and the virtualizer", () => {
  const range = { startIndex: 40, endIndex: 50, overscan: 8, count: 200 };
  it("adds the stop row to the rendered rows only when it is outside them", () => {
    expect(keepMounted(range, 3)).toEqual([3, ...Array.from({ length: 27 }, (_, i) => 32 + i)]);
    expect(keepMounted(range, 150).at(-1)).toBe(150);
    expect(keepMounted(range, 45)).toEqual(Array.from({ length: 27 }, (_, i) => 32 + i));
    expect(keepMounted(range, -1)).toHaveLength(27);
  });
  it("loads more from where the window reaches, not from the stop row", () => {
    expect(windowReach(range, 200)).toBe(58);
    expect(windowReach({ endIndex: 195 }, 200)).toBe(199);
    expect(windowReach(null, 200)).toBe(0);
  });
});

describe("the Muted strip", () => {
  it("is in the Tab order only on the list's Tab stop row", () => {
    const item = { ...card(1), muted_by: "1", muted_by_name: "Rule" };
    const { rerender } = render(
      <RowTabStop.Provider value={false}>
        <MutedStrip item={item} onRestore={() => {}} onEditRule={() => {}} />
      </RowTabStop.Provider>,
    );
    for (const b of screen.getAllByRole("button")) expect(b.tabIndex).toBe(-1);
    rerender(
      <RowTabStop.Provider value={true}>
        <MutedStrip item={item} onRestore={() => {}} onEditRule={() => {}} />
      </RowTabStop.Provider>,
    );
    for (const b of screen.getAllByRole("button")) expect(b.tabIndex).toBe(0);
  });
});
