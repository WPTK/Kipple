import { useCallback, useSyncExternalStore } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { keys } from "@/api/queryKeys";
import type { Bootstrap, StatsSummary } from "@/api/types";
import { daysBetween, durationLabel, hourLabel, plural, todayString, weekdayName } from "@/lib/statsFormat";

/**
 * Wrapped: a yearly summary derived on the device from the ordinary stats summary. Nothing here talks to the network.
 * Facts only: no comparisons, goals, badges or advice.
 */

export type WrappedState = "on" | "off" | "unknown";

/** Unknown while the bootstrap answer is absent; a missing key counts as on (the setting's default). */
export function wrappedStateOf(qc: QueryClient | undefined): WrappedState {
  const b = qc?.getQueryData<Bootstrap>(keys.bootstrap);
  if (b === undefined) return "unknown";
  if (b.settings?.["stats.enabled"] === false) return "off";
  return b.settings?.["stats.wrapped_enabled"] === false ? "off" : "on";
}

/** Whether Wrapped shows (statistics on and Wrapped on). Reads the cached bootstrap only; absent means hidden. */
export function useWrappedEnabled(): boolean {
  const qc = useQueryClient();
  const subscribe = useCallback(
    (notify: () => void) =>
      qc.getQueryCache().subscribe((e) => {
        if (e.query.queryKey[0] === keys.bootstrap[0]) notify();
      }),
    [qc],
  );
  return useSyncExternalStore(subscribe, () => wrappedStateOf(qc) === "on");
}

// ---- Years ------------------------------------------------------------------------------------------------------

export const currentYear = (today = todayString()): number => Number(today.slice(0, 4));

/** Years from the first event's year to this year, newest first. Just this year when nothing is recorded yet. */
export function yearRange(firstEventDate: string | null | undefined, today = todayString()): number[] {
  const now = currentYear(today);
  const first = firstEventDate && /^\d{4}-/.test(firstEventDate) ? Math.min(now, Number(firstEventDate.slice(0, 4))) : now;
  return Array.from({ length: now - first + 1 }, (_, i) => now - i);
}

/** The summary's date span for a year: the whole year, or up to today for the current one. */
export function yearSpan(year: number, today = todayString()): { from: string; to: string } {
  const to = currentYear(today) === year ? today : `${year}-12-31`;
  return { from: `${year}-01-01`, to };
}

// ---- Model ------------------------------------------------------------------------------------------------------

export interface WrappedModel {
  year: number;
  itemsRead: number;
  activeSeconds: number;
  daysActive: number;
  longestStreak: number;
  /** 0 = Sunday. */
  busiestWeekday: number | null;
  busiestHour: number | null;
  /** 0 = January. */
  busiestMonth: number | null;
  avgReadSeconds: number | null;
  topSources: { name: string; items: number }[];
  longestRead: { title: string; feed: string; seconds: number } | null;
  /** Days from the first recorded reading (or January 1) to the end of the span. */
  historyDays: number;
  empty: boolean;
}

export const TOP_SOURCES = 5;

/** Longest run of consecutive days with something read. */
export function longestStreakIn(daily: { date: string; items_read: number }[]): number {
  const days = [...new Set(daily.filter((d) => d.items_read > 0).map((d) => d.date))].sort();
  let best = 0;
  let run = 0;
  let prev: string | null = null;
  for (const d of days) {
    run = prev && daysBetween(prev, d) === 1 ? run + 1 : 1;
    if (run > best) best = run;
    prev = d;
  }
  return best;
}

/** The key with the largest total; ties go to the earlier key; null when nothing is above zero. */
function argMax(totals: Map<number, number>): number | null {
  let best: number | null = null;
  let bestV = 0;
  for (const [k, v] of [...totals.entries()].sort((a, b) => a[0] - b[0])) {
    if (v > bestV) {
      best = k;
      bestV = v;
    }
  }
  return best;
}

export function deriveWrapped(data: StatsSummary, year: number, today = todayString()): WrappedModel {
  const daily = data.daily ?? [];
  const months = new Map<number, number>();
  for (const d of daily) {
    if (d.items_read > 0 && d.date.startsWith(`${year}-`)) {
      const m = Number(d.date.slice(5, 7)) - 1;
      months.set(m, (months.get(m) ?? 0) + d.items_read);
    }
  }
  const cells = data.heatmap ?? [];
  const useTime = cells.some((c) => c.active_seconds > 0);
  const by = (key: (c: (typeof cells)[number]) => number) => {
    const t = new Map<number, number>();
    for (const c of cells) t.set(key(c), (t.get(key(c)) ?? 0) + (useTime ? c.active_seconds : c.opens));
    return argMax(t);
  };
  const b = data.behavior;
  const weekday = cells.length ? by((c) => c.weekday) : (b?.busiest_weekday?.weekday ?? null);
  const hour = cells.length ? by((c) => c.hour) : (b?.busiest_hour?.hour ?? null);
  const t = data.totals;
  const span = yearSpan(year, today);
  const start = data.first_event_date && data.first_event_date > span.from ? data.first_event_date : span.from;
  const topSources = [...(data.sources ?? [])]
    .filter((s) => s.items_read > 0)
    .sort((a, c) => c.items_read - a.items_read || a.feed_title.localeCompare(c.feed_title))
    .slice(0, TOP_SOURCES)
    .map((s) => ({ name: s.feed_title, items: s.items_read }));
  const l = b?.longest_read;
  return {
    year,
    itemsRead: t?.items_read ?? 0,
    activeSeconds: t?.active_seconds ?? 0,
    daysActive: t?.days_active ?? 0,
    longestStreak: longestStreakIn(daily),
    busiestWeekday: weekday,
    busiestHour: hour,
    busiestMonth: argMax(months),
    avgReadSeconds: b?.avg_read_seconds ?? null,
    topSources,
    longestRead: l ? { title: l.title, feed: l.feed_title, seconds: l.seconds } : null,
    historyDays: Math.max(1, daysBetween(start, span.to) + 1),
    empty: !t || (t.items_read === 0 && t.opens === 0 && t.active_seconds === 0),
  };
}

/** True when less than a week of the year has any history to show. */
export const isLowData = (m: WrappedModel): boolean => m.historyDays < 7;

export const monthName = (month: number): string => new Date(2024, month, 1).toLocaleDateString(undefined, { month: "long" });

export function hoursLabel(seconds: number): string {
  const h = seconds / 3600;
  if (h < 1) return durationLabel(seconds);
  return h >= 10 ? `${Math.round(h)} hours` : `${Math.round(h * 10) / 10} hours`;
}

// ---- Share options, text and card -------------------------------------------------------------------------------

export interface WrappedOptions {
  topSources: boolean;
  longestRead: boolean;
}
export const DEFAULT_OPTIONS: WrappedOptions = { topSources: false, longestRead: false };

/** The facts as lines of plain text. Feed names and titles appear only when the options ask for them. */
export function wrappedLines(m: WrappedModel, o: WrappedOptions): string[] {
  const lines = [`I read ${plural(m.itemsRead, "item")} in ${m.year}.`, `${hoursLabel(m.activeSeconds)} of active reading.`];
  lines.push(`${plural(m.daysActive, "day")} with reading; longest streak ${plural(m.longestStreak, "day")}.`);
  if (m.busiestWeekday != null && m.busiestHour != null) lines.push(`Busiest day: ${weekdayName(m.busiestWeekday)}, busiest hour: ${hourLabel(m.busiestHour)}.`);
  else if (m.busiestWeekday != null) lines.push(`Busiest day: ${weekdayName(m.busiestWeekday)}.`);
  if (m.busiestMonth != null) lines.push(`Busiest month: ${monthName(m.busiestMonth)}.`);
  if (o.topSources && m.topSources.length) lines.push(`Top sources: ${m.topSources.map((s) => `${s.name} (${s.items})`).join(", ")}.`);
  if (o.longestRead && m.longestRead) lines.push(`Longest read: ${m.longestRead.title}, ${durationLabel(m.longestRead.seconds)}.`);
  return lines;
}

export function wrappedText(m: WrappedModel, o: WrappedOptions): string {
  return `My ${m.year} in Kipple\n${wrappedLines(m, o).join("\n")}`;
}

export type CardColor = "bg" | "surface" | "text" | "text2" | "accent";

export type CardOp =
  | { kind: "rect"; x: number; y: number; w: number; h: number; color: CardColor; radius?: number }
  | { kind: "text"; text: string; x: number; y: number; size: number; weight: 400 | 700; color: CardColor };

export const CARD_W = 1080;
export const CARD_H = 1350;

const clip = (s: string, n: number): string => (s.length > n ? `${s.slice(0, n - 1).trimEnd()}…` : s);

/** Draw instructions for the share card: pure, so the preview, the PNG and the tests all read the same list. */
export function buildWrappedCard(m: WrappedModel, o: WrappedOptions): CardOp[] {
  const ops: CardOp[] = [{ kind: "rect", x: 0, y: 0, w: CARD_W, h: CARD_H, color: "bg" }];
  const X = 90;
  const text = (t: string, x: number, y: number, size: number, weight: 400 | 700 = 400, color: CardColor = "text") => ops.push({ kind: "text", text: t, x, y, size, weight, color });
  ops.push({ kind: "rect", x: X, y: 90, w: 120, h: 10, color: "accent", radius: 5 });
  text(`My ${m.year} in Kipple`, X, 180, 52, 700, "text2");
  text(String(m.itemsRead), X, 350, 190, 700, "accent");
  text(m.itemsRead === 1 ? "item read" : "items read", X, 410, 56);
  const tiles: [string, string][] = [
    [hoursLabel(m.activeSeconds).replace(" hours", " h"), "active reading"],
    [String(m.daysActive), m.daysActive === 1 ? "day with reading" : "days with reading"],
    [String(m.longestStreak), m.longestStreak === 1 ? "day, longest streak" : "days, longest streak"],
  ];
  tiles.forEach(([big, small], i) => {
    const x = X + i * 300;
    ops.push({ kind: "rect", x, y: 470, w: 270, h: 190, color: "surface", radius: 24 });
    text(clip(big, 8), x + 24, 560, 60, 700);
    text(clip(small, 22), x + 24, 620, 26, 400, "text2");
  });
  const fact = (label: string, value: string, x: number, y: number) => {
    text(label, x, y, 28, 400, "text2");
    text(clip(value, 22), x, y + 54, 46, 700);
  };
  if (m.busiestWeekday != null) fact("Busiest day", weekdayName(m.busiestWeekday), X, 740);
  if (m.busiestHour != null) fact("Busiest hour", hourLabel(m.busiestHour), X + 480, 740);
  if (m.busiestMonth != null) fact("Busiest month", monthName(m.busiestMonth), X, 860);
  let y = 980;
  if (o.topSources && m.topSources.length) {
    text("Top sources", X, y, 28, 400, "text2");
    m.topSources.slice(0, 3).forEach((s, i) => text(`${clip(s.name, 30)}  ${s.items}`, X, y + 48 + i * 42, 32));
    y += 48 + Math.min(3, m.topSources.length) * 42 + 20;
  }
  if (o.longestRead && m.longestRead) {
    text("Longest read", X, y, 28, 400, "text2");
    text(clip(m.longestRead.title, 44), X, y + 48, 32);
    text(durationLabel(m.longestRead.seconds), X, y + 92, 28, 400, "text2");
  }
  return ops;
}
