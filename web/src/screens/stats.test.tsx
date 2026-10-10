import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { StatsRange, StatsSummary } from "@/api/types";
import { QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { usePatchSettings } from "@/api/admin";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { emptyStats, offStats, richStats } from "./stats.fixtures";

class NoES {
  addEventListener() {}
  close() {}
}

function setup(stats: (range: string) => StatsSummary | Response, settings: Record<string, unknown> = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, settings }),
    "GET /api/stats/summary": (u) => {
      const r = stats(u.searchParams.get("range") ?? "");
      return r instanceof Response ? r : json(r);
    },
    "GET /api/items": () => json({ items: [], next_cursor: null }),
  });
}

function go(path = "/stats") {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

function setupSpan(stats: (u: URL) => StatsSummary | Response) {
  return mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
    "GET /api/stats/summary": (u) => {
      const r = stats(u);
      return r instanceof Response ? r : json(r);
    },
    "GET /api/items": () => json({ items: [], next_cursor: null }),
  });
}

const statsCalls = (m: ReturnType<typeof mockFetch>) => m.calls.filter((c) => c.url.pathname === "/api/stats/summary");

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  localStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("Stats screen", () => {
  it("renders every section from a rich fixture", async () => {
    const m = setup(() => richStats);
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(statsCalls(m)[0]?.url.searchParams.get("range")).toBe("month"); // default
    const summary = screen.getByRole("region", { name: "Summary" });
    expect(within(summary).getByText("84")).toBeInTheDocument();
    expect(within(summary).getByText("1h 20m")).toBeInTheDocument();
    expect(within(summary).getByText("12")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /Items read per day/ })).toBeInTheDocument();
    expect(screen.getByText("9 days")).toBeInTheDocument(); // longest streak
    expect(screen.getByText("Tuesday 8 pm: 12 min")).toBeInTheDocument();
    expect(screen.getByText(/Busiest day: Tuesday/)).toBeInTheDocument();
    expect(screen.getByText(/Busiest hour: 8 pm/)).toBeInTheDocument();
    expect(screen.getByText(/Average read length: 3 min/)).toBeInTheDocument();
    expect(screen.getByText(/Longest read: A very long essay, from Long Reads, 22 min/)).toBeInTheDocument();
    expect(screen.getByText("Most starred: Gamma Daily (5), Alpha Blog (2), Beta News (1).")).toBeInTheDocument();
    expect(screen.getByText("Quiet Feed")).toBeInTheDocument();
    expect(screen.getByText(/subscribed on Aug 15, 2026/)).toBeInTheDocument();
    expect(screen.queryByText(/Only \d+ days? of reading/)).toBeNull(); // 12 active days
  });

  it("says what counts as a read, and how many reads are unmeasured legacy opens", async () => {
    setup(() => ({ ...richStats, totals: { ...richStats.totals!, legacy_opens: 0 } }));
    const r = go();
    const summary = await screen.findByRole("region", { name: "Summary" });
    expect(within(summary).getByText(/counts as read after 10 seconds of reading, or 3 seconds once you.ve scrolled a quarter of the way down./)).toBeInTheDocument();
    expect(within(summary).queryByText(/before reading time was recorded/)).toBeNull();
    r.unmount();

    setup(() => ({ ...richStats, totals: { ...richStats.totals!, legacy_opens: 7 } }));
    const r2 = go();
    const s2 = await screen.findByRole("region", { name: "Summary" });
    expect(within(s2).getByText(/7 articles here were opened before Kipple measured reading time. They count as read but add no time\./)).toBeInTheDocument();
    r2.unmount();

    setup(() => ({ ...richStats, totals: { ...richStats.totals!, legacy_opens: 1 } }));
    go();
    const s3 = await screen.findByRole("region", { name: "Summary" });
    expect(within(s3).getByText(/1 article here was opened before Kipple measured reading time. It counts as read but adds no time/)).toBeInTheDocument();
  });

  it("shows friendly empty states, not blank charts", async () => {
    setup(() => emptyStats);
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(screen.getByText(/No reading in this range yet/)).toBeInTheDocument();
    expect(screen.getByText(/Nothing to show yet/)).toBeInTheDocument();
    expect(screen.getByText(/Nothing here until/)).toBeInTheDocument();
    expect(screen.getByText(/Nothing read yet/)).toBeInTheDocument();
    expect(screen.getByText(/No streak yet/)).toBeInTheDocument();
    expect(screen.getByText(/Every feed you subscribe to has had an article opened/)).toBeInTheDocument();
  });

  it("switches range, refetches and remembers the choice", async () => {
    const user = userEvent.setup();
    const m = setup(() => richStats);
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    await user.click(screen.getByRole("radio", { name: "Year" }));
    await waitFor(() => expect(statsCalls(m).map((c) => c.url.searchParams.get("range"))).toContain("year"));
    expect(localStorage.getItem("kipple.stats.range")).toBe("year");
  });

  it("starts from the remembered range", async () => {
    localStorage.setItem("kipple.stats.range", "week");
    const m = setup(() => richStats);
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(statsCalls(m)[0]?.url.searchParams.get("range")).toBe("week");
    expect(screen.getByRole("radio", { name: "Week" })).toBeChecked();
  });

  it("notes a young history, not a sparse week", async () => {
    setup(() => ({ ...richStats, first_event_date: "2026-09-22" })); // to = 2026-09-26: five days of history
    go();
    expect(await screen.findByText("Only 5 days of reading so far.")).toBeInTheDocument();
  });

  it("does not note low data when the history is old, however few days were active in the range", async () => {
    setup(() => ({ ...richStats, totals: { ...richStats.totals!, days_active: 3 } }));
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(screen.queryByText(/Only \d+ days? of reading/)).toBeNull();
  });

  it("does not note low data for All", async () => {
    localStorage.setItem("kipple.stats.range", "all");
    setup(() => ({ ...richStats, first_event_date: "2026-09-22" }));
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(screen.queryByText(/Only 5 days/)).toBeNull();
  });

  it("does not show the low-data note while a new range loads", async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": async (u) => {
        if (u.searchParams.get("range") === "year") await gate;
        return json({ ...richStats, first_event_date: "2026-09-22" });
      },
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    go();
    await screen.findByText(/Only 5 days of reading/);
    await user.click(screen.getByRole("radio", { name: "Year" }));
    await waitFor(() => expect(screen.queryByText(/Only 5 days of reading/)).toBeNull());
    release();
    expect(await screen.findByText(/Only 5 days of reading/)).toBeInTheDocument();
  });

  it("says a running longest streak is still going, and dates a finished one", async () => {
    setup(() => ({ ...richStats, streaks: { current: 9, longest: 9, longest_end: "2026-09-26" } }));
    const first = go();
    expect(await screen.findByText("Longest streak, still going")).toBeInTheDocument();
    expect(screen.queryByText(/Longest streak, ended/)).toBeNull();
    first.unmount();
    setup(() => ({ ...richStats, streaks: { current: 9, longest: 9, longest_end: "2026-09-25" } })); // read yesterday, not yet today: the run is alive
    const second = go();
    expect(await screen.findByText("Longest streak, still going")).toBeInTheDocument();
    second.unmount();
    setup(() => richStats); // longest ended 2026-09-10, current 3
    go();
    expect(await screen.findByText("Longest streak, ended Sep 10")).toBeInTheDocument();
  });

  it("invalidates the stats queries when a setting is saved", async () => {
    const qc = makeQueryClient({ retry: false });
    qc.setQueryData(["stats", "month"], richStats);
    mockFetch({ "PATCH /api/settings": () => json({ settings: [], values: { "stats.week_start": "monday" } }) });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => usePatchSettings(), { wrapper });
    expect(qc.getQueryState(["stats", "month"])?.isInvalidated).toBe(false);
    await result.current.mutateAsync({ "stats.week_start": "monday" });
    await waitFor(() => expect(qc.getQueryState(["stats", "month"])?.isInvalidated).toBe(true));
  });

  it("keeps a very long feed name from displacing the columns and never clips the suffix", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("640"), media: q, addEventListener() {}, removeEventListener() {} }));
    const long = "L".repeat(120);
    setup(() => ({ ...richStats, sources: [{ ...richStats.sources![0]!, feed_title: long, subscribed: false }] }));
    go();
    const table = await screen.findByRole("table", { name: /by items read/ });
    expect(table.className).toContain("table-fixed");
    const suffix = within(table).getByText("(unsubscribed)");
    expect(suffix.className).toContain("shrink-0");
    const name = within(table).getByText(long);
    expect(name.className).toContain("truncate");
    expect(name.contains(suffix)).toBe(false);
    expect(within(table).getByRole("columnheader", { name: "Stars" })).toBeInTheDocument();
  });

  it("keeps the suffix outside the truncating name in the phone list", async () => {
    const long = "L".repeat(120);
    setup(() => ({ ...richStats, sources: [{ ...richStats.sources![0]!, feed_title: long, subscribed: false }] }));
    go();
    const list = await screen.findByRole("list", { name: /by items read/ });
    expect(within(list).getByText(long).contains(within(list).getByText("(unsubscribed)"))).toBe(false);
  });

  it("says when the sources list was capped", async () => {
    setup(() => ({ ...richStats, sources_truncated: true }));
    go();
    expect(await screen.findByText("Showing the most active feeds only. Folder totals count just those.")).toBeInTheDocument();
  });

  it("does not call an untimed opens cell empty when the range also has timed reading", async () => {
    setup(() => ({ ...richStats, heatmap: [...richStats.heatmap!, { weekday: 3, hour: 14, active_seconds: 0, opens: 3 }] }));
    go();
    const cell = (await screen.findByText("Wednesday 2 pm: 3 opens, no reading time recorded")).closest("td")!;
    expect(cell).toHaveAttribute("data-level", "1");
    expect(screen.getByText("Tuesday 8 pm: 12 min").closest("td")).toHaveAttribute("data-level", "4");
    expect(screen.getByText("Wednesday 3 pm: none").closest("td")).toHaveAttribute("data-level", "0");
    expect(screen.getByText(/Opens with no recorded time show as the lightest shade/)).toBeInTheDocument();
  });

  it("keeps Export available when statistics are off", async () => {
    const user = userEvent.setup();
    setup(() => offStats, { "stats.enabled": false });
    go();
    expect(await screen.findByRole("button", { name: "Delete a date range…" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete all statistics…" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Export…" }));
    expect(await screen.findByRole("dialog", { name: "Export statistics" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
  });

  it("keeps the data actions on Settings when the settings request fails", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/settings": () => json({ error: "boom" }, 500),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    go("/settings/statistics");
    expect(await screen.findByText(/Couldn't load your settings/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export…" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete a date range…" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete all statistics…" })).toBeInTheDocument();
  });

  it("opens the export dialog from the Export button beside the Range control", async () => {
    const user = userEvent.setup();
    setup(() => richStats);
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    await user.click(screen.getByRole("button", { name: "Export" }));
    expect(await screen.findByRole("dialog", { name: "Export statistics" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
  });

  it("hides the Range control when the server says stats are off", async () => {
    setup(() => offStats);
    go();
    expect(await screen.findByText(/Statistics are off/)).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("radio", { name: "Week" })).toBeNull());
  });

  it("warns and offers a retry when a background refresh fails over earlier numbers", async () => {
    const user = userEvent.setup();
    let fail = false;
    const m = setup(() => (fail ? json({ error: "boom" }, 500) : richStats));
    const qc = makeQueryClient({ retry: false });
    window.history.replaceState({ idx: 0 }, "", "/stats");
    render(<App client={qc} />);
    await screen.findByRole("heading", { name: "Daily activity" });
    fail = true;
    await qc.invalidateQueries({ queryKey: ["stats"] });
    expect(await screen.findByText(/Couldn't refresh; showing earlier numbers/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Daily activity" })).toBeInTheDocument();
    const before = statsCalls(m).length;
    fail = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(statsCalls(m).length).toBeGreaterThan(before));
    await waitFor(() => expect(screen.queryByText(/Couldn't refresh/)).toBeNull());
  });

  it("orders heatmap rows by the week start", async () => {
    setup(() => ({ ...richStats, week_start: "monday" }));
    go();
    await screen.findByRole("heading", { name: "When you read" });
    const table = screen.getByRole("table", { name: /by weekday and hour/ });
    const heads = within(table)
      .getAllByRole("rowheader")
      .map((h) => h.textContent);
    expect(heads[0]).toMatch(/^Mon/);
    expect(heads[6]).toMatch(/^Sun/);
  });

  it("puts Sunday first by default", async () => {
    setup(() => richStats);
    go();
    await screen.findByRole("heading", { name: "When you read" });
    const table = screen.getByRole("table", { name: /by weekday and hour/ });
    expect(within(table).getAllByRole("rowheader")[0]?.textContent).toMatch(/^Sun/);
  });

  it("stacks sources on a phone, with every stat labelled", async () => {
    setup(() => richStats);
    go();
    await screen.findByRole("heading", { name: "Sources" });
    expect(screen.queryByRole("table", { name: /by items read/ })).toBeNull();
    const list = screen.getByRole("list", { name: /by items read/ });
    const first = within(list).getAllByRole("listitem")[0]!;
    expect(first).toHaveTextContent("Gamma Daily");
    expect(first).toHaveTextContent("Avg read -");
    const beta = within(list).getByText("Beta News").closest("li")!;
    expect(beta).toHaveTextContent("Avg read 5 min");
    expect(beta).toHaveTextContent("Quick bounce 10%");
    expect(beta).toHaveTextContent("Opened original 30%");
    expect(beta).toHaveTextContent("Stars 1");
  });

  it("toggles Items/Minutes and Feeds/Folders", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("640"), media: q, addEventListener() {}, removeEventListener() {} }));
    const user = userEvent.setup();
    setup(() => richStats);
    go();
    await screen.findByRole("heading", { name: "Sources" });
    const rowNames = () =>
      within(screen.getByRole("table", { name: /by (items read|reading time)/ }))
        .getAllByRole("rowheader")
        .map((r) => r.textContent ?? "");
    expect(rowNames()[0]).toContain("Gamma Daily"); // 44 items
    await user.click(screen.getByRole("radio", { name: "Minutes" }));
    expect(rowNames()[0]).toContain("Beta News"); // 50 min
    await user.click(screen.getByRole("radio", { name: "Folders" }));
    const folders = rowNames();
    expect(folders[0]).toContain("Tech"); // 80 min rolled up
    expect(folders[0]).toContain("(2 feeds)");
    expect(within(screen.getByRole("table", { name: /Folders by reading time/ })).getAllByRole("row")[1]).toHaveTextContent("1h 20m");
  });

  it("says statistics are off and hides the nav entry", async () => {
    const m = setup(() => offStats, { "stats.enabled": false });
    go();
    expect(await screen.findByText(/Statistics are off/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Settings > Statistics/ })).toBeInTheDocument();
    expect(statsCalls(m)).toHaveLength(0);
    expect(within(screen.getByRole("navigation", { name: "Primary" })).queryByRole("link", { name: /Stats/ })).toBeNull();
  });

  it("shows the nav entry when stats are on", async () => {
    setup(() => richStats);
    go("/l/unread");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(within(nav).getByRole("link", { name: /Stats/ })).toHaveAttribute("href", "/stats");
  });

  it("offers a retry when the request fails", async () => {
    let fail = true;
    const m = setup(() => (fail ? json({ error: "boom" }, 500) : richStats));
    const user = userEvent.setup();
    go();
    await user.click(await screen.findByRole("button", { name: "Try again" }));
    await waitFor(() => expect(statsCalls(m).length).toBe(2));
    fail = false;
    await user.click(await screen.findByRole("button", { name: "Try again" }));
    await waitFor(() => expect(statsCalls(m).length).toBe(3));
    expect(await screen.findByRole("heading", { name: "Daily activity" })).toBeInTheDocument();
  });

  it("tiles show plain numbers; a tap reveals the previous period and a second tap hides it", async () => {
    const prev: StatsSummary = { ...richStats, totals: { items_read: 70, opens: 90, active_seconds: 4000, days_active: 12 } };
    const m = mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: {} }),
      "GET /api/stats/summary": (u) => json(u.searchParams.get("from") ? (isCurrent(u) ? richStats : prev) : { ...richStats, covered_from: "2026-01-01" }),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    const user = userEvent.setup();
    go();
    const summary = await screen.findByRole("region", { name: "Summary" });
    expect(within(summary).queryByText(/from 70/)).toBeNull();
    expect(statsCalls(m).some((c) => c.url.searchParams.get("from"))).toBe(false); // nothing fetched until asked
    await user.click(within(summary).getByRole("button", { name: /Items read/ }));
    expect(await within(summary).findByText("+20% from 70")).toBeInTheDocument();
    expect(within(summary).getByText("Complete days only, so today is left out of both: compared with the previous 29 days.")).toBeInTheDocument();
    // Both sides are complete days: the range without today, and the 29 days before it.
    const spans = statsCalls(m).filter((c) => c.url.searchParams.get("from")).map((c) => [c.url.searchParams.get("from"), c.url.searchParams.get("to")]);
    expect(spans).toContainEqual(["2026-08-28", "2026-09-25"]);
    expect(spans).toContainEqual(["2026-07-30", "2026-08-27"]);
    await user.click(within(summary).getByRole("button", { name: /Items read/ }));
    expect(within(summary).queryByText(/from 70/)).toBeNull();
  });

  it("Months fetches everything and draws one bar per month", async () => {
    const m = setup(() => richStats);
    const user = userEvent.setup();
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    await user.click(screen.getByRole("radio", { name: "Months" }));
    await screen.findByRole("heading", { name: "Monthly activity" });
    expect(statsCalls(m).at(-1)?.url.searchParams.get("range")).toBe("all");
    expect(screen.getByRole("img", { name: /Items read per month/ })).toBeInTheDocument();
  });

  const prevTotals = { items_read: 70, opens: 90, active_seconds: 4000, days_active: 12 };
  /** A span request for the range itself less today, not the earlier span. */
  const isCurrent = (u: URL) => u.searchParams.get("from") === "2026-08-28";

  it("says so, with a retry, when the earlier period cannot be loaded", async () => {
    let fail = true;
    setupSpan((u) => (u.searchParams.get("from") ? (fail ? json({ error: "boom" }, 500) : isCurrent(u) ? richStats : { ...richStats, totals: prevTotals }) : { ...richStats, covered_from: "2026-01-01" }));
    const user = userEvent.setup();
    go();
    const summary = await screen.findByRole("region", { name: "Summary" });
    await user.click(within(summary).getByRole("button", { name: /Items read/ }));
    expect(await within(summary).findByText("Couldn't load the earlier period.")).toBeInTheDocument();
    expect(within(summary).queryByText("Loading")).toBeNull();
    fail = false;
    await user.click(within(summary).getByRole("button", { name: "Try again" }));
    expect(await within(summary).findByText("+20% from 70")).toBeInTheDocument();
  });

  it("makes no comparison for a period that starts before recording was complete", async () => {
    // covered_from is after the earlier span (statistics were off, or the range was deleted): its zeros are gaps.
    const m = setupSpan((u) => (u.searchParams.get("from") ? { ...richStats, totals: { ...prevTotals, items_read: 0 } } : { ...richStats, covered_from: "2026-09-10" }));
    const user = userEvent.setup();
    go();
    const summary = await screen.findByRole("region", { name: "Summary" });
    await user.click(within(summary).getByRole("button", { name: /Items read/ }));
    expect(await within(summary).findByText("Not enough history")).toBeInTheDocument();
    expect(within(summary).queryByText(/from 0/)).toBeNull();
    expect(within(summary).queryByText(/Complete days only/)).toBeNull(); // nothing is compared, so nothing to explain
    expect(within(summary).getByRole("button", { name: /Items read/ })).not.toHaveAttribute("aria-describedby"); // no dangling reference
    expect(statsCalls(m).some((c) => c.url.searchParams.get("from"))).toBe(false); // nothing to fetch for it
  });

  it("bounds the active time comparison by when time was first recorded", async () => {
    setupSpan((u) =>
      u.searchParams.get("from") ? (isCurrent(u) ? richStats : { ...richStats, totals: prevTotals }) : { ...richStats, covered_from: "2026-01-01", timed_from: "2026-09-10" },
    );
    const user = userEvent.setup();
    go();
    const summary = await screen.findByRole("region", { name: "Summary" });
    await user.click(within(summary).getByRole("button", { name: /Active time/ }));
    expect(await within(summary).findByText("Not enough history")).toBeInTheDocument();
    await user.click(within(summary).getByRole("button", { name: /Items read/ }));
    expect(await within(summary).findByText("+20% from 70")).toBeInTheDocument();
  });

  it("ties the comparison line to the tile, and a week compares with the same days last week", async () => {
    setupSpan((u) =>
      u.searchParams.get("from")
        ? { ...richStats, totals: prevTotals }
        : { ...richStats, range: { key: "week", from: "2026-09-20", to: "2026-09-26", days: 7 } },
    );
    const user = userEvent.setup();
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    await user.click(screen.getByRole("radio", { name: "Week" }));
    const summary = await screen.findByRole("region", { name: "Summary" });
    const tile = within(summary).getByRole("button", { name: /Items read/ });
    await user.click(tile);
    const line = await within(summary).findByText("Complete days only, so today is left out of both: compared with the same days last week.");
    expect(tile.getAttribute("aria-describedby")).toBe(line.id);
  });

  it("All and Months have no tile to tap", async () => {
    setupSpan((u) => ({ ...richStats, range: { ...richStats.range!, key: (u.searchParams.get("range") ?? "month") as StatsRange } }));
    const user = userEvent.setup();
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    for (const name of ["All", "Months"]) {
      await user.click(screen.getByRole("radio", { name }));
      await waitFor(() => expect(within(screen.getByRole("region", { name: "Summary" })).queryByRole("button", { name: /Items read/ })).toBeNull());
    }
  });
});
