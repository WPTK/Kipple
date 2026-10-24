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
function wrappedStateOf(qc: QueryClient | undefined): WrappedState {
  const b = qc?.getQueryData<Bootstrap>(keys.bootstrap);
  if (b === undefined) return "unknown";
  if (b.settings?.["stats.enabled"] === false) return "off";
  return b.settings?.["stats.wrapped_enabled"] === false ? "off" : "on";
}

/** Whether Wrapped shows (statistics on and Wrapped on). Reads the cached bootstrap only; absent means hidden. */
export function useWrappedEnabled(): boolean {
  return useWrappedState() === "on";
}

/**
 * The full tri-state, for callers that must tell "not known yet" apart from "off" (the Wrapped screen itself: an
 * unknown bootstrap answer, such as a direct load of /stats/wrapped, is not the same as the setting being off).
 */
export function useWrappedState(): WrappedState {
  const qc = useQueryClient();
  const subscribe = useCallback(
    (notify: () => void) =>
      qc.getQueryCache().subscribe((e) => {
        if (e.query.queryKey[0] === keys.bootstrap[0]) notify();
      }),
    [qc],
  );
  return useSyncExternalStore(subscribe, () => wrappedStateOf(qc));
}

/**
 * Whether a fetched summary actually belongs to the selected year. With `keepPreviousData`, the query can hand back
 * the previous year's response while the new year's request is still in flight; a model must never be derived from
 * that mismatched data (deriveWrapped(previousYearData, newYear) would silently mislabel it).
 */
export function summaryMatchesYear(data: StatsSummary | null | undefined, year: number): boolean {
  const from = data?.range?.from;
  return typeof from === "string" && from.startsWith(`${year}-`);
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
  topSources: { feedId: string; name: string; items: number }[];
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
  // The server already computes these correctly for the requested range (and is what the main Stats screen shows
  // for the same range), so trust it over a client recompute — a tie can break differently between the two.
  // Recompute from the heatmap only when there is no behavior block to ask at all.
  const b = data.behavior;
  const weekday = b ? (b.busiest_weekday?.weekday ?? null) : cells.length ? by((c) => c.weekday) : null;
  const hour = b ? (b.busiest_hour?.hour ?? null) : cells.length ? by((c) => c.hour) : null;
  const t = data.totals;
  const span = yearSpan(year, today);
  const start = data.first_event_date && data.first_event_date > span.from ? data.first_event_date : span.from;
  const topSources = [...(data.sources ?? [])]
    .filter((s) => s.items_read > 0)
    .sort((a, c) => c.items_read - a.items_read || a.feed_title.localeCompare(c.feed_title))
    .slice(0, TOP_SOURCES)
    .map((s) => ({ feedId: s.feed_id, name: s.feed_title, items: s.items_read }));
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
  const rounded = h >= 10 ? Math.round(h) : Math.round(h * 10) / 10;
  return `${rounded} ${rounded === 1 ? "hour" : "hours"}`;
}

/**
 * Whether active-reading seconds are a real fact for this model, rather than a legacy gap: items were read (or
 * opened) with no seconds ever recorded for them, which is not the same as zero reading having happened.
 */
const activeSecondsKnown = (m: WrappedModel): boolean => m.activeSeconds > 0 || m.itemsRead === 0;

/** The active-reading fact as it should read in the card and the text, consistently: a duration, or "not recorded". */
function activeReadingFact(m: WrappedModel): string {
  return activeSecondsKnown(m) ? hoursLabel(m.activeSeconds) : "not recorded";
}

// ---- Share options, text and card -------------------------------------------------------------------------------

export interface WrappedOptions {
  topSources: boolean;
  longestRead: boolean;
}
export const DEFAULT_OPTIONS: WrappedOptions = { topSources: false, longestRead: false };

/** The facts as lines of plain text. Feed names and titles appear only when the options ask for them. */
export function wrappedLines(m: WrappedModel, o: WrappedOptions): string[] {
  const lines = [`I read ${plural(m.itemsRead, "article")} in ${m.year}.`];
  lines.push(activeSecondsKnown(m) ? `${activeReadingFact(m)} of active reading.` : "Active reading time not recorded.");
  lines.push(`${plural(m.daysActive, "day")} with reading; longest streak ${plural(m.longestStreak, "day")}.`);
  if (m.busiestWeekday != null && m.busiestHour != null) lines.push(`Busiest day: ${weekdayName(m.busiestWeekday)}, busiest hour: ${hourLabel(m.busiestHour)}.`);
  else if (m.busiestWeekday != null) lines.push(`Busiest day: ${weekdayName(m.busiestWeekday)}.`);
  else if (m.busiestHour != null) lines.push(`Busiest hour: ${hourLabel(m.busiestHour)}.`);
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

/** Truncates on a code-point boundary, so a surrogate pair (an opted-in feed name or title with an emoji) is never split. */
const clip = (s: string, n: number): string => {
  const points = Array.from(s);
  return points.length > n ? `${points.slice(0, n - 1).join("").trimEnd()}…` : s;
};

/** Words wrapped onto at most `lines` lines of `width` characters; only a title longer than all of them is cut. */
function wrapLines(s: string, width: number, lines: number): string[] {
  const out: string[] = [];
  let line = "";
  for (const word of s.split(/\s+/).filter(Boolean)) {
    const next = line ? `${line} ${word}` : word;
    if (Array.from(next).length <= width || !line) line = next;
    else {
      out.push(line);
      line = word;
    }
  }
  if (line) out.push(line);
  if (out.length <= lines) return out.map((l) => clip(l, width));
  return [...out.slice(0, lines - 1).map((l) => clip(l, width)), clip(out.slice(lines - 1).join(" "), width)];
}

/** Words wrapped to lines of at most `width` characters, none dropped: a word longer than a line is broken across lines. */
function wrapWhole(s: string, width: number): string[] {
  const out: string[] = [];
  let line = "";
  for (const word of s.split(/\s+/).filter(Boolean)) {
    const next = line ? `${line} ${word}` : word;
    if (Array.from(next).length <= width) {
      line = next;
      continue;
    }
    if (line) out.push(line);
    const points = Array.from(word);
    let at = 0;
    for (; points.length - at > width; at += width) out.push(points.slice(at, at + width).join(""));
    line = points.slice(at).join("");
  }
  if (line) out.push(line);
  return out.length ? out : [""];
}

const MARGIN = 90;
const TITLE_CHARS = 46;
const SOURCE_CHARS = 40;
const MIN_GAP = 20;

/**
 * Draw instructions for the share card: pure, so the preview, the PNG and the tests all read the same list. The blocks
 * (heading, count, three tiles, three facts, then the opted-in sources and longest read) are stacked down the card with
 * the leftover height shared between them, so the card is filled whatever is switched on and nothing is cut to fit.
 */
export function buildWrappedCard(m: WrappedModel, o: WrappedOptions): CardOp[] {
  const ops: CardOp[] = [{ kind: "rect", x: 0, y: 0, w: CARD_W, h: CARD_H, color: "bg" }];
  const X = MARGIN;
  const text = (t: string, x: number, y: number, size: number, weight: 400 | 700 = 400, color: CardColor = "text") => ops.push({ kind: "text", text: t, x, y, size, weight, color });

  const tiles: [string, string][] = [
    [activeSecondsKnown(m) ? activeReadingFact(m).replace(" hours", " h").replace(" hour", " h") : "n/a", "active reading"],
    [String(m.daysActive), m.daysActive === 1 ? "day with reading" : "days with reading"],
    [String(m.longestStreak), m.longestStreak === 1 ? "day, longest streak" : "days, longest streak"],
  ];
  const facts: [string, string][] = [];
  if (m.busiestWeekday != null) facts.push(["Busiest day", weekdayName(m.busiestWeekday)]);
  if (m.busiestHour != null) facts.push(["Busiest hour", hourLabel(m.busiestHour)]);
  if (m.busiestMonth != null) facts.push(["Busiest month", monthName(m.busiestMonth)]);
  const sources = o.topSources ? m.topSources : [];
  const title = o.longestRead && m.longestRead ? wrapLines(m.longestRead.title, TITLE_CHARS, 2) : [];
  // Source names are wrapped whole, never cut, so the rows take whatever height the names need. The other blocks have
  // fixed heights; the sources get what is left after them and the smallest gap, shrinking their type until every
  // name fits.
  const titleH = title.length ? 38 + title.length * 42 + 44 : 0;
  const otherH = 100 + 240 + 190 + (facts.length ? 100 : 0) + titleH;
  const blockCount = 3 + (facts.length ? 1 : 0) + (title.length ? 1 : 0) + (sources.length ? 1 : 0);
  const budget = CARD_H - 2 * MARGIN - otherH - MIN_GAP * (blockCount - 1);
  const sourceRows = (scale: number) => {
    const lines = sources.map((s) => wrapWhole(s.name, Math.floor(SOURCE_CHARS / scale)));
    const line = Math.round(38 * scale);
    const pad = Math.round(8 * scale);
    return { lines, line, pad, font: Math.round(32 * scale), h: 38 + lines.reduce((a, l) => a + l.length * line + pad, 0) };
  };
  let scale = 1;
  let rows = sourceRows(scale);
  while (rows.h > budget && scale > 0.3) {
    scale = Math.round((scale - 0.05) * 100) / 100;
    rows = sourceRows(scale);
  }

  // Each block: its height, and how it draws itself from its top edge.
  const blocks: { h: number; draw: (top: number) => void }[] = [
    {
      h: 100,
      draw: (top) => {
        ops.push({ kind: "rect", x: X, y: top, w: 120, h: 10, color: "accent", radius: 5 });
        text(`My ${m.year} in Kipple`, X, top + 90, 52, 700, "text2");
      },
    },
    {
      h: 240,
      draw: (top) => {
        text(String(m.itemsRead), X, top + 170, 190, 700, "accent");
        text(m.itemsRead === 1 ? "article read" : "articles read", X, top + 235, 56);
      },
    },
    {
      h: 190,
      draw: (top) =>
        tiles.forEach(([big, small], i) => {
          const x = X + i * 300;
          ops.push({ kind: "rect", x, y: top, w: 270, h: 190, color: "surface", radius: 24 });
          text(clip(big, 8), x + 24, top + 90, 60, 700);
          text(clip(small, 22), x + 24, top + 150, 26, 400, "text2");
        }),
    },
  ];
  if (facts.length) {
    blocks.push({
      h: 100,
      draw: (top) =>
        facts.forEach(([label, value], i) => {
          const x = X + i * 320;
          text(label, x, top + 28, 28, 400, "text2");
          text(clip(value, 12), x, top + 82, 44, 700);
        }),
    });
  }
  if (sources.length) {
    blocks.push({
      h: rows.h,
      draw: (top) => {
        text("Top sources", X, top + 28, 28, 400, "text2");
        let y = top + 40 + rows.line;
        sources.forEach((s, i) => {
          text(String(s.items), X, y, rows.font, 700, "accent");
          rows.lines[i]!.forEach((l, j) => text(l, X + 90, y + j * rows.line, rows.font));
          y += rows.lines[i]!.length * rows.line + rows.pad;
        });
      },
    });
  }
  if (title.length && m.longestRead) {
    const l = m.longestRead;
    blocks.push({
      h: titleH,
      draw: (top) => {
        text("Longest read", X, top + 28, 28, 400, "text2");
        title.forEach((t, i) => text(t, X, top + 78 + i * 42, 32));
        text(durationLabel(l.seconds), X, top + 78 + title.length * 42 + 2, 28, 400, "text2");
      },
    });
  }
  const used = blocks.reduce((a, b) => a + b.h, 0);
  const free = CARD_H - 2 * MARGIN - used;
  const gap = Math.max(MIN_GAP, Math.min(150, free / (blocks.length - 1)));
  let top = MARGIN;
  for (const b of blocks) {
    b.draw(Math.round(top));
    top += b.h + gap;
  }
  return ops;
}