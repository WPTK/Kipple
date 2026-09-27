import type { StatsSummary } from "@/api/types";

function days(n: number, from = "2026-08-28"): string[] {
  const [y, m, d] = from.split("-").map(Number) as [number, number, number];
  return Array.from({ length: n }, (_, i) => {
    const t = new Date(Date.UTC(y, m - 1, d + i));
    return t.toISOString().slice(0, 10);
  });
}

/** A month of reading: 30 days, 12 of them active. */
export const richStats: StatsSummary = {
  enabled: true,
  tz: "America/New_York",
  week_start: "sunday",
  range: { key: "month", from: "2026-08-28", to: "2026-09-26", days: 30 },
  first_event_date: "2026-08-01",
  totals: { items_read: 84, opens: 120, active_seconds: 4800, days_active: 12 },
  daily: days(30).map((date, i) => ({ date, items_read: i % 5 === 0 ? 0 : (i % 7) + 1, active_seconds: i % 5 === 0 ? 0 : 120 * ((i % 7) + 1) })),
  streaks: { current: 3, longest: 9, longest_end: "2026-09-10" },
  heatmap: [
    { weekday: 2, hour: 20, active_seconds: 720, opens: 9 },
    { weekday: 2, hour: 8, active_seconds: 300, opens: 4 },
    { weekday: 0, hour: 9, active_seconds: 90, opens: 2 },
    { weekday: 1, hour: 7, active_seconds: 30, opens: 1 },
  ],
  behavior: {
    busiest_weekday: { weekday: 2, active_seconds: 1020, opens: 13 },
    busiest_hour: { hour: 20, active_seconds: 720, opens: 9 },
    avg_read_seconds: 150,
    longest_read: { item_id: "77", title: "A very long essay", feed_title: "Long Reads", seconds: 1320, date: "2026-09-03" },
  },
  sources: [
    { feed_id: "1", feed_title: "Alpha Blog", folder_id: "10", folder_name: "Tech", items_read: 30, opens: 40, active_seconds: 1800, avg_read_seconds: 60, bounce_rate: 0.5, open_original_rate: 0.1, tracked_opens: 40, bounces: 20, items_opened: 30, items_original: 3, stars: 2, subscribed: true },
    { feed_id: "2", feed_title: "Beta News", folder_id: "10", folder_name: "Tech", items_read: 10, opens: 60, active_seconds: 3000, avg_read_seconds: 300, bounce_rate: 0.1, open_original_rate: 0.3, tracked_opens: 60, bounces: 6, items_opened: 10, items_original: 3, stars: 1, subscribed: true },
    { feed_id: "3", feed_title: "Gamma Daily", folder_id: "11", folder_name: "News", items_read: 44, opens: 20, active_seconds: 0, avg_read_seconds: null, bounce_rate: null, open_original_rate: null, tracked_opens: 0, bounces: 0, items_opened: 20, items_original: 0, stars: 5, subscribed: true },
  ],
  never_opened: [{ feed_id: "9", title: "Quiet Feed", folder_name: "Misc", subscribed_on: "2026-08-15" }],
};

export const emptyStats: StatsSummary = {
  enabled: true,
  tz: "America/New_York",
  week_start: "sunday",
  range: { key: "month", from: "2026-08-28", to: "2026-09-26", days: 30 },
  first_event_date: null,
  totals: { items_read: 0, opens: 0, active_seconds: 0, days_active: 0 },
  daily: days(30).map((date) => ({ date, items_read: 0, active_seconds: 0 })),
  streaks: { current: 0, longest: 0, longest_end: null },
  heatmap: [],
  behavior: { busiest_weekday: null, busiest_hour: null, avg_read_seconds: null, longest_read: null },
  sources: [],
  never_opened: [],
};

export const offStats: StatsSummary = { enabled: false, tz: "America/New_York", week_start: "sunday" };
