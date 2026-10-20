import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { readFileSync } from "node:fs";
import { MemoryRouter } from "react-router";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { SCREEN_RANGES } from "@/lib/statsFormat";
import { BackButton } from "@/ui/BackButton";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

// The phone's header, tab bar and back buttons (issues #420, #422, #424).

class NoES {
  addEventListener() {}
  close() {}
}

/** matchMedia that answers true for any query containing one of `on` (a phone is none of them). */
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

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

let refreshes = 0;
beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  prefsStore.set({ ...DEFAULT_PREFS });
  resetDevicePrefs();
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
  clearListMemory();
  vi.stubGlobal("EventSource", NoES);
  refreshes = 0;
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1), card(2)])),
    "POST /api/refresh": () => {
      refreshes += 1;
      return json({ run_id: "r1", total: 1 }, 202);
    },
  });
});
afterEach(() => vi.unstubAllGlobals());

const header = () => screen.getByRole("banner");
const buttonNames = () => within(header()).getAllByRole("button").map((b) => b.getAttribute("aria-label") ?? b.textContent);

describe("the list header on a phone (#422)", () => {
  it("has no Refresh button: the pull on the list and the menu's Refresh all feeds do it", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(buttonNames().filter((n) => /Refresh/.test(n ?? ""))).toEqual([]);
    const user = userEvent.setup();
    await user.click(within(header()).getByRole("button", { name: "List actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Refresh all feeds" }));
    await vi.waitFor(() => expect(refreshes).toBe(1));
  });

  it("keeps at most four controls", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(buttonNames().length).toBeLessThanOrEqual(4);
  });

  it("keeps the Refresh button where there is room (wide)", async () => {
    media("min-width: 900px");
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(within(header()).getByRole("button", { name: "Refresh all feeds" })).toBeInTheDocument();
    await userEvent.setup().click(within(header()).getByRole("button", { name: "List actions" }));
    expect(screen.queryByRole("menuitem", { name: "Refresh all feeds" })).toBeNull();
  });
});

describe("the list header on a phone held sideways (#420)", () => {
  it("puts the Show pills on the title row", async () => {
    media("max-height: 500px");
    go("/l/unread");
    await screen.findByText("Article number 1");
    const h1 = within(header()).getByRole("heading", { level: 1 });
    const pills = within(header()).getByRole("navigation", { name: "Show" });
    expect(pills.parentElement).toBe(h1.parentElement);
  });

  it("keeps the pills on their own row on a phone held upright", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    const h1 = within(header()).getByRole("heading", { level: 1 });
    const pills = within(header()).getByRole("navigation", { name: "Show" });
    expect(pills.parentElement).not.toBe(h1.parentElement);
  });

  it("puts the feed switch on the title row too", async () => {
    media("max-height: 500px");
    const first = bootstrap.feeds[0]!;
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [first, { ...first, id: "2", title: "Second Feed" }] }),
      "GET /api/items": () => json(pageOf([card(1), card(2)])),
    });
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    const h1 = within(header()).getByRole("heading", { level: 1 });
    expect(within(header()).getByRole("group", { name: "Switch feed" }).parentElement).toBe(h1.parentElement);
  });

  it("has a tab bar that is one row of icon and name, and a smaller --tabbar-h", () => {
    const css = readFileSync("src/index.css", "utf8");
    expect(css).toMatch(/@media \(max-height: 500px\) \{\s*:root \{ --tabbar-h: 2\.75rem; \}/);
    const shell = readFileSync("src/shell/AppShell.tsx", "utf8");
    expect(shell).toContain("[@media(max-height:500px)]:flex-row");
    expect(shell).toContain("[@media(max-height:500px)]:pt-0");
  });
});

describe("one back pattern (#424)", () => {
  it("BackButton is a chevron link when the parent has an address", () => {
    render(
      <MemoryRouter>
        <BackButton to="/stats" label="Back to Stats" />
      </MemoryRouter>,
    );
    const a = screen.getByRole("link", { name: "Back to Stats" });
    expect(a).toHaveAttribute("href", "/stats");
    expect(a).toHaveAttribute("title", "Back to Stats");
    expect(a.querySelector("svg")).not.toBeNull();
    expect(a.textContent).toBe("");
  });

  it("BackButton is a chevron button when the way back is a call", async () => {
    const onClick = vi.fn();
    render(<BackButton onClick={onClick} label="Back to Settings" />);
    await userEvent.setup().click(screen.getByRole("button", { name: "Back to Settings" }));
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("Your year goes back to Stats with the chevron, not a text link", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": () => json({ enabled: false }),
    });
    go("/stats/wrapped");
    const back = await screen.findByRole("link", { name: "Back to Stats" });
    expect(back).toHaveAttribute("href", "/stats");
    expect(back.textContent).toBe("");
  });

  it("a Settings group and Feed health use it too", async () => {
    go("/settings/appearance");
    expect(await screen.findByRole("button", { name: "Back to Settings" })).toBeInTheDocument();
    document.body.innerHTML = "";
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/health": () => json({}) });
    go("/health");
    expect(await screen.findByRole("link", { name: "Back to Feeds" })).toHaveAttribute("href", "/feeds");
  });
});

describe("the Stats ranges (#424)", () => {
  it("call the all-time bars one per month Monthly, not Months beside Month", () => {
    const labels = SCREEN_RANGES.map((r) => r.label);
    expect(labels).toEqual(["Week", "Month", "Year", "Monthly", "All"]);
    expect(new Set(labels.map((l) => l.toLowerCase().replace(/s$/, ""))).size).toBe(labels.length);
  });
});
