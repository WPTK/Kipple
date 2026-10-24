import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { focusIsLost, forgetFocusedRows, restoreFocusedRow, useRememberFocusedRow } from "@/lib/arrivalFocus";
import { SHORT_QUERY } from "@/lib/useMedia";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  prefsStore.set({ ...DEFAULT_PREFS });
  vi.stubGlobal("EventSource", NoES);
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/health": () => json({}),
  });
});
afterEach(() => vi.unstubAllGlobals());

async function sidebarCurrent(path: string): Promise<string[]> {
  wide();
  window.history.replaceState({ idx: 0 }, "", path);
  render(<App client={makeQueryClient({ retry: false })} />);
  const nav = await screen.findByRole("navigation", { name: "Primary" });
  await waitFor(() => expect(within(nav).getByRole("link", { name: "Feeds" })).toBeInTheDocument());
  return within(nav)
    .getAllByRole("link")
    .filter((l) => l.getAttribute("aria-current") === "page")
    .map((l) => (l.textContent ?? "").replace(/\d+\+?$/, "").trim());
}

describe("the sidebar marks one destination (#638)", () => {
  it.each([
    ["/l/unread", "Unread"],
    ["/l/all", "All articles"],
    ["/l/starred", "Starred"],
    ["/feeds", "Feeds"],
    ["/feeds/index", "Feed index"],
    ["/health", "Feeds"],
    ["/search", "Search"],
    ["/settings", "Settings"],
  ])("%s marks %s", async (path, name) => {
    expect(await sidebarCurrent(path)).toEqual([name]);
  });

  it("marks no destination row on a feed's list, where the tree marks the feed", async () => {
    expect(await sidebarCurrent("/l/unread?feed=1")).toEqual([]);
  });
});

describe("history steps give focus back (#641)", () => {
  it("Back returns focus to the row left, and the history is never written by focus or presses", async () => {
    forgetFocusedRows();
    wide();
    window.history.replaceState({ idx: 0, key: "list" }, "", "/l/all");
    render(<App client={makeQueryClient({ retry: false })} />);
    const row = await waitFor(() => {
      const a = document.querySelector<HTMLElement>("[data-item-id] a");
      if (!a) throw new Error("no row yet");
      return a;
    });
    const id = row.closest<HTMLElement>("[data-item-id]")?.dataset.itemId;
    const replace = vi.spyOn(window.history, "replaceState");
    row.focus();
    await userEvent.setup().click(row);
    await waitFor(() => expect(window.location.pathname).toMatch(/^\/i\//));
    (document.activeElement as HTMLElement | null)?.blur();
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe("/l/all"));
    await waitFor(() => expect(document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id")).toBe(id));
    expect(replace).not.toHaveBeenCalled();
    replace.mockRestore();
  });
});
describe("restoring focus after a step (#641)", () => {
  it("returns the row recorded in the entry, and tells a lost focus from a kept one", () => {
    forgetFocusedRows();
    document.body.innerHTML = `<main id="m" tabindex="-1"><div data-item-id="a/1"><a href="#x">One</a></div></main>`;
    const main = document.getElementById("m");
    window.history.replaceState({ idx: 2, key: "e1" }, "");
    expect(focusIsLost(main)).toBe(true);
    // Nothing recorded for this entry: the shell falls back to the heading.
    expect(restoreFocusedRow(main)).toBe(false);
    const hook = renderHook(() => useRememberFocusedRow());    document.querySelector<HTMLElement>("[data-item-id] a")?.focus();
    expect(focusIsLost(main)).toBe(false);
    (document.activeElement as HTMLElement).blur();
    hook.unmount();    expect(restoreFocusedRow(main)).toBe(true);
    expect(document.activeElement?.textContent).toBe("One");
    // The row is no longer on the page: false, so the heading takes focus.
    document.body.innerHTML = `<main id="m" tabindex="-1"></main>`;
    expect(restoreFocusedRow(document.getElementById("m"))).toBe(false);
    expect(focusIsLost(document.getElementById("m"))).toBe(true);
    forgetFocusedRows();
    document.body.innerHTML = "";
  });

  it("keeps focus in the search box when the first push after a step changes only the query", async () => {
    wide();
    window.history.replaceState({ idx: 0, key: "start" }, "", "/search");
    render(<App client={makeQueryClient({ retry: false })} />);
    const user = userEvent.setup();
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await user.click(await within(nav).findByRole("link", { name: "Settings" }));
    await waitFor(() => expect(window.location.pathname).toMatch(/^\/settings/));
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe("/search"));
    const box = await screen.findByRole("searchbox");
    await user.click(box);
    await user.type(box, "foo");
    await waitFor(() => expect(window.location.search).toContain("q=foo"), { timeout: 3000 });
    await new Promise((r) => setTimeout(r, 100));
    expect(document.activeElement).toBe(box);
  });
});

describe("the landscape tab bar (#643)", () => {
  it("puts the unread count after the Articles name, not on the icon", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query === SHORT_QUERY, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }));
    window.history.replaceState({ idx: 0 }, "", "/l/all");
    render(<App client={makeQueryClient({ retry: false })} />);
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    const badge = await within(nav).findByTestId("unread-count");
    expect(badge.className).toContain("ml-1");
    expect(badge.className).not.toContain("absolute");
    expect(badge.closest("a")?.textContent).toMatch(/^Articles\s*\d/);
  });
});
