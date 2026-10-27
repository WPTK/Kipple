import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { SCREEN_RANGES } from "@/lib/statsFormat";
import { NARROW_QUERY, SHORT_QUERY, WIDE_QUERY } from "@/lib/useMedia";
import { BackButton } from "@/ui/BackButton";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

// The phone's header, tab bar and back buttons (issues #420, #422, #424).

class NoES {
  addEventListener() {}
  close() {}
}

/**
 * matchMedia whose answers are exactly the queries in `on` (a phone held upright is none of them). Changing them
 * fires `change` on the queries that are listened to, as a rotation does.
 */
let on = new Set<string>();
const listeners = new Set<{ query: string; cb: () => void }>();
function media(...queries: string[]) {
  on = new Set(queries);
  vi.stubGlobal("matchMedia", (query: string) => ({
    get matches() {
      return on.has(query);
    },
    media: query,
    onchange: null,
    addEventListener(_: string, cb: () => void) {
      listeners.add({ query, cb });
    },
    removeEventListener(_: string, cb: () => void) {
      for (const l of listeners) if (l.query === query && l.cb === cb) listeners.delete(l);
    },
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}
/** The device turns (or the window resizes): `queries` now match, the rest do not. */
function rotateTo(...queries: string[]) {
  on = new Set(queries);
  act(() => {
    for (const l of [...listeners]) l.cb();
  });
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
  listeners.clear();
  media();
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1), card(2)])),
    "POST /api/refresh": () => {
      refreshes += 1;
      return json({ run_id: "r1", total: 1 }, 202);
    },
  });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.documentElement.removeAttribute("data-short");
});

const header = () => screen.getByRole("banner");
const buttonNames = () => within(header()).getAllByRole("button").map((b) => b.getAttribute("aria-label") ?? b.textContent);

const showPills = () => within(header()).getByRole("navigation", { name: "Show" });
const titleRow = () => within(header()).getByRole("heading", { level: 1 }).closest("[data-title-row]");
const tabs = () => within(screen.getByRole("navigation", { name: "Primary" })).getAllByRole("link");
const actionsMenu = () => within(header()).getByRole("button", { name: "List actions" });

describe("the list header on a phone (#422)", () => {
  it("has no Refresh button: the pull on the list and the menu's Refresh all feeds do it", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(buttonNames().filter((n) => /Refresh/.test(n ?? ""))).toEqual([]);
    const user = userEvent.setup();
    await user.click(actionsMenu());
    await user.click(await screen.findByRole("menuitem", { name: "Refresh all feeds" }));
    await vi.waitFor(() => expect(refreshes).toBe(1));
  });

  it("keeps at most four controls", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(buttonNames().length).toBeLessThanOrEqual(4);
  });

  it("has no Refresh button on a wide screen either: it is in the menu, and five controls at most", async () => {
    media(WIDE_QUERY);
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(buttonNames().filter((n) => /Refresh/.test(n ?? ""))).toEqual([]);
    expect(buttonNames().length).toBeLessThanOrEqual(5);
    expect(within(header()).queryByRole("link", { name: "Settings" })).toBeNull();
    await userEvent.setup().click(actionsMenu());
    expect(await screen.findByRole("menuitem", { name: "Refresh all feeds" })).toBeInTheDocument();
  });

});

describe("Refresh all feeds in the actions menu (#422)", () => {
  it("is reachable from the keyboard: open the menu, it is the first item, Enter refreshes", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    actionsMenu().focus();
    await user.keyboard("{Enter}");
    const item = await screen.findByRole("menuitem", { name: "Refresh all feeds" });
    await waitFor(() => expect(item).toHaveFocus());
    await user.keyboard("{Enter}");
    await vi.waitFor(() => expect(refreshes).toBe(1));
  });

  it("while a refresh runs a line shows it, the trigger keeps its icon, and the item stays in the arrow-key order, announced as disabled", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    expect(screen.queryByTestId("refresh-line")).toBeNull();
    await user.click(actionsMenu());
    await user.click(await screen.findByRole("menuitem", { name: "Refresh all feeds" }));
    await vi.waitFor(() => expect(refreshes).toBe(1));
    await waitFor(() => expect(screen.getByTestId("refresh-line")).toBeInTheDocument());
    expect(actionsMenu().querySelector("svg.animate-spin")).toBeNull();

    actionsMenu().focus();
    await user.keyboard("{Enter}");
    const item = await screen.findByRole("menuitem", { name: "Refreshing" });
    expect(item).toHaveAttribute("aria-disabled", "true");
    await waitFor(() => expect(item).toHaveFocus());
    await user.keyboard("{Enter}");
    expect(refreshes).toBe(1);
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });

  it("works in a search, where pulling the list does not refresh", async () => {
    go("/l/all?q=Article");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(actionsMenu());
    await user.click(await screen.findByRole("menuitem", { name: "Refresh all feeds" }));
    await vi.waitFor(() => expect(refreshes).toBe(1));
  });
});

describe("the list header on a phone held sideways (#420)", () => {
  it("puts the Show pills on the title row", async () => {
    media(SHORT_QUERY);
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(showPills().closest("[data-title-row]")).toBe(titleRow());
  });

  it("keeps the pills on their own row on a phone held upright, also when its keyboard has taken the height", async () => {
    // The keyboard shrinks the height but not the orientation: the short query does not match.
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(showPills().closest("[data-title-row]")).not.toBe(titleRow());
  });

  it("puts the feed switch on the title row too", async () => {
    media(SHORT_QUERY);
    const first = bootstrap.feeds[0]!;
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [first, { ...first, id: "2", title: "Second Feed" }] }),
      "GET /api/items": () => json(pageOf([card(1), card(2)])),
    });
    go("/l/unread?feed=1");
    await screen.findByText("Article number 1");
    expect(within(header()).getByRole("group", { name: "Switch feed" }).closest("[data-title-row]")).toBe(titleRow());
  });

  it("makes the Show pills a select on a narrow screen, where the row cannot hold them, and the select changes the list", async () => {
    media(SHORT_QUERY, NARROW_QUERY);
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(within(header()).queryByRole("navigation", { name: "Show" })).toBeNull();
    const select = within(header()).getByRole("combobox", { name: "Show" });
    expect(select.closest("[data-title-row]")).toBe(titleRow());
    await userEvent.setup().selectOptions(select, "All");
    expect(window.location.pathname).toBe("/l/all");
  });

  it("swaps the layout when the phone is turned while the screen is open", async () => {
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(showPills().closest("[data-title-row]")).not.toBe(titleRow());
    expect(tabs()[0]).toHaveClass("flex-col");
    expect(document.documentElement).not.toHaveAttribute("data-short");

    rotateTo(SHORT_QUERY);
    expect(showPills().closest("[data-title-row]")).toBe(titleRow());
    expect(tabs()[0]).toHaveClass("flex-row");
    expect(tabs()[0]).not.toHaveClass("flex-col");
    expect(document.documentElement).toHaveAttribute("data-short");

    rotateTo(SHORT_QUERY, NARROW_QUERY);
    expect(within(header()).getByRole("combobox", { name: "Show" })).toBeInTheDocument();

    rotateTo();
    expect(showPills().closest("[data-title-row]")).not.toBe(titleRow());
    expect(tabs()[0]).toHaveClass("flex-col");
    expect(document.documentElement).not.toHaveAttribute("data-short");
  });

  it("does not shorten the sidebar layout of a wide window that is short", async () => {
    media(WIDE_QUERY, SHORT_QUERY);
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(showPills().closest("[data-title-row]")).not.toBe(titleRow());
    expect(document.documentElement).not.toHaveAttribute("data-short");
  });

  it("has a tab bar with no padding above the tabs", async () => {
    media(SHORT_QUERY);
    go("/l/unread");
    await screen.findByText("Article number 1");
    const bar = screen.getByRole("navigation", { name: "Primary" });
    expect(bar).not.toHaveClass("pt-2");
  });
});

describe("one back pattern (#424)", () => {
  it("BackButton is a chevron link to the parent", () => {
    render(
      <MemoryRouter>
        <BackButton to="/stats" ariaLabel="Back to Stats" />
      </MemoryRouter>,
    );
    const a = screen.getByRole("link", { name: "Back to Stats" });
    expect(a).toHaveAttribute("href", "/stats");
    expect(a).toHaveAttribute("title", "Back to Stats");
    expect(a.querySelector("svg")).not.toBeNull();
    expect(a.textContent).toBe("");
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
    expect(await screen.findByRole("link", { name: "Back to Settings" })).toHaveAttribute("href", "/settings");
    document.body.innerHTML = "";
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/health": () => json({}) });
    go("/health");
    expect(await screen.findByRole("link", { name: "Back to Feeds" })).toHaveAttribute("href", "/feeds");
  });

  it("goes back in the history when the entry before is in the app, and focus arrives on the screen as for any route", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": () => json({ enabled: false }),
    });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: /^Appearance/ }));
    await user.click(await screen.findByRole("link", { name: "Back to Settings" }));
    await waitFor(() => expect(window.location.pathname).toBe("/settings"));
    // The shell moves focus on every change of screen: here to the row of the group just left.
    await waitFor(() => expect(screen.getByRole("link", { name: /^Appearance/ })).toHaveFocus());
    expect(window.history.state.idx).toBe(0); // a step back, not a new entry on top
  });

  it("goes to the parent when the screen is the first entry", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": () => json({ enabled: false }),
    });
    go("/stats/wrapped");
    await userEvent.setup().click(await screen.findByRole("link", { name: "Back to Stats" }));
    await waitFor(() => expect(window.location.pathname).toBe("/stats"));
    expect(window.history.state.idx).toBe(0); // replaced: the system Back does not return to the screen just left
  });

  it("goes to the parent, not back, when the entry before is another screen", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": () => json({ enabled: false }),
    });
    go("/stats");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: /Settings > Stats/ }));
    await user.click(await screen.findByRole("link", { name: "Back to Settings" }));
    await waitFor(() => expect(window.location.pathname).toBe("/settings"));
    expect(window.history.state.idx).toBe(1); // replaced the group: Stats, Settings
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe("/stats"));
  });
});

describe("the Stats ranges (#424)", () => {
  it("call the all-time bars one per month Monthly, not Months beside Month", () => {
    const labels = SCREEN_RANGES.map((r) => r.label);
    expect(labels).toEqual(["Week", "Month", "Year", "Monthly", "All"]);
    expect(new Set(labels.map((l) => l.toLowerCase().replace(/s$/, ""))).size).toBe(labels.length);
  });
});