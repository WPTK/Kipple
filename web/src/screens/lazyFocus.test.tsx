import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { navigateTo } from "@/test/nav";

// The shell's arrival focus when the routed screen is lazy. This file runs the Feeds screen's chunk for the first time
// (its own module graph per file), so the heading really is missing when the route changes and the observer path runs.

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json({ settings: [], values: {} }),
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("arrival focus on a lazily loaded screen", () => {
  it("lands on the heading when it mounts, and later changes in main do not pull focus back", async () => {
    window.history.replaceState({ idx: 0 }, "", "/l/unread");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByText("Article number 1");
    navigateTo("/feeds");
    // Straight after the route change the screen has not loaded: focus waits on main.
    expect(screen.queryByRole("heading", { level: 1, name: "Feeds" })).toBeNull();
    const h = await screen.findByRole("heading", { level: 1, name: "Feeds" });
    await waitFor(() => expect(h).toHaveFocus());

    // The user moves on; a DOM change inside main afterwards (a re-render adding nodes) must not take focus back.
    const link = screen.getAllByRole("link")[0]!;
    link.focus();
    const main = document.getElementById("main")!;
    main.appendChild(document.createElement("div")).setAttribute("data-route-heading", "");
    await new Promise((r) => setTimeout(r, 50));
    expect(link).toHaveFocus();
  });
});
