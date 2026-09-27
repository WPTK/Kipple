import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { StatsSummary } from "@/api/types";
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
    expect(screen.getByLabelText("Tuesday 8 pm: 12 min")).toBeInTheDocument();
    expect(screen.getByText(/Busiest day: Tuesday/)).toBeInTheDocument();
    expect(screen.getByText(/Busiest hour: 8 pm/)).toBeInTheDocument();
    expect(screen.getByText(/Average read length: 3 min/)).toBeInTheDocument();
    expect(screen.getByText(/Longest read: A very long essay, from Long Reads, 22 min/)).toBeInTheDocument();
    expect(screen.getByText("Most starred: Gamma Daily (5), Alpha Blog (2), Beta News (1).")).toBeInTheDocument();
    expect(screen.getByText("Quiet Feed")).toBeInTheDocument();
    expect(screen.getByText(/subscribed on Aug 15, 2026/)).toBeInTheDocument();
    expect(screen.queryByText(/Only \d+ days? of reading/)).toBeNull(); // 12 active days
  });

  it("shows friendly empty states, not blank charts", async () => {
    setup(() => emptyStats);
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(screen.getByText(/No reading in this range yet/)).toBeInTheDocument();
    expect(screen.getByText(/heatmap fills in/)).toBeInTheDocument();
    expect(screen.getByText(/Observations show up/)).toBeInTheDocument();
    expect(screen.getByText(/Sources are listed here/)).toBeInTheDocument();
    expect(screen.getByText(/No streak yet/)).toBeInTheDocument();
    expect(screen.getByText(/Every subscribed feed has had an article opened/)).toBeInTheDocument();
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

  it("notes a low-data range", async () => {
    setup(() => ({ ...richStats, totals: { ...richStats.totals!, days_active: 3 } }));
    go();
    expect(await screen.findByText("Only 3 days of reading so far; charts fill in as you read.")).toBeInTheDocument();
  });

  it("does not note low data for All", async () => {
    localStorage.setItem("kipple.stats.range", "all");
    setup(() => ({ ...richStats, totals: { ...richStats.totals!, days_active: 3 } }));
    go();
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(screen.queryByText(/Only 3 days/)).toBeNull();
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
    setup(() => json({ error: "boom" }, 500));
    go();
    expect(await screen.findByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
