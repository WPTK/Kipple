import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { StatsSummary } from "@/api/types";
import { shortDate } from "@/lib/statsFormat";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { richStats } from "./stats.fixtures";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  localStorage.clear();
});
afterEach(() => {
  cleanup(); // unmount before the body is cleared, or the open sheet's portal is removed under React
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

// Alpha Blog alone: what the server answers for feed=1.
const alpha: StatsSummary = {
  ...richStats,
  covered_from: "2026-01-01",
  timed_from: "2026-01-01",
  totals: { items_read: 30, opens: 40, active_seconds: 1800, days_active: 9 },
  read_rate_from: "2026-09-01",
  read_rate: { items_read: 10, new_items: 40, rate: 0.25 },
  sources: [richStats.sources![0]!],
  never_opened: [],
};
const alphaBefore: StatsSummary = { ...alpha, totals: { items_read: 20, opens: 30, active_seconds: 1200, days_active: 6 } };

function setup() {
  return mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
    "GET /api/stats/summary": (u) => {
      if (u.searchParams.get("feed") !== "1") return json(richStats);
      // The earlier period of the comparison starts before the range; the range itself (less today) is Alpha's.
      const from = u.searchParams.get("from");
      return json(from && from < richStats.range!.from ? alphaBefore : alpha);
    },
    "GET /api/items": () => json({ items: [], next_cursor: null }),
  });
}

const feedCalls = (m: ReturnType<typeof mockFetch>) =>
  m.calls.filter((c) => c.url.pathname === "/api/stats/summary" && c.url.searchParams.get("feed"));

function go() {
  window.history.replaceState({ idx: 0 }, "", "/stats");
  return render(<App client={makeQueryClient({ retry: false })} />);
}

describe("Feed drill-down", () => {
  it("a feed row opens a sheet with that feed's numbers over the same range", async () => {
    const m = setup();
    const user = userEvent.setup();
    go();
    const sources = await screen.findByRole("region", { name: "Sources" });
    expect(feedCalls(m)).toHaveLength(0); // nothing is fetched until a feed is opened
    await user.click(within(sources).getByRole("button", { name: /Alpha Blog/ }));
    const sheet = await screen.findByRole("dialog", { name: "Alpha Blog" });
    expect(within(sheet).getByText("The last 30 days.")).toBeInTheDocument();
    expect(await within(sheet).findByText(/^An article counts as read after 10 seconds/)).toBeInTheDocument();
    expect(await within(sheet).findByRole("heading", { name: "Daily activity" })).toBeInTheDocument();
    const call = feedCalls(m)[0]!.url.searchParams;
    expect([call.get("feed"), call.get("range")]).toEqual(["1", "month"]);
    const engagement = within(sheet).getByRole("region", { name: "Engagement" });
    expect(within(engagement).getByText("Quick bounce").nextSibling?.textContent).toBe("50%");
    expect(within(engagement).getByText("Stars").nextSibling?.textContent).toBe("2");
    await user.click(within(sheet).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("a tile in the sheet compares the feed with its own earlier period, complete days only", async () => {
    const m = setup();
    const user = userEvent.setup();
    go();
    const sources = await screen.findByRole("region", { name: "Sources" });
    await user.click(within(sources).getByRole("button", { name: /Alpha Blog/ }));
    const sheet = await screen.findByRole("dialog", { name: "Alpha Blog" });
    await user.click(await within(sheet).findByRole("button", { name: /Items read/ }));
    expect(await within(sheet).findByText("+50% from 20")).toBeInTheDocument();
    const spans = feedCalls(m)
      .filter((c) => c.url.searchParams.get("from"))
      .map((c) => [c.url.searchParams.get("feed"), c.url.searchParams.get("from"), c.url.searchParams.get("to")]);
    expect(spans).toContainEqual(["1", "2026-08-28", "2026-09-25"]);
    expect(spans).toContainEqual(["1", "2026-07-30", "2026-08-27"]);
  });

  it("a feed with too little history says so instead of comparing with a gap", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": (u) => json(u.searchParams.get("feed") ? { ...alpha, covered_from: "2026-09-01" } : richStats),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    const user = userEvent.setup();
    go();
    const sources = await screen.findByRole("region", { name: "Sources" });
    await user.click(within(sources).getByRole("button", { name: /Alpha Blog/ }));
    const sheet = await screen.findByRole("dialog", { name: "Alpha Blog" });
    await user.click(await within(sheet).findByRole("button", { name: /Items read/ }));
    expect(await within(sheet).findByText("Not enough history")).toBeInTheDocument();
  });

  it("a feed with no name still gives its row and sheet one", async () => {
    const nameless = { ...richStats, sources: [{ ...richStats.sources![0]!, feed_title: "" }] };
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": () => json(nameless),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    const user = userEvent.setup();
    go();
    const sources = await screen.findByRole("region", { name: "Sources" });
    await user.click(within(sources).getByRole("button", { name: /Unnamed feed/ }));
    expect(await screen.findByRole("dialog", { name: "Unnamed feed" })).toBeInTheDocument();
  });

  it("the read rate is a percentage, with the counts on a tap", async () => {
    setup();
    const user = userEvent.setup();
    go();
    const sources = await screen.findByRole("region", { name: "Sources" });
    expect(within(sources).getByText("Read rate 25%")).toBeInTheDocument();
    expect(within(sources).getAllByText("Read rate -")).toHaveLength(2); // the feeds with no rate show a dash
    await user.click(within(sources).getByRole("button", { name: /Alpha Blog/ }));
    const sheet = await screen.findByRole("dialog", { name: "Alpha Blog" });
    const rate = await within(sheet).findByRole("button", { name: "25%" });
    expect(rate).toHaveAttribute("aria-expanded", "false");
    await user.click(rate);
    expect(rate).toHaveAttribute("aria-expanded", "true");
    expect(within(rate).getByText(`10 of 40 new since ${shortDate("2026-09-01")}`)).toBeInTheDocument();
  });

  it.each([
    ["no window", { read_rate_from: null, read_rate: { items_read: 0, new_items: 0, rate: null } }, "Not enough history"],
    ["nothing new", { read_rate: { items_read: 3, new_items: 0, rate: null } }, `Nothing new arrived since ${shortDate("2026-09-01")}`],
  ])("a feed with %s shows a dash, never 0%%", async (_, over, note) => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": (u) => json(u.searchParams.get("feed") ? { ...alpha, ...over } : richStats),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    const user = userEvent.setup();
    go();
    const sources = await screen.findByRole("region", { name: "Sources" });
    await user.click(within(sources).getByRole("button", { name: /Alpha Blog/ }));
    const sheet = await screen.findByRole("dialog", { name: "Alpha Blog" });
    const engagement = await within(sheet).findByRole("region", { name: "Engagement" });
    expect(within(engagement).queryByText("0%")).toBeNull();
    await user.click(within(engagement).getByRole("button", { name: "-" }));
    expect(within(engagement).getByText(note)).toBeInTheDocument();
  });

  it("folder rows do not open a sheet", async () => {
    setup();
    const user = userEvent.setup();
    go();
    const sources = await screen.findByRole("region", { name: "Sources" });
    await user.click(within(sources).getByRole("radio", { name: "Folders" }));
    expect(within(sources).queryByRole("button", { name: /Tech/ })).toBeNull();
    expect(within(sources).queryByText(/Read rate/)).toBeNull(); // a folder's quiet feeds are not listed
  });
});
