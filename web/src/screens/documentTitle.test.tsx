import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

// WCAG 2.4.2: every screen names the page ("<heading> - Kipple"), so the tab, history and window switcher differ.

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
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/items/1": () => json(detail(1)),
    "GET /api/settings": () => json({ settings: [], values: {} }),
  });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.title = "Kipple";
});

describe("the document title", () => {
  it("names the list", async () => {
    go("/l/starred");
    await screen.findByRole("heading", { level: 1, name: "Starred" });
    expect(document.title).toBe("Starred - Kipple");
  });

  it("names Search", async () => {
    go("/search");
    await screen.findByRole("heading", { level: 1, name: "Search" });
    expect(document.title).toBe("Search - Kipple");
  });

  it("names a Settings section", async () => {
    go("/settings/about");
    await screen.findByRole("heading", { level: 1, name: "About" });
    expect(document.title).toBe("About - Settings - Kipple");
  });

  it("names the Feeds screen", async () => {
    go("/feeds");
    await screen.findByRole("heading", { level: 1, name: "Feeds" });
    expect(document.title).toBe("Feeds - Kipple");
  });

  it("names the article by its title", async () => {
    go("/i/1");
    const h = await screen.findByRole("heading", { level: 1, name: detail(1).title });
    await waitFor(() => expect(document.title).toBe(`${detail(1).title} - Kipple`));
    expect(h).toHaveTextContent(detail(1).title);
  });

  it("keeps the suffix on an article whose title names Kipple", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
      "GET /api/items/1": () => json(detail(1, { title: "Kipple 1.0 released" })),
    });
    go("/i/1");
    await screen.findByRole("heading", { level: 1, name: "Kipple 1.0 released" });
    await waitFor(() => expect(document.title).toBe("Kipple 1.0 released - Kipple"));
  });

  it("names an article that could not be opened, with an h1", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
      "GET /api/items/7": () => json({ error: "boom" }, 500),
    });
    go("/i/7");
    await screen.findByRole("heading", { level: 1, name: "Couldn't open this article" });
    expect(document.title).toBe("Couldn't open this article - Kipple");
    // The failure heading takes focus when it appears.
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 })).toHaveFocus());
  });

  it("names the not-found page, with an h1", async () => {
    go("/nowhere");
    await screen.findByRole("heading", { level: 1, name: "Page not found" });
    expect(document.title).toBe("Page not found - Kipple");
  });
});
