import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { focusIsLost, restoreFocusedRow } from "@/lib/arrivalFocus";
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

describe("focus after a step through history (#641)", () => {
  it("goes back to the row recorded in the entry, and a lost focus is told from a kept one", () => {
    document.body.innerHTML = `<main id="m" tabindex="-1"><div data-item-id="a/1"><a href="#x">One</a></div></main>`;
    const main = document.getElementById("m");
    window.history.replaceState({ idx: 2, kpRow: "a/1" }, "");
    expect(focusIsLost(main)).toBe(true);
    expect(restoreFocusedRow(main)).toBe(true);
    expect(document.activeElement?.textContent).toBe("One");
    expect(focusIsLost(main)).toBe(false);
    window.history.replaceState({ idx: 2, kpRow: "gone" }, "");
    expect(restoreFocusedRow(main)).toBe(false);
    window.history.replaceState({ idx: 2 }, "");
    expect(restoreFocusedRow(main)).toBe(false);
  });
});
