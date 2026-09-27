import { describe, expect, it } from "vitest";
import type { StatsSummary } from "@/api/types";
import {
  buildWrappedCard,
  deriveWrapped,
  hoursLabel,
  isLowData,
  longestStreakIn,
  summaryMatchesYear,
  wrappedLines,
  wrappedText,
  yearRange,
  yearSpan,
  TOP_SOURCES,
  type WrappedModel,
} from "./wrapped";

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

  it("trusts the server's busiest weekday/hour over a heatmap recompute, even on a tie the two would break differently", () => {
    // The heatmap alone ties weekday 0 and weekday 3 at 100 active seconds each; the client's own tie-break (earlier
    // key wins) would pick 0. The server (same as the main Stats screen) says the winner is 3 — trust that instead.
    const heatmap = [
      { weekday: 0, hour: 7, active_seconds: 100, opens: 1 },
      { weekday: 3, hour: 19, active_seconds: 100, opens: 1 },
    ];
    const behavior = { busiest_weekday: { weekday: 3, active_seconds: 100, opens: 1 }, busiest_hour: { hour: 19, active_seconds: 100, opens: 1 }, avg_read_seconds: null, longest_read: null };
    const m = deriveWrapped({ ...base, heatmap, behavior }, 2026);
    expect(m.busiestWeekday).toBe(3);
    expect(m.busiestHour).toBe(19);
    // Sanity: recomputing from the same heatmap directly (no behavior) really would have picked the other day.
    expect(deriveWrapped({ ...base, heatmap }, 2026).busiestWeekday).toBe(0);
  });

  it("trusts an explicit 'no winner' from behavior instead of falling back to the heatmap", () => {
    const heatmap = [{ weekday: 0, hour: 7, active_seconds: 100, opens: 1 }];
    const behavior = { busiest_weekday: null, busiest_hour: null, avg_read_seconds: null, longest_read: null };
    const m = deriveWrapped({ ...base, heatmap, behavior }, 2026);
    expect(m.busiestWeekday).toBeNull();
    expect(m.busiestHour).toBeNull();
  });

  it("caps top sources at five, most items first, dropping unread feeds, and carries the feed id", () => {
    const sources = [1, 2, 3, 4, 5, 6, 7].map((n) => src(n, n)).concat(src(8, 0));
    const m = deriveWrapped({ ...base, sources }, 2026);
    expect(m.topSources).toHaveLength(TOP_SOURCES);
    expect(m.topSources.map((s) => s.items)).toEqual([7, 6, 5, 4, 3]);
    expect(m.topSources.map((s) => s.feedId)).toEqual(["7", "6", "5", "4", "3"]);
  });

  it("keys two same-named feeds by their distinct feed ids, not the shared name", () => {
    const sources = [{ ...src(1, 5), feed_title: "Renamed Feed" }, { ...src(2, 3), feed_title: "Renamed Feed" }];
    const m = deriveWrapped({ ...base, sources }, 2026);
    expect(m.topSources.map((s) => ({ feedId: s.feedId, name: s.name }))).toEqual([
      { feedId: "1", name: "Renamed Feed" },
      { feedId: "2", name: "Renamed Feed" },
    ]);
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
  avgReadSeconds: 150, topSources: [{ feedId: "1", name: "Secret Feed", items: 40 }, { feedId: "2", name: "Other Blog", items: 9 }], longestRead: { title: "Private Title", feed: "Secret Feed", seconds: 1320 }, historyDays: 200, empty: false,
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

  it("hours label rounds to a singular hour, never '1 hours'", () => {
    expect(hoursLabel(3600)).toBe("1 hour"); // exactly 3600s
    expect(hoursLabel(3779)).toBe("1 hour"); // rounds to 1.0 but under the 1.05h rounding edge
    expect(hoursLabel(3800)).toBe("1.1 hours");
    expect(hoursLabel(2 * 3600)).toBe("2 hours");
    expect(hoursLabel(36000)).toBe("10 hours");
  });

  it("says active reading was not recorded rather than '0 min', for a legacy year with items but no timed seconds", () => {
    const legacy: WrappedModel = { ...model, itemsRead: 12, activeSeconds: 0 };
    const cardText = texts(buildWrappedCard(legacy, { topSources: false, longestRead: false }));
    expect(cardText).not.toContain("0 min");
    expect(cardText).not.toContain("0 h");
    const lines = wrappedLines(legacy, { topSources: false, longestRead: false });
    expect(lines).toContain("Active reading time not recorded.");
    expect(lines.join(" ")).not.toContain("0 min of active reading");
  });

  it("still states a true zero when nothing at all was read", () => {
    const nothing: WrappedModel = { ...model, itemsRead: 0, activeSeconds: 0, empty: true };
    expect(wrappedLines(nothing, { topSources: false, longestRead: false })).toContain("0 min of active reading.");
  });

  it("states the busiest hour in the text even when there is no busiest weekday, matching the card", () => {
    const hourOnly: WrappedModel = { ...model, busiestWeekday: null, busiestHour: 3 };
    const lines = wrappedLines(hourOnly, { topSources: false, longestRead: false });
    expect(lines.some((l) => l.includes("Busiest hour"))).toBe(true);
    const cardText = texts(buildWrappedCard(hourOnly, { topSources: false, longestRead: false }));
    expect(cardText).toContain("Busiest hour");
  });

  it("truncates long feed names and titles on a code-point boundary, never splitting an emoji's surrogate pair", () => {
    // Placed so a naive UTF-16 `.slice(0, n - 1)` would land inside the emoji's surrogate pair: n-2 plain
    // characters, then the emoji straddling the old cut point, then more text pushing it over the clip length.
    const sourceName = `${"A".repeat(28)}💜tail`; // clipped at 30 code points
    const title = `${"B".repeat(42)}💜tail`; // clipped at 44 code points
    const withEmoji: WrappedModel = {
      ...model,
      topSources: [{ feedId: "1", name: sourceName, items: 3 }],
      longestRead: { title, feed: "F", seconds: 60 },
    };
    const ops = buildWrappedCard(withEmoji, { topSources: true, longestRead: true });
    const hasLoneSurrogate = (s: string) => {
      for (let i = 0; i < s.length; i++) {
        const c = s.charCodeAt(i);
        if (c >= 0xd800 && c <= 0xdbff && !(s.charCodeAt(i + 1) >= 0xdc00 && s.charCodeAt(i + 1) <= 0xdfff)) return true;
        if (c >= 0xdc00 && c <= 0xdfff && !(s.charCodeAt(i - 1) >= 0xd800 && s.charCodeAt(i - 1) <= 0xdbff)) return true;
      }
      return false;
    };
    const textOps = ops.flatMap((op) => (op.kind === "text" ? [op.text] : []));
    for (const t of textOps) expect(hasLoneSurrogate(t)).toBe(false);
    const nameText = textOps.find((t) => t.includes("💜"));
    expect(nameText).toContain("…");
  });
});

describe("summaryMatchesYear", () => {
  it("is true only when the response's range starts with the selected year", () => {
    expect(summaryMatchesYear({ ...base, range: { key: "all", from: "2026-01-01", to: "2026-09-27", days: 270 } }, 2026)).toBe(true);
    expect(summaryMatchesYear({ ...base, range: { key: "all", from: "2025-01-01", to: "2025-12-31", days: 365 } }, 2026)).toBe(false);
    expect(summaryMatchesYear({ ...base }, 2026)).toBe(false); // no range at all
    expect(summaryMatchesYear(undefined, 2026)).toBe(false);
  });
});
