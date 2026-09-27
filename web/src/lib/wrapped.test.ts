import { describe, expect, it } from "vitest";
import type { StatsSummary } from "@/api/types";
import { buildWrappedCard, deriveWrapped, isLowData, longestStreakIn, wrappedText, yearRange, yearSpan, TOP_SOURCES, type WrappedModel } from "./wrapped";

const base: StatsSummary = { enabled: true, tz: "UTC", week_start: "sunday" };
const day = (date: string, items_read: number) => ({ date, items_read, active_seconds: items_read * 60 });
const src = (n: number, items: number) => ({
  feed_id: String(n), feed_title: `Feed ${n}`, folder_id: null, folder_name: null, items_read: items, opens: items, active_seconds: 0, timed_seconds: 0,
  timed_items: 0, avg_read_seconds: null, bounce_rate: null, open_original_rate: null, tracked_opens: 0, bounces: 0, items_opened: 0, items_original: 0, stars: 0, subscribed: true,
});

describe("wrapped model", () => {
  it("finds the longest streak inside the year across gaps", () => {
    const daily = [day("2026-01-01", 1), day("2026-01-02", 2), day("2026-01-03", 0), day("2026-02-01", 1), day("2026-02-02", 1), day("2026-02-03", 1), day("2026-02-05", 4)];
    expect(longestStreakIn(daily)).toBe(3);
    expect(longestStreakIn([])).toBe(0);
    expect(longestStreakIn([day("2026-12-30", 1), day("2026-12-31", 1)])).toBe(2);
  });

  it("picks the busiest month, earlier on a tie", () => {
    const m = deriveWrapped({ ...base, daily: [day("2026-03-05", 5), day("2026-10-05", 3), day("2026-10-06", 2), day("2026-04-01", 4)] }, 2026, "2026-12-31");
    expect(m.busiestMonth).toBe(2); // March 5 ties October 5; March is earlier
    expect(deriveWrapped({ ...base, daily: [] }, 2026).busiestMonth).toBeNull();
  });

  it("picks busiest weekday and hour from the heatmap, summing across cells", () => {
    const heatmap = [
      { weekday: 0, hour: 7, active_seconds: 100, opens: 1 },
      { weekday: 0, hour: 19, active_seconds: 100, opens: 1 },
      { weekday: 3, hour: 19, active_seconds: 150, opens: 1 },
    ];
    const m = deriveWrapped({ ...base, heatmap }, 2026);
    expect(m.busiestWeekday).toBe(0);
    expect(m.busiestHour).toBe(19);
  });

  it("falls back to opens when nothing is timed, and to behavior when there is no heatmap", () => {
    expect(deriveWrapped({ ...base, heatmap: [{ weekday: 2, hour: 5, active_seconds: 0, opens: 3 }] }, 2026).busiestHour).toBe(5);
    const m = deriveWrapped({ ...base, behavior: { busiest_weekday: { weekday: 4, active_seconds: 1, opens: 1 }, busiest_hour: { hour: 9, active_seconds: 1, opens: 1 }, avg_read_seconds: 90, longest_read: null } }, 2026);
    expect([m.busiestWeekday, m.busiestHour, m.avgReadSeconds]).toEqual([4, 9, 90]);
  });

  it("caps top sources at five, most items first, dropping unread feeds", () => {
    const sources = [1, 2, 3, 4, 5, 6, 7].map((n) => src(n, n)).concat(src(8, 0));
    const m = deriveWrapped({ ...base, sources }, 2026);
    expect(m.topSources).toHaveLength(TOP_SOURCES);
    expect(m.topSources.map((s) => s.items)).toEqual([7, 6, 5, 4, 3]);
  });

  it("handles empty data and a young history", () => {
    const m = deriveWrapped({ ...base, totals: { items_read: 0, opens: 0, active_seconds: 0, days_active: 0 }, daily: [], first_event_date: null }, 2026, "2026-09-26");
    expect(m.empty).toBe(true);
    expect([m.longestStreak, m.busiestWeekday, m.busiestMonth, m.longestRead]).toEqual([0, null, null, null]);
    expect(deriveWrapped({ ...base, first_event_date: "2026-09-22" }, 2026, "2026-09-26").historyDays).toBe(5);
    expect(isLowData(deriveWrapped({ ...base, first_event_date: "2026-09-22" }, 2026, "2026-09-26"))).toBe(true);
    expect(isLowData(deriveWrapped({ ...base, first_event_date: "2025-01-01" }, 2026, "2026-09-26"))).toBe(false);
  });
});

describe("years", () => {
  it("lists years from the first event to this year, newest first", () => {
    expect(yearRange("2024-05-01", "2026-09-26")).toEqual([2026, 2025, 2024]);
    expect(yearRange(null, "2026-09-26")).toEqual([2026]);
    expect(yearRange("2030-01-01", "2026-09-26")).toEqual([2026]);
  });
  it("spans the whole past year and the current one up to today", () => {
    expect(yearSpan(2025, "2026-09-26")).toEqual({ from: "2025-01-01", to: "2025-12-31" });
    expect(yearSpan(2026, "2026-09-26")).toEqual({ from: "2026-01-01", to: "2026-09-26" });
  });
});

const model: WrappedModel = {
  year: 2026, itemsRead: 1234, activeSeconds: 7200 * 5, daysActive: 200, longestStreak: 31, busiestWeekday: 0, busiestHour: 19, busiestMonth: 9,
  avgReadSeconds: 150, topSources: [{ name: "Secret Feed", items: 40 }, { name: "Other Blog", items: 9 }], longestRead: { title: "Private Title", feed: "Secret Feed", seconds: 1320 }, historyDays: 200, empty: false,
};
const texts = (ops: ReturnType<typeof buildWrappedCard>) => ops.flatMap((o) => (o.kind === "text" ? [o.text] : [])).join("\n");

describe("share card and text", () => {
  it("holds aggregates only by default", () => {
    const all = texts(buildWrappedCard(model, { topSources: false, longestRead: false }));
    expect(all).toContain("2026");
    expect(all).toContain("1234");
    expect(all).toContain("Sunday");
    expect(all).toContain("7 pm");
    expect(all).toContain("October");
    expect(all).toContain("31");
    expect(all).not.toContain("Secret Feed");
    expect(all).not.toContain("Private Title");
    const t = wrappedText(model, { topSources: false, longestRead: false });
    expect(t).not.toContain("Secret Feed");
    expect(t).not.toContain("Private Title");
  });
  it("adds feed names and the title only when toggled on", () => {
    expect(texts(buildWrappedCard(model, { topSources: true, longestRead: false }))).toContain("Secret Feed");
    expect(texts(buildWrappedCard(model, { topSources: true, longestRead: false }))).not.toContain("Private Title");
    expect(texts(buildWrappedCard(model, { topSources: false, longestRead: true }))).toContain("Private Title");
    const t = wrappedText(model, { topSources: true, longestRead: true });
    expect(t).toContain("Other Blog (9)");
    expect(t).toContain("Private Title");
    expect(t.startsWith("My 2026 in Kipple")).toBe(true);
  });
  it("stays inside the 1080 by 1350 canvas with everything on", () => {
    for (const op of buildWrappedCard(model, { topSources: true, longestRead: true })) {
      expect(op.y).toBeLessThanOrEqual(1350);
      expect(op.x).toBeLessThan(1080);
    }
  });
});
