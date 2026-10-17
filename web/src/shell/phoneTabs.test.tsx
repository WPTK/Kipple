import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { render, screen, waitFor, within } from "@testing-library/react";
import { readFileSync } from "node:fs";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

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

async function expectTab(path: string, name: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  render(<App client={makeQueryClient({ retry: false })} />);
  await waitFor(() => {
    const nav = screen.getByRole("navigation", { name: "Primary" });
    const current = within(nav)
      .getAllByRole("link")
      .filter((l) => l.getAttribute("aria-current") === "page")
      .map((l) => l.textContent ?? "");
    expect(current).toHaveLength(1);
    expect(current[0]).toContain(name);
  });
}

describe("the phone tab bar's current tab", () => {
  it.each(["/l/unread", "/l/all", "/l/starred"])("%s is the Unread tab", async (path) => {
    await expectTab(path, "Unread");
  });

  it.each(["/l/unread?feed=1", "/l/all?folder=1", "/feeds", "/health"])("%s is the Feeds tab", async (path) => {
    await expectTab(path, "Feeds");
  });

  it("Feed Health has a way back to Feeds", async () => {
    window.history.replaceState({ idx: 0 }, "", "/health");
    render(<App client={makeQueryClient({ retry: false })} />);
    expect(await screen.findByRole("link", { name: "Back to Feeds" })).toHaveAttribute("href", "/feeds");
  });
});

describe("the reading column", () => {
  it("is measured in rem, so a title and the body (different font sizes) share one width", () => {
    const css = readFileSync("src/index.css", "utf8");
    const measures = [...css.matchAll(/--kp-measure:\s*([^;]+);/g)].map((m) => m[1]);
    expect(measures.length).toBeGreaterThan(0);
    for (const m of measures) expect(m).toMatch(/rem$/);
  });
});

describe("the list header on a phone", () => {
  it("has no order toggle (it lives in the Layout menu), and List actions has no Previous/Next feed", async () => {
    window.history.replaceState({ idx: 0 }, "", "/l/unread?feed=1");
    render(<App client={makeQueryClient({ retry: false })} />);
    const actions = await screen.findByRole("button", { name: "List actions" });
    expect(screen.queryByRole("button", { name: "Oldest first" })).toBeNull();
    await userEvent.setup().click(actions);
    await screen.findByRole("menuitem", { name: /Mark all as read/ });
    expect(screen.queryAllByRole("menuitem", { name: /^(Previous|Next) / })).toHaveLength(0);
  });
});
