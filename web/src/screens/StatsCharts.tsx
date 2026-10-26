import { useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from "react";
import type { StatsSummary } from "@/api/types";
import {
  dateWithYear,
  durationLabel,
  heatLevel,
  hourLabel,
  activityBars,
  NOTHING_IN_RANGE,
  monthName,
  monthlyBars,
  plural,
  shortDate,
  weekOrder,
  weekdayName,
} from "@/lib/statsFormat";
import { HEAT_MIX } from "@/theme/contrast";

export function Empty() {
  return <p className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-sm text-fg2">{NOTHING_IN_RANGE}.</p>;
}

// ---- Bar chart --------------------------------------------------------------------------------------------------

interface Bar {
  key: string;
  value: number;
  /** The axis label when this bar is the first, middle or last one. */
  label: string;
  /** What a tap on the bar shows under the chart. */
  detail: string;
}

/** The widest a bar's column gets: a chart of one bar is one bar wide, never a stretched block. */
const DAY_SLOT = 24;
const WEEK_SLOT = 12;
const MONTH_SLOT = 40;

/**
 * Fixed-width bars with the largest value labelled at the top and zero at the baseline. A tap (or a click, or the arrow
 * keys) on a bar shows its date and number in a line under the chart; the line is a status, so a screen reader hears it.
 * The bars are picked by pointer position rather than one button each, so a year of 1 px bars is still pickable.
 */
function BarChart({ bars, summary, slot, hint }: { bars: Bar[]; summary: string; slot: number; hint: string }) {
  const [sel, setSel] = useState<string | null>(null);
  const max = Math.max(1, ...bars.map((b) => b.value));
  const at = sel == null ? -1 : bars.findIndex((b) => b.key === sel);
  const pick = (e: PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    if (r.width <= 0) return;
    const i = Math.min(bars.length - 1, Math.max(0, Math.floor(((e.clientX - r.left) / r.width) * bars.length)));
    setSel(bars[i]!.key);
  };
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    const to = e.key === "ArrowLeft" ? Math.max(0, (at < 0 ? bars.length : at) - 1) : e.key === "ArrowRight" ? Math.min(bars.length - 1, at + 1) : e.key === "Home" ? 0 : e.key === "End" ? bars.length - 1 : null;
    if (to != null) {
      e.preventDefault();
      setSel(bars[to]!.key);
    } else if (e.key === "Escape" && sel != null) {
      e.stopPropagation();
      setSel(null);
    }
  };
  const mid = bars[Math.floor((bars.length - 1) / 2)]!;
  const shown = at >= 0 ? bars[at]! : null;
  return (
    <div>
      <div className="flex gap-2">
        <div aria-hidden="true" className="flex h-32 w-8 shrink-0 flex-col justify-between text-right text-xs leading-none tabular-nums text-fg2">
          <span>{max}</span>
          <span>0</span>
        </div>
        <div className="min-w-0 flex-1">
          <div style={{ maxWidth: bars.length * slot }}>
            <div
              role="group"
              tabIndex={0}
              aria-label={summary}
              onPointerDown={pick}
              onPointerMove={(e) => {
                if (e.pointerType === "mouse") pick(e);
              }}
              onKeyDown={key}
              className="flex h-32 touch-pan-y items-end gap-px border-y border-line"
            >
              {bars.map((b, i) => (
                <div key={b.key} aria-hidden="true" className="flex h-full min-w-0 flex-1 items-end">
                  <div
                    className={`w-full rounded-t-sm bg-accent ${at >= 0 && at !== i ? "opacity-40" : ""}`}
                    style={{ height: `${(b.value / max) * 100}%`, minHeight: b.value > 0 ? 3 : 0 }}
                  />
                </div>
              ))}
            </div>
            <div aria-hidden="true" className="mt-1 flex justify-between text-xs text-fg2">
              <span>{bars[0]!.label}</span>
              {bars.length > 14 ? <span className="hidden min-[420px]:inline">{mid.label}</span> : null}
              {bars.length > 1 ? <span>{bars[bars.length - 1]!.label}</span> : null}
            </div>
          </div>
        </div>
      </div>
      <p role="status" className="mt-2 min-h-5 text-sm">
        {shown ? shown.detail : <span className="text-fg2">{hint}</span>}
      </p>
    </div>
  );
}

export function DailyChart({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const { unit, rows: daily } = activityBars(data.daily ?? [], data.week_start);
  if (empty || daily.length === 0) return <Empty />;
  const week = unit === "week";
  const when = (d: { date: string; end: string }) => (week ? `${shortDate(d.date)} to ${shortDate(d.end)}` : shortDate(d.date));
  const total = daily.reduce((a, d) => a + d.items_read, 0);
  const busiest = daily.reduce((a, d) => (d.items_read > a.items_read ? d : a), daily[0]!);
  const summary = `Articles read per ${unit}, ${shortDate(daily[0]!.date)} to ${shortDate(daily[daily.length - 1]!.end)}. ${plural(total, "article")} in total. Most in one ${unit}: ${busiest.items_read}, ${week ? "" : "on "}${when(busiest)}. Use the arrow keys to hear a ${unit}.`;
  const bars = daily.map((d, i) => ({
    key: d.date,
    value: d.items_read,
    // The axis ends where the data does: the last week's end, not its start.
    label: shortDate(week && i === daily.length - 1 ? d.end : d.date),
    detail: `${when(d)}: ${plural(d.items_read, "article")}, ${durationLabel(d.active_seconds)}`,
  }));
  return (
    <div>
      <BarChart bars={bars} summary={summary} slot={week ? WEEK_SLOT : DAY_SLOT} hint={week ? "Tap a bar for its week." : "Tap a bar for its day."} />
      <div className="sr-only"><table>
        <caption>Articles read and active time per {unit}</caption>
        <thead>
          <tr>
            <th scope="col">{week ? "Week of" : "Date"}</th>
            <th scope="col">Articles read</th>
            <th scope="col">Active time</th>
          </tr>
        </thead>
        <tbody>
          {daily
            .filter((d) => d.items_read > 0 || d.active_seconds > 0)
            .map((d) => (
              <tr key={d.date}>
                <th scope="row">{dateWithYear(d.date)}</th>
                <td>{d.items_read}</td>
                <td>{durationLabel(d.active_seconds)}</td>
              </tr>
            ))}
        </tbody>
      </table></div>
    </div>
  );
}

/** One bar per calendar month on record, from the first month with reading through this one. */
export function MonthlyChart({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const months = monthlyBars(data.daily ?? []);
  if (empty || months.length === 0) return <Empty />;
  const total = months.reduce((a, m) => a + m.items_read, 0);
  const busiest = months.reduce((a, m) => (m.items_read > a.items_read ? m : a), months[0]!);
  const summary = `Articles read per month, ${monthName(months[0]!.month)} to ${monthName(months[months.length - 1]!.month)}. ${plural(total, "article")} in total. Most in one month: ${busiest.items_read}, in ${monthName(busiest.month)}. Use the arrow keys to hear a month.`;
  const bars = months.map((m) => ({
    key: m.month,
    value: m.items_read,
    label: monthName(m.month),
    detail: `${monthName(m.month)}: ${plural(m.items_read, "article")}, ${durationLabel(m.active_seconds)}`,
  }));
  return (
    <div>
      <BarChart bars={bars} summary={summary} slot={MONTH_SLOT} hint="Tap a bar for its month." />
      <div className="sr-only"><table>
        <caption>Articles read and active time per month</caption>
        <thead>
          <tr>
            <th scope="col">Month</th>
            <th scope="col">Articles read</th>
            <th scope="col">Active time</th>
          </tr>
        </thead>
        <tbody>
          {months.map((m) => (
            <tr key={m.month}>
              <th scope="row">{monthName(m.month, "long")}</th>
              <td>{m.items_read}</td>
              <td>{durationLabel(m.active_seconds)}</td>
            </tr>
          ))}
        </tbody>
      </table></div>
    </div>
  );
}

// ---- Heatmap ----------------------------------------------------------------------------------------------------

const heatStyle = (level: number) => ({ "--heat": HEAT_MIX[level] }) as CSSProperties;

const hourTick = (h: number) => (h === 0 ? "12a" : h === 12 ? "12p" : h < 12 ? `${h}a` : `${h - 12}p`);

export function Heatmap({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const [sel, setSel] = useState<{ wd: number; h: number } | null>(null);
  const cells = data.heatmap ?? [];
  if (empty || cells.length === 0) return <Empty />;
  const useTime = cells.some((c) => c.active_seconds > 0);
  const val = (c: { active_seconds: number; opens: number }) => (useTime ? c.active_seconds : c.opens);
  // Reads from before reading time was recorded have no time: in a range that also has timed reading they still show,
  // at the lightest shade.
  const untimed = (c: { active_seconds: number; opens: number }) => useTime && c.active_seconds <= 0 && c.opens > 0;
  const mixed = cells.some(untimed);
  const by = new Map(cells.map((c) => [`${c.weekday}:${c.hour}`, c]));
  const ranked = cells.filter((c) => !untimed(c)).map(val).filter((v) => v > 0).sort((a, b) => a - b);
  const hours = Array.from({ length: 24 }, (_, h) => h);
  const days = weekOrder(data.week_start);
  const what = (wd: number, h: number) => {
    const c = by.get(`${wd}:${h}`);
    const v = c ? val(c) : 0;
    if (c && untimed(c)) return `${plural(c.opens, "article")}, no reading time recorded`;
    return v > 0 ? (useTime ? durationLabel(v) : plural(v, "article")) : "none";
  };
  const move = (e: KeyboardEvent<HTMLDivElement>) => {
    const step: Record<string, [number, number]> = { ArrowLeft: [0, -1], ArrowRight: [0, 1], ArrowUp: [-1, 0], ArrowDown: [1, 0] };
    const d = step[e.key];
    if (e.key === "Escape" && sel) {
      e.stopPropagation();
      setSel(null);
    } else if (d) {
      e.preventDefault();
      // With nothing selected the first press selects the first cell, like the bars.
      if (!sel) return setSel({ wd: days[0]!, h: 0 });
      const r = Math.min(6, Math.max(0, days.indexOf(sel.wd) + d[0]));
      const c = Math.min(23, Math.max(0, sel.h + d[1]));
      setSel({ wd: days[r]!, h: c });
    }
  };
  return (
    <div>
      <p className="mb-2 text-xs text-fg2">
        {useTime ? "Reading time by weekday and hour." : "Articles read by weekday and hour."}
        {mixed ? " Reads with no recorded time show as the lightest shade." : ""} Tap a square for its day and hour.
      </p>
      <div className="max-w-3xl" tabIndex={0} role="group" aria-label="Reading by weekday and hour. Use the arrow keys to move between hours." onKeyDown={move}>
        <table className="w-full table-fixed border-separate border-spacing-0.5">
          <caption className="sr-only">{useTime ? "Active reading time" : "Articles read"} by weekday and hour</caption>
          <colgroup>
            <col style={{ width: "2.25rem" }} />
            {hours.map((h) => (
              <col key={h} />
            ))}
          </colgroup>
          <thead>
            <tr>
              <td />
              {hours.map((h) => (
                <th key={h} scope="col" className="overflow-visible text-left text-micro leading-none font-normal whitespace-nowrap text-fg2">
                  <span aria-hidden="true">{h % 3 === 0 ? hourTick(h) : ""}</span>
                  <span className="sr-only">{hourLabel(h)}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {days.map((wd) => (
              <tr key={wd}>
                <th scope="row" className="pr-1 text-left text-xs font-medium whitespace-nowrap text-fg2">
                  {weekdayName(wd, "short")}
                </th>
                {hours.map((h) => {
                  const c = by.get(`${wd}:${h}`);
                  const level = c && untimed(c) ? 1 : heatLevel(c ? val(c) : 0, ranked);
                  const on = sel?.wd === wd && sel.h === h;
                  return (
                    <td
                      key={h}
                      data-level={level}
                      data-selected={on || undefined}
                      onClick={() => setSel({ wd, h })}
                      className="heat-cell relative h-5 rounded-sm border border-line p-0 data-[selected]:outline-2 data-[selected]:outline-offset-1 data-[selected]:outline-fg"
                      style={heatStyle(level)}
                    >
                      <span className="sr-only">{`${weekdayName(wd)} ${hourLabel(h)}: ${what(wd, h)}`}</span>
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p role="status" className="mt-2 min-h-5 text-sm">
        {sel ? `${weekdayName(sel.wd)}, ${hourLabel(sel.h)}: ${what(sel.wd, sel.h)}` : null}
      </p>
      <div aria-hidden="true" className="mt-1 flex items-center gap-1 text-xs text-fg2">
        Less
        {[0, 1, 2, 3, 4].map((l) => (
          <span key={l} className="heat-cell inline-block size-3 rounded-sm border border-line" style={heatStyle(l)} />
        ))}
        More
      </div>
    </div>
  );
}
