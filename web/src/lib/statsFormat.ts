import { feedName } from "@/lib/feedStatus";
import type { ReadRate, StatsRange, StatsSource, WeekStart } from "@/api/types";

/** What makes an open a read (design §8, `store.statsIsRead`); the thresholds live on the server. */
export const READ_RULE =
  "An article counts as read after 10 seconds of reading, or 3 seconds once you've scrolled a quarter of the way down.";

/** What a figure shows when there is nothing to compute it from (never a dash, never 0). */
export const NO_DATA = "Not enough data yet";

/**
 * The one duration format of Stats and Your year: "45 s" under two minutes, "12 min" up to an hour, then "1h 20m" or
 * "2h". 0 seconds is "0 min".
 */
export function durationLabel(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds)) return NO_DATA;
  if (seconds <= 0) return "0 min";
  if (seconds < 120) return `${Math.max(1, Math.round(seconds))} s`;
  const m = Math.round(seconds / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  const r = m % 60;
  return r ? `${h}h ${r}m` : `${h}h`;
}

/** A 0..1 rate as a whole-number percentage; "Not enough data yet" when unknown. */
export function pctLabel(rate: number | null | undefined): string {
  return rate == null || !Number.isFinite(rate) ? NO_DATA : `${Math.round(rate * 100)}%`;
}

/**
 * The counts behind a read rate and its window of complete days (the summary's `read_rate_from`..`read_rate_to`),
 * shown on a tap: "31 of 50 new from Sep 2 to Sep 23", or "on Sep 23" for one day. No window is not enough history,
 * not 0%.
 */
export function readRateNote(r: ReadRate | undefined, from: string | null | undefined, to: string | null | undefined): string {
  if (!r || !from || !to) return "Not enough history";
  const days = from === to ? `on ${shortDate(from)}` : `from ${shortDate(from)} to ${shortDate(to)}`;
  if (r.new_items === 0) return `No new articles ${days}`;
  return `${r.items_read} of ${r.new_items} new ${days}`;
}

/** "YYYY-MM-DD" as a local date (never through UTC, which would shift the day). */
export function parseDay(s: string): Date {
  const [y = 1970, m = 1, d = 1] = s.split("-").map(Number);
  return new Date(y, m - 1, d);
}

export function shortDate(s: string): string {
  return parseDay(s).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function dateWithYear(s: string): string {
  return parseDay(s).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
}

/** Weekday name for 0 = Sunday, in the reader's locale. */
export function weekdayName(weekday: number, style: "long" | "short" = "long"): string {
  // 2024-01-07 was a Sunday.
  return new Date(2024, 0, 7 + weekday).toLocaleDateString(undefined, { weekday: style });
}

export function hourLabel(hour: number): string {
  return `${hour % 12 || 12} ${hour < 12 ? "am" : "pm"}`;
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** Weekdays (0 = Sunday) in display order. */
export function weekOrder(weekStart: WeekStart): number[] {
  const first = weekStart === "monday" ? 1 : 0;
  return Array.from({ length: 7 }, (_, i) => (first + i) % 7);
}

export const RANGES: readonly { value: StatsRange; label: string }[] = [
  { value: "week", label: "Week" },
  { value: "month", label: "Month" },
  { value: "year", label: "Year" },
  { value: "all", label: "All" },
];

const RANGE_KEY = "kipple.stats.range";

/** The Stats screen's ranges: the four the server knows, plus Monthly, which is "all" shown as one bar per month. */
export type ScreenRange = StatsRange | "months";

export const SCREEN_RANGES: readonly { value: ScreenRange; label: string }[] = [
  { value: "week", label: "Week" },
  { value: "month", label: "Month" },
  { value: "year", label: "Year" },
  { value: "months", label: "Monthly" },
  { value: "all", label: "All" },
];

/** The server range a screen range is fetched with. */
export function apiRange(r: ScreenRange): StatsRange {
  return r === "months" ? "all" : r;
}

export function loadScreenRange(): ScreenRange {
  try {
    const v = localStorage.getItem(RANGE_KEY);
    if (v === "week" || v === "month" || v === "year" || v === "months" || v === "all") return v;
  } catch {
    /* not remembered */
  }
  return "month";
}

/** The remembered range as the server names it, for the export's default. */
export function loadRange(): StatsRange {
  return apiRange(loadScreenRange());
}

export function saveRange(r: ScreenRange): void {
  try {
    localStorage.setItem(RANGE_KEY, r);
  } catch {
    /* not remembered */
  }
}

export interface SourceRow {
  key: string;
  name: string;
  items_read: number;
  opens: number;
  active_seconds: number;
  avg_read_seconds: number | null;
  bounce_rate: number | null;
  open_original_rate: number | null;
  stars: number;
  subscribed: boolean;
  /** Feeds rolled into this row (1 for a feed row). */
  count: number;
  /**
   * A feed's read rate; null for a folder, since Sources lists only the feeds with activity and a folder's quiet
   * feeds would be missing from its new items.
   */
  read_rate: number | null;
}

export function feedRows(sources: StatsSource[]): SourceRow[] {
  return sources.map((s) => ({
    key: s.feed_id,
    name: feedName({ title: s.feed_title }), // a row, its button and its sheet always have a name
    items_read: s.items_read,
    opens: s.opens,
    active_seconds: s.active_seconds,
    avg_read_seconds: s.avg_read_seconds,
    bounce_rate: s.bounce_rate,
    open_original_rate: s.open_original_rate,
    stars: s.stars,
    subscribed: s.subscribed,
    count: 1,
    read_rate: s.read_rate?.rate ?? null,
  }));
}

const sum = (list: StatsSource[], f: (s: StatsSource) => number) => list.reduce((a, s) => a + f(s), 0);
const ratio = (n: number, d: number): number | null => (d > 0 ? n / d : null);

/**
 * Feeds rolled up by folder: counts add up; exact from the raw counts: bounces over tracked opens, items opened
 * on the original over items opened, and the average read as timed seconds over timed items (the server's own
 * definition; legacy items with no recorded time do not dilute it).
 */
export function folderRows(sources: StatsSource[]): SourceRow[] {
  const groups = new Map<string, StatsSource[]>();
  for (const s of sources) {
    const k = s.folder_id ?? (s.folder_name ? `n:${s.folder_name}` : "none");
    groups.set(k, [...(groups.get(k) ?? []), s]);
  }
  return [...groups.entries()].map(([key, list]) => ({
    key,
    name: list[0]?.folder_name ?? "Uncategorized",
    items_read: list.reduce((a, s) => a + s.items_read, 0),
    opens: list.reduce((a, s) => a + s.opens, 0),
    active_seconds: list.reduce((a, s) => a + s.active_seconds, 0),
    avg_read_seconds: ratio(sum(list, (s) => s.timed_seconds), sum(list, (s) => s.timed_items)),
    bounce_rate: ratio(sum(list, (s) => s.bounces ?? 0), sum(list, (s) => s.tracked_opens ?? 0)),
    open_original_rate: ratio(sum(list, (s) => s.items_original ?? 0), sum(list, (s) => s.items_opened ?? 0)),
    stars: list.reduce((a, s) => a + s.stars, 0),
    subscribed: list.some((s) => s.subscribed),
    count: list.length,
    read_rate: null,
  }));
}

export type SourceMetric = "items" | "minutes";

export function sortRows(rows: SourceRow[], metric: SourceMetric): SourceRow[] {
  const val = (r: SourceRow) => (metric === "items" ? r.items_read : r.active_seconds);
  return [...rows].sort((a, b) => val(b) - val(a) || b.opens - a.opens || a.name.localeCompare(b.name));
}

/** Rows with at least one star, most first (ties by name). */
export function mostStarred(rows: SourceRow[], n = 3): SourceRow[] {
  return rows
    .filter((r) => r.stars > 0)
    .sort((a, b) => b.stars - a.stars || a.name.localeCompare(b.name))
    .slice(0, n);
}

/**
 * 0 (empty) to 4 (most): the step of the heatmap ramp for a value, by its rank among the non-zero values (`positives`,
 * sorted ascending) rather than its share of the maximum, so one very busy hour cannot flatten every other cell into the
 * lightest shade. Equal values share a level; any non-zero value is at least 1.
 */
export function heatLevel(value: number, positives: readonly number[]): 0 | 1 | 2 | 3 | 4 {
  if (value <= 0 || positives.length === 0) return 0;
  let below = 0;
  while (below < positives.length && positives[below]! < value) below++;
  return (1 + Math.min(3, Math.floor((4 * below) / positives.length))) as 1 | 2 | 3 | 4;
}

/** Whole days from `from` to `to` (both "YYYY-MM-DD"), DST-proof. */
export function daysBetween(from: string, to: string): number {
  const u = (s: string) => {
    const [y = 1970, m = 1, d = 1] = s.split("-").map(Number);
    return Date.UTC(y, m - 1, d);
  };
  return Math.round((u(to) - u(from)) / 86_400_000);
}

/** Today as "YYYY-MM-DD" in local time. */
export function todayString(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export interface MonthBar {
  /** "YYYY-MM". */
  month: string;
  items_read: number;
  active_seconds: number;
}

/** Daily rows folded into one row per calendar month, every month from the first through the last, empty ones included. */
export function monthlyBars(daily: { date: string; items_read: number; active_seconds: number }[]): MonthBar[] {
  if (daily.length === 0) return [];
  const by = new Map<string, MonthBar>();
  for (const d of daily) {
    const month = d.date.slice(0, 7);
    const m = by.get(month) ?? { month, items_read: 0, active_seconds: 0 };
    m.items_read += d.items_read;
    m.active_seconds += d.active_seconds;
    by.set(month, m);
  }
  const first = daily[0]!.date.slice(0, 7);
  const last = daily[daily.length - 1]!.date.slice(0, 7);
  const out: MonthBar[] = [];
  let [y = 1970, mo = 1] = first.split("-").map(Number);
  for (;;) {
    const key = `${y}-${String(mo).padStart(2, "0")}`;
    out.push(by.get(key) ?? { month: key, items_read: 0, active_seconds: 0 });
    if (key >= last) break;
    if (++mo > 12) {
      mo = 1;
      y++;
    }
  }
  return out;
}

export interface ActivityRow {
  /** First day of the bar ("YYYY-MM-DD"). */
  date: string;
  /** Last day of the bar; equal to `date` for a single day. */
  end: string;
  items_read: number;
  active_seconds: number;
}

/** More bars than this in the chart width is under a pixel each, so the days are folded or the empty lead is cut. */
const MAX_DAY_BARS = 120;

/**
 * The rows the activity chart draws. A short range is one bar per day. A long one starts at the first day with
 * reading, since a stretch of empty days before it only shrinks every bar; if that still leaves too many days, they
 * are folded into calendar weeks starting on the reader's week start, so a bar keeps its days from one visit to the
 * next (the first and last can be partial).
 */
export function activityBars(
  daily: { date: string; items_read: number; active_seconds: number }[],
  weekStart: WeekStart = "sunday",
): { unit: "day" | "week"; rows: ActivityRow[] } {
  const days = daily.map((d) => ({ ...d, end: d.date }));
  if (days.length <= MAX_DAY_BARS) return { unit: "day", rows: days };
  const first = days.findIndex((d) => d.items_read > 0 || d.active_seconds > 0);
  const live = first > 0 ? days.slice(first) : days;
  if (live.length <= MAX_DAY_BARS) return { unit: "day", rows: live };
  const startDay = weekStart === "monday" ? 1 : 0;
  const rows: ActivityRow[] = [];
  for (const d of live) {
    const cur = rows[rows.length - 1];
    if (cur && parseDay(d.date).getDay() !== startDay) {
      cur.end = d.date;
      cur.items_read += d.items_read;
      cur.active_seconds += d.active_seconds;
    } else {
      rows.push({ date: d.date, end: d.date, items_read: d.items_read, active_seconds: d.active_seconds });
    }
  }
  return { unit: "week", rows };
}

export function monthName(month: string, style: "short" | "long" = "short"): string {
  const [y = 1970, m = 1] = month.split("-").map(Number);
  return new Date(y, m - 1, 1).toLocaleDateString(undefined, { month: style, year: "numeric" });
}

/** A local date string moved by whole days (the calendar's days, so DST and month lengths cannot skew it). */
export function addDays(day: string, n: number): string {
  const d = parseDay(day);
  d.setDate(d.getDate() + n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/**
 * What a summary's range is compared with. Only complete days count, so the range's last day (today, for every range
 * the screen offers) is left out of both sides: `current` is the range without it, and the earlier span has the same
 * length. A week so far (at most six complete days) is set against the same days of the week before; month and year (the last 30 and 365 days)
 * against the stretch just before. "all" has nothing before it, nor does a range that is only today.
 */
export function previousPeriod(range: { key: StatsRange; from: string; to: string }): {
  current: { from: string; to: string };
  from: string;
  to: string;
  label: string;
} | null {
  if (range.key === "all") return null;
  const to = addDays(range.to, -1);
  const days = daysBetween(range.from, to) + 1;
  if (days < 1) return null;
  const current = { from: range.from, to };
  if (range.key === "week") {
    const from = addDays(range.from, -7);
    return { current, from, to: addDays(from, days - 1), label: "the same days last week" };
  }
  return { current, from: addDays(range.from, -days), to: addDays(range.from, -1), label: `the previous ${days} days` };
}

/** "+18% from 120", "-5% from 40", "no change from 7", "up from 0" or "no change from 0" when the earlier value was zero. */
export function changeLabel(now: number, before: number, show: (n: number) => string): string {
  if (before <= 0) return `${now > 0 ? "up" : "no change"} from ${show(before)}`;
  const pct = Math.round(((now - before) / before) * 100);
  if (pct === 0) return `no change from ${show(before)}`;
  return `${pct > 0 ? "+" : "-"}${Math.abs(pct)}% from ${show(before)}`;
}
