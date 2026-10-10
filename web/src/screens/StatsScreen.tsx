import { useId, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { Link } from "react-router";
import { ChevronRight } from "lucide-react";
import { useSpanSummary, useStatsSummary } from "@/api/stats";
import { errorMessage } from "@/api/client";
import type { StatsSummary } from "@/api/types";
import {
  dateWithYear,
  daysBetween,
  durationLabel,
  feedRows,
  folderRows,
  heatLevel,
  hourLabel,
  loadScreenRange,
  apiRange,
  SCREEN_RANGES,
  changeLabel,
  monthName,
  monthlyBars,
  previousPeriod,
  type ScreenRange,
  mostStarred,
  pctLabel,
  plural,
  readRateNote,
  saveRange,
  shortDate,
  sortRows,
  todayString,
  weekOrder,
  weekdayName,
  type SourceMetric,
  type SourceRow,
} from "@/lib/statsFormat";
import { HEAT_MIX } from "@/theme/contrast";
import { useStatsEnabled } from "@/lib/statsSender";
import { useMedia } from "@/lib/useMedia";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton } from "@/ui/kit";
import { Segmented } from "@/ui/segmented";
import { useWrappedEnabled } from "@/lib/wrapped";
import { StatsDataSection, StatsExportDialog } from "./StatsDataDialogs";

function Section({ title, children, sub = false }: { title: string; children: ReactNode; sub?: boolean }) {
  const id = useId();
  const H = sub ? "h3" : "h2";
  return (
    <section aria-labelledby={id} className={sub ? "border-t border-line pt-4" : "border-b border-line py-5"}>
      <H id={id} className={sub ? "mb-3 text-base font-bold" : "mb-3 text-lg font-bold"}>
        {title}
      </H>
      {children}
    </section>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <p className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-sm text-fg2">{children}</p>;
}

// ---- 1. Summary strip -------------------------------------------------------------------------------------------

function Stat({
  label,
  value,
  note,
  onToggle,
  describedBy,
}: {
  label: string;
  value: string;
  note?: string | null;
  onToggle?: () => void;
  describedBy?: string;
}) {
  const inner = (
    <>
      <span className="block text-xl font-bold tabular-nums">{value}</span>
      <span className="block text-xs text-fg2">{label}</span>
      {note ? <span className="mt-1 block text-xs font-medium tabular-nums">{note}</span> : null}
    </>
  );
  return (
    <div className="min-w-0 rounded-xl bg-surface">
      {onToggle ? (
        <button type="button" onClick={onToggle} aria-expanded={note != null} aria-describedby={note != null ? describedBy : undefined} className="block w-full min-w-0 rounded-xl px-3 py-3 text-left">
          {inner}
        </button>
      ) : (
        <div className="px-3 py-3">{inner}</div>
      )}
    </div>
  );
}

/** What makes an open a read (design §8, `store.statsIsRead`); the thresholds live on the server. */
export const READ_RULE =
  "An article counts as read after 10 seconds of reading, or 3 seconds once you've scrolled a quarter of the way down.";

type Tile = "items" | "time" | "days";

export function SummaryStrip({ data, compare = false }: { data: StatsSummary; compare?: boolean }) {
  return (
    <Section title="Summary">
      <SummaryTiles data={data} compare={compare} />
    </Section>
  );
}

/**
 * The three summary tiles of `data`, each comparable on a tap, and what counts as a read. With `feed`, the earlier
 * period is that feed's too.
 */
export function SummaryTiles({ data, compare = false, feed }: { data: StatsSummary; compare?: boolean; feed?: string }) {
  const t = data.totals;
  const legacy = t?.legacy_opens ?? 0;
  const noteId = useId();
  // Tiles show plain numbers. Tapping one shows its previous-period value; the earlier period is fetched on the first tap.
  const [open, setOpen] = useState<ReadonlySet<Tile>>(new Set());
  const period = useMemo(() => (compare && data.range ? previousPeriod(data.range) : null), [compare, data.range]);
  // Statistics off, deleted, or not yet timed leave gaps, which are not quiet days: a tile whose earlier span starts
  // before the server's covered date (opens) or timed date (active time) makes no comparison.
  const thinFor = (k: Tile) => {
    const from = k === "time" ? data.timed_from : data.covered_from;
    return period == null || from == null || period.from < from;
  };
  const wanted = (["items", "time", "days"] as const).some((k) => open.has(k) && !thinFor(k));
  const prevSpan = useSpanSummary(wanted ? period : null, true, feed);
  const curSpan = useSpanSummary(wanted && period ? period.current : null, true, feed);
  const prev = prevSpan.data?.totals;
  const cur = curSpan.data?.totals;
  const failed = wanted && (prevSpan.isError || curSpan.isError || (prevSpan.isSuccess && !prev) || (curSpan.isSuccess && !cur));
  const retry = () => {
    void prevSpan.refetch();
    void curSpan.refetch();
  };
  const flip = (k: Tile) =>
    setOpen((o) => {
      const n = new Set(o);
      if (!n.delete(k)) n.add(k);
      return n;
    });
  const noteFor = (k: Tile, now: number | undefined, before: number | undefined, show: (n: number) => string) => {
    if (thinFor(k)) return "Not enough history";
    if (now != null && before != null) return changeLabel(now, before, show);
    return failed ? "Unavailable" : "Loading";
  };
  const tile = (k: Tile, label: string, total: number, now: number | undefined, before: number | undefined, show: (n: number) => string) => (
    <Stat
      label={label}
      value={show(total)}
      onToggle={period ? () => flip(k) : undefined}
      describedBy={wanted ? noteId : undefined}
      note={period && open.has(k) ? noteFor(k, now, before, show) : null}
    />
  );
  const num = (n: number) => String(n);
  return (
    <>
      <div className="grid grid-cols-3 gap-2">
        {tile("items", "Items read", t?.items_read ?? 0, cur?.items_read, prev?.items_read, num)}
        {tile("time", "Active time", t?.active_seconds ?? 0, cur?.active_seconds, prev?.active_seconds, durationLabel)}
        {tile("days", "Days with reading", t?.days_active ?? 0, cur?.days_active, prev?.days_active, num)}
      </div>
      {period && wanted ? (
        <p id={noteId} role="status" className="mt-2 text-xs text-fg2">
          {`Complete days only, so today is left out of both: compared with ${period.label}.`}
        </p>
      ) : null}
      {failed ? (
        <div role="alert" className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
          Couldn't load the earlier period.
          <Button onClick={retry}>Try again</Button>
        </div>
      ) : null}
      <p className="mt-2 text-xs text-fg2">
        {READ_RULE}
        {legacy > 0
          ? ` ${plural(legacy, "article")} here ${legacy === 1 ? "was" : "were"} opened before Kipple measured reading time. ${legacy === 1 ? "It counts" : "They count"} as read but add${legacy === 1 ? "s" : ""} no time.`
          : ""}
      </p>
    </>
  );
}

// ---- 2. Daily activity ------------------------------------------------------------------------------------------

const BAR_W = 10;
const PAD = 8;
const CHART_H = 120;

export function DailyChart({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const daily = data.daily ?? [];
  if (empty || daily.length === 0) return <Empty>No reading in this range yet.</Empty>;
  const max = Math.max(1, ...daily.map((d) => d.items_read));
  const total = daily.reduce((a, d) => a + d.items_read, 0);
  const busiest = daily.reduce((a, d) => (d.items_read > a.items_read ? d : a), daily[0]!);
  const summary = `Items read per day, ${shortDate(daily[0]!.date)} to ${shortDate(daily[daily.length - 1]!.date)}. ${plural(total, "item")} in total. Most in one day: ${busiest.items_read}, on ${shortDate(busiest.date)}.`;
  const mid = daily[Math.floor((daily.length - 1) / 2)]!;
  return (
    <div>
      <svg
        role="img"
        aria-label={summary}
        viewBox={`0 0 ${daily.length * BAR_W + PAD * 2} ${CHART_H}`}
        preserveAspectRatio="none"
        className="block h-32 w-full rounded-lg bg-surface"
      >
        {daily.map((d, i) => {
          const h = d.items_read > 0 ? Math.max(3, (d.items_read / max) * (CHART_H - 6)) : 0;
          return (
            <rect key={d.date} x={PAD + i * BAR_W + 1} y={CHART_H - h} width={BAR_W - 2} height={h} fill="var(--color-accent)">
              <title>{`${shortDate(d.date)}: ${plural(d.items_read, "item")}, ${durationLabel(d.active_seconds)}`}</title>
            </rect>
          );
        })}
      </svg>
      <div aria-hidden="true" className="mt-1 flex justify-between text-xs text-fg2">
        <span>{shortDate(daily[0]!.date)}</span>
        {daily.length > 14 ? <span className="hidden min-[420px]:inline">{shortDate(mid.date)}</span> : null}
        <span>{shortDate(daily[daily.length - 1]!.date)}</span>
      </div>
      <p className="mt-1 text-xs text-fg2">Most in a day: {busiest.items_read}.</p>
      <table className="sr-only">
        <caption>Items read and active time per day</caption>
        <thead>
          <tr>
            <th scope="col">Date</th>
            <th scope="col">Items read</th>
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
      </table>
    </div>
  );
}

const MONTH_W = 28;

/** One bar per calendar month on record, from the first month with reading through this one. */
export function MonthlyChart({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const months = monthlyBars(data.daily ?? []);
  if (empty || months.length === 0) return <Empty>No reading in this range yet.</Empty>;
  const max = Math.max(1, ...months.map((m) => m.items_read));
  const total = months.reduce((a, m) => a + m.items_read, 0);
  const busiest = months.reduce((a, m) => (m.items_read > a.items_read ? m : a), months[0]!);
  const summary = `Items read per month, ${monthName(months[0]!.month)} to ${monthName(months[months.length - 1]!.month)}. ${plural(total, "item")} in total. Most in one month: ${busiest.items_read}, in ${monthName(busiest.month)}.`;
  return (
    <div>
      <div className="overflow-x-auto">
        <svg
          role="img"
          aria-label={summary}
          viewBox={`0 0 ${months.length * MONTH_W + PAD * 2} ${CHART_H}`}
          preserveAspectRatio="none"
          className="block h-32 w-full rounded-lg bg-surface"
          style={{ minWidth: Math.min(months.length * 14, 2000), maxWidth: (months.length * MONTH_W + PAD * 2) * 2 }}
        >
          {months.map((m, i) => {
            const h = m.items_read > 0 ? Math.max(3, (m.items_read / max) * (CHART_H - 6)) : 0;
            return (
              <rect key={m.month} x={PAD + i * MONTH_W + 2} y={CHART_H - h} width={MONTH_W - 4} height={h} fill="var(--color-accent)">
                <title>{`${monthName(m.month)}: ${plural(m.items_read, "item")}, ${durationLabel(m.active_seconds)}`}</title>
              </rect>
            );
          })}
        </svg>
      </div>
      <div aria-hidden="true" className="mt-1 flex justify-between text-xs text-fg2">
        <span>{monthName(months[0]!.month)}</span>
        <span>{monthName(months[months.length - 1]!.month)}</span>
      </div>
      <p className="mt-1 text-xs text-fg2">Most in a month: {busiest.items_read}, {monthName(busiest.month)}.</p>
      <table className="sr-only">
        <caption>Items read and active time per month</caption>
        <thead>
          <tr>
            <th scope="col">Month</th>
            <th scope="col">Items read</th>
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
      </table>
    </div>
  );
}

// ---- 3. Streaks -------------------------------------------------------------------------------------------------

export function Streaks({ data }: { data: StatsSummary }) {
  const s = data.streaks;
  if (!s || (s.current === 0 && s.longest === 0)) return <Empty>No streak yet.</Empty>;
  const today = data.range?.to ?? todayString();
  const running = s.current > 0 && (s.current >= s.longest || (s.longest_end != null && daysBetween(s.longest_end, today) <= 1));
  const longestLabel = running ? "Longest streak, still going" : s.longest_end ? `Longest streak, ended ${shortDate(s.longest_end)}` : "Longest streak";
  return (
    <div className="grid grid-cols-2 gap-2">
      <Stat label="Current streak" value={plural(s.current, "day")} />
      <Stat label={longestLabel} value={plural(s.longest, "day")} />
    </div>
  );
}

// ---- 4. Heatmap -------------------------------------------------------------------------------------------------

const heatStyle = (level: number) => ({ "--heat": HEAT_MIX[level] }) as CSSProperties;

export function Heatmap({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const cells = data.heatmap ?? [];
  if (empty || cells.length === 0) return <Empty>Nothing to show yet.</Empty>;
  const useTime = cells.some((c) => c.active_seconds > 0);
  const val = (c: { active_seconds: number; opens: number }) => (useTime ? c.active_seconds : c.opens);
  // Legacy opens have no read time: in a range that also has timed reading they still show, at the lightest shade.
  const untimed = (c: { active_seconds: number; opens: number }) => useTime && c.active_seconds <= 0 && c.opens > 0;
  const mixed = cells.some(untimed);
  const by = new Map(cells.map((c) => [`${c.weekday}:${c.hour}`, c]));
  const max = Math.max(0, ...cells.map(val));
  const hours = Array.from({ length: 24 }, (_, h) => h);
  const days = weekOrder(data.week_start);
  return (
    <div>
      <p className="mb-2 text-xs text-fg2">
        {useTime ? "Reading time by weekday and hour." : "Opens by weekday and hour."}
        {mixed ? " Opens with no recorded time show as the lightest shade." : ""}
      </p>
      <div className="max-w-3xl">
        <table className="w-full table-fixed border-separate border-spacing-0.5">
          <caption className="sr-only">{useTime ? "Active reading time" : "Opens"} by weekday and hour</caption>
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
                <th key={h} scope="col" className="overflow-visible text-left text-[10px] leading-none font-normal whitespace-nowrap text-fg2">
                  <span aria-hidden="true">{h % 6 === 0 ? (h === 0 ? "12a" : h === 12 ? "12p" : h < 12 ? `${h}a` : `${h - 12}p`) : ""}</span>
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
                  const v = c ? val(c) : 0;
                  const legacy = c ? untimed(c) : false;
                  const level = legacy ? 1 : heatLevel(v, max);
                  const what = legacy ? `${plural(c!.opens, "open")}, no reading time recorded` : v > 0 ? (useTime ? durationLabel(v) : plural(v, "open")) : "none";
                  const label = `${weekdayName(wd)} ${hourLabel(h)}: ${what}`;
                  return (
                    <td key={h} data-level={level} className="heat-cell relative h-5 rounded-sm border border-line p-0" style={heatStyle(level)}>
                      <span className="sr-only">{label}</span>
                      <span aria-hidden="true" title={label} className="absolute inset-0" />
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div aria-hidden="true" className="mt-2 flex items-center gap-1 text-xs text-fg2">
        Less
        {[0, 1, 2, 3, 4].map((l) => (
          <span key={l} className="heat-cell inline-block size-3 rounded-sm border border-line" style={heatStyle(l)} />
        ))}
        More
      </div>
    </div>
  );
}

// ---- 5. Behavior facts ------------------------------------------------------------------------------------------

export function Behavior({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const b = data.behavior;
  if (empty || !b) return <Empty>Nothing here until you've read a few articles.</Empty>;
  const facts: ReactNode[] = [];
  const amount = (x: { active_seconds: number; opens: number }) => (x.active_seconds > 0 ? durationLabel(x.active_seconds) : plural(x.opens, "open"));
  if (b.busiest_weekday) facts.push(<>Busiest day: {weekdayName(b.busiest_weekday.weekday)} ({amount(b.busiest_weekday)}).</>);
  if (b.busiest_hour) facts.push(<>Busiest hour: {hourLabel(b.busiest_hour.hour)} ({amount(b.busiest_hour)}).</>);
  if (b.avg_read_seconds != null) facts.push(<>Average read length: {durationLabel(b.avg_read_seconds)}.</>);
  if (b.longest_read) {
    const l = b.longest_read;
    facts.push(
      <>
        Longest read: {l.title}, from {l.feed_title}, {durationLabel(l.seconds)}, {shortDate(l.date)}.
      </>,
    );
  }
  if (facts.length === 0) return <Empty>Nothing here until you've read a few articles.</Empty>;
  return (
    <ul className="flex flex-col gap-2 text-sm">
      {facts.map((f, i) => (
        <li key={i} className="rounded-xl bg-surface px-3 py-2">
          {f}
        </li>
      ))}
    </ul>
  );
}

// ---- 6. Sources -------------------------------------------------------------------------------------------------

const SHOWN = 25;

function SourceName({ r, by }: { r: SourceRow; by: "feeds" | "folders" }) {
  // The suffix sits outside the truncating span so a long title is clipped, never the suffix.
  return (
    <span className="flex min-w-0 items-baseline">
      <span className="min-w-0 truncate font-medium" title={r.name}>
        {r.name}
      </span>
      {by === "feeds" && !r.subscribed ? <span className="shrink-0 whitespace-pre font-normal text-fg2"> (unsubscribed)</span> : null}
      {by === "folders" ? <span className="shrink-0 whitespace-pre font-normal text-fg2"> ({plural(r.count, "feed")})</span> : null}
    </span>
  );
}

/** A source's name and amount: for a feed, a button that opens its drill-down sheet. */
function SourceHead({ r, by, amount, onOpen }: { r: SourceRow; by: "feeds" | "folders"; amount: string; onOpen?: (r: SourceRow) => void }) {
  const inner = (
    <>
      <SourceName r={r} by={by} />
      <span className="ml-auto shrink-0 tabular-nums">{amount}</span>
    </>
  );
  if (by !== "feeds" || !onOpen) return <div className="flex items-baseline justify-between gap-2">{inner}</div>;
  return (
    <button
      type="button"
      aria-haspopup="dialog"
      onClick={() => onOpen(r)}
      className="flex min-h-11 w-full min-w-0 items-center gap-2 rounded text-left hover:text-link"
    >
      {inner}
      <ChevronRight aria-hidden="true" className="size-4 shrink-0 text-fg2" />
    </button>
  );
}

export function Sources({ data, onOpen }: { data: StatsSummary; onOpen?: (r: SourceRow) => void }) {
  const [metric, setMetric] = useState<SourceMetric>("items");
  const [by, setBy] = useState<"feeds" | "folders">("feeds");
  const [all, setAll] = useState(false);
  const wide = useMedia("(min-width: 640px)");
  const sources = data.sources;
  const rows = useMemo(() => {
    const base = by === "feeds" ? feedRows(sources ?? []) : folderRows(sources ?? []);
    return sortRows(base, metric).filter((r) => r.items_read > 0 || r.active_seconds > 0 || r.opens > 0 || r.stars > 0);
  }, [sources, metric, by]);
  if (rows.length === 0 && (sources?.length ?? 0) === 0) return <Empty>Nothing read yet.</Empty>;
  const val = (r: SourceRow) => (metric === "items" ? r.items_read : r.active_seconds);
  const amount = (r: SourceRow) => (metric === "items" ? String(r.items_read) : durationLabel(r.active_seconds));
  const max = Math.max(1, ...rows.map(val));
  const starred = mostStarred(by === "feeds" ? feedRows(sources ?? []) : folderRows(sources ?? []));
  const shown = all ? rows : rows.slice(0, SHOWN);
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-3">
        <Segmented
          legend="Measure"
          value={metric}
          onChange={setMetric}
          options={[
            { value: "items", label: "Items" },
            { value: "minutes", label: "Minutes" },
          ]}
        />
        <Segmented
          legend="Group by"
          value={by}
          onChange={setBy}
          options={[
            { value: "feeds", label: "Feeds" },
            { value: "folders", label: "Folders" },
          ]}
        />
      </div>
      {data.sources_truncated ? <p className="text-xs text-fg2">Showing the most active feeds only. Folder totals count just those.</p> : null}
      {starred.length > 0 ? (
        <p className="text-sm text-fg2">Most starred: {starred.map((r) => `${r.name} (${r.stars})`).join(", ")}.</p>
      ) : null}
      {wide ? (
      <div>
        <table className="w-full table-fixed text-left text-sm">
          <caption className="sr-only">
            {by === "feeds" ? "Feeds" : "Folders"} by {metric === "items" ? "items read" : "reading time"}
          </caption>
          <thead className="text-xs text-fg2">
            <tr className="border-b border-line">
              <th scope="col" className="py-2 pr-2 font-medium">
                {by === "feeds" ? "Feed" : "Folder"}
              </th>
              <th scope="col" className="w-20 px-2 py-2 text-right font-medium">
                Avg read
              </th>
              <th scope="col" className="w-20 px-2 py-2 text-right font-medium">
                Quick bounce
              </th>
              <th scope="col" className="w-24 px-2 py-2 text-right font-medium">
                Opened original
              </th>
              {by === "feeds" ? (
                <th scope="col" className="w-20 whitespace-nowrap px-2 py-2 text-right font-medium">
                  Read rate
                </th>
              ) : null}
              <th scope="col" className="w-14 py-2 pl-2 text-right font-medium">
                Stars
              </th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.key} className="border-b border-line align-top">
                <th scope="row" className="min-w-0 py-2 pr-2 text-left font-normal">
                  <SourceHead r={r} by={by} amount={amount(r)} onOpen={onOpen} />
                  <div aria-hidden="true" className="mt-1 h-1.5 rounded-full bg-surface">
                    <div className="h-full rounded-full bg-accent" style={{ width: `${Math.max(2, (val(r) / max) * 100)}%`, opacity: val(r) > 0 ? 1 : 0 }} />
                  </div>
                </th>
                <td className="px-2 py-2 text-right tabular-nums">{durationLabel(r.avg_read_seconds)}</td>
                <td className="px-2 py-2 text-right tabular-nums">{pctLabel(r.bounce_rate)}</td>
                <td className="px-2 py-2 text-right tabular-nums">{pctLabel(r.open_original_rate)}</td>
                {by === "feeds" ? <td className="px-2 py-2 text-right tabular-nums">{pctLabel(r.read_rate)}</td> : null}
                <td className="py-2 pl-2 text-right tabular-nums">{r.stars}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      ) : (
      <ul aria-label={`${by === "feeds" ? "Feeds" : "Folders"} by ${metric === "items" ? "items read" : "reading time"}`} className="flex flex-col divide-y divide-line">
        {shown.map((r) => (
          <li key={r.key} className="py-2 text-sm">
            <SourceHead r={r} by={by} amount={amount(r)} onOpen={onOpen} />
            <div aria-hidden="true" className="mt-1 h-1.5 rounded-full bg-surface">
              <div className="h-full rounded-full bg-accent" style={{ width: `${Math.max(2, (val(r) / max) * 100)}%`, opacity: val(r) > 0 ? 1 : 0 }} />
            </div>
            <p className="mt-1 flex flex-wrap gap-x-3 text-xs text-fg2">
              <span>Avg read {durationLabel(r.avg_read_seconds)}</span>
              <span>Quick bounce {pctLabel(r.bounce_rate)}</span>
              <span>Opened original {pctLabel(r.open_original_rate)}</span>
              {by === "feeds" ? <span>Read rate {pctLabel(r.read_rate)}</span> : null}
              <span>Stars {r.stars}</span>
            </p>
          </li>
        ))}
      </ul>
      )}
      {rows.length > SHOWN ? (
        <Button onClick={() => setAll((a) => !a)}>
          {all ? `Show the top ${SHOWN}` : `Show all ${rows.length}`}
        </Button>
      ) : null}
    </div>
  );
}

// ---- 7. Never opened --------------------------------------------------------------------------------------------

export function NeverOpened({ data }: { data: StatsSummary }) {
  const list = data.never_opened ?? [];
  if (list.length === 0) return <Empty>Every feed you subscribe to has had an article opened.</Empty>;
  return (
    <ul className="flex flex-col divide-y divide-line text-sm">
      {list.map((f) => (
        <li key={f.feed_id} className="py-2">
          <p className="font-medium">{f.title}</p>
          <p className="text-xs text-fg2">
            {f.folder_name ? `${f.folder_name} · ` : ""}subscribed on {dateWithYear(f.subscribed_on)}
          </p>
        </li>
      ))}
    </ul>
  );
}

// ---- 8. One feed ------------------------------------------------------------------------------------------------

const RANGE_WORDS: Record<ScreenRange, string> = {
  week: "This week",
  month: "The last 30 days",
  year: "The last 365 days",
  months: "Every month on record",
  all: "All time",
};

/** One feed's numbers over the screen's range, in a sheet opened from its row under Sources. */
export function FeedSheet({ feed, range, onClose }: { feed: SourceRow | null; range: ScreenRange; onClose: () => void }) {
  if (!feed) return null;
  return (
    <Modal
      open
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
      size="lg"
      title={feed.name}
      description={`${RANGE_WORDS[range]}${feed.subscribed ? "" : ". Unsubscribed"}.`}
      footer={<Button onClick={onClose}>Close</Button>}
    >
      <FeedDetail key={feed.key} feed={feed.key} range={range} />
    </Modal>
  );
}

/** The feed's read rate as a percentage (a dash with no data); a tap shows the counts behind it. */
function ReadRateTerm({ data }: { data: StatsSummary }) {
  const [shown, setShown] = useState(false);
  const noteId = useId();
  const r = data.read_rate;
  const pct = pctLabel(r?.rate);
  return (
    <div className="relative rounded-xl bg-surface px-3 py-2">
      <dt className="text-xs text-fg2">Read rate</dt>
      <dd>
        {/* The name stays "Read rate 25%" whether or not the counts are showing; they are the button's controlled region. */}
        <button
          type="button"
          aria-label={`Read rate ${pct === "-" ? "unknown" : pct}`}
          aria-expanded={shown}
          aria-controls={noteId}
          onClick={() => setShown((s) => !s)}
          className="block text-left text-base font-bold tabular-nums after:absolute after:inset-0 after:rounded-xl"
        >
          {pct}
        </button>
        <span id={noteId} hidden={!shown} className="mt-1 block text-xs font-medium tabular-nums">
          {shown ? readRateNote(r, data.read_rate_from, data.read_rate_to) : null}
        </span>
      </dd>
    </div>
  );
}

function FeedDetail({ feed, range }: { feed: string; range: ScreenRange }) {
  const q = useStatsSummary(apiRange(range), true, feed);
  const data = q.data;
  if (q.isPending) return <Skeleton rows={4} label="Loading this feed" />;
  if (!data) {
    return (
      <div role="alert" className="flex flex-col items-start gap-3">
        <Notice tone="error">{errorMessage(q.error)}</Notice>
        <Button onClick={() => void q.refetch()}>Try again</Button>
      </div>
    );
  }
  const t = data.totals;
  const empty = !t || (t.items_read === 0 && t.opens === 0 && t.active_seconds === 0);
  const s = data.sources?.[0];
  return (
    <>
      <div>
        <SummaryTiles data={data} compare feed={feed} />
      </div>
      {range === "months" ? (
        <Section sub title="Monthly activity">
          <MonthlyChart data={data} empty={empty} />
        </Section>
      ) : (
        <Section sub title="Daily activity">
          <DailyChart data={data} empty={empty} />
        </Section>
      )}
      <Section sub title="Engagement">
        <dl className="grid grid-cols-2 gap-2 min-[560px]:grid-cols-3">
          {(
            [
              ["Opens", String(t?.opens ?? 0)],
              ["Avg read", durationLabel(s?.avg_read_seconds)],
              ["Quick bounce", pctLabel(s?.bounce_rate)],
              ["Opened original", pctLabel(s?.open_original_rate)],
              ["Stars", String(s?.stars ?? 0)],
            ] as const
          ).map(([k, v]) => (
            <div key={k} className="rounded-xl bg-surface px-3 py-2">
              <dt className="text-xs text-fg2">{k}</dt>
              <dd className="text-base font-bold tabular-nums">{v}</dd>
            </div>
          ))}
          <ReadRateTerm data={data} />
        </dl>
      </Section>
      <Section sub title="Reading habits">
        <Behavior data={data} empty={empty} />
      </Section>
    </>
  );
}

// ---- Screen -----------------------------------------------------------------------------------------------------

export function StatsScreen() {
  const on = useStatsEnabled();
  const wrapped = useWrappedEnabled();
  const [range, setRange] = useState<ScreenRange>(loadScreenRange);
  const q = useStatsSummary(apiRange(range), on);
  const data = q.data;
  const [exporting, setExporting] = useState(false);
  const [feed, setFeed] = useState<SourceRow | null>(null);
  const pick = (r: ScreenRange) => {
    setRange(r);
    saveRange(r);
  };

  let body: ReactNode;
  if (!on || (data && !data.enabled)) {
    body = (
      <div className="py-8 text-center">
        <p className="text-sm text-fg2" role="status">
          Statistics are off. Turn them on in{" "}
          <Link to="/settings/statistics" className="text-link underline underline-offset-2">
            Settings &gt; Statistics
          </Link>
          .
        </p>
        <div className="mt-6 text-left">
          <StatsDataSection defaultRange={apiRange(range)} />
        </div>
      </div>
    );
  } else if (q.isPending) {
    body = <Skeleton rows={5} label="Loading statistics" />;
  } else if (q.isError && !data) {
    body = (
      <div role="alert" className="flex flex-col items-start gap-3 py-6">
        <Notice tone="error">{errorMessage(q.error)}</Notice>
        <Button onClick={() => void q.refetch()}>Try again</Button>
      </div>
    );
  } else if (data) {
    const t = data.totals;
    const empty = !t || (t.items_read === 0 && t.opens === 0 && t.active_seconds === 0);
    const today = data.range?.to ?? todayString();
    const history = data.first_event_date ? daysBetween(data.first_event_date, today) + 1 : null;
    body = (
      <div aria-busy={q.isPlaceholderData} className={q.isPlaceholderData ? "opacity-60" : undefined}>
        {q.isError ? (
          <div className="mt-3">
            <Notice tone="warn">
              <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
                Couldn't refresh; showing earlier numbers.
                <Button onClick={() => void q.refetch()}>Try again</Button>
              </span>
            </Notice>
          </div>
        ) : null}
        {apiRange(range) !== "all" && !q.isPlaceholderData && history != null && history >= 1 && history < 7 && (t?.days_active ?? 0) > 0 ? (
          <p className="mt-3 rounded-xl bg-surface px-3 py-2 text-sm text-fg2">
            Only {plural(history, "day")} of reading so far.
          </p>
        ) : null}
        <SummaryStrip key={`${data.range?.key}-${data.range?.from}`} data={data} compare />
        {wrapped ? (
          <div className="border-b border-line py-4">
            <Link to="/stats/wrapped" className="flex min-h-11 items-center justify-between gap-3 rounded-xl bg-surface px-4 py-3 text-sm font-medium">
              <span>Your year</span>
              <span aria-hidden="true" className="text-fg2">
                &rarr;
              </span>
            </Link>
          </div>
        ) : null}
        {range === "months" ? (
          <Section title="Monthly activity">
            <MonthlyChart data={data} empty={empty} />
          </Section>
        ) : (
          <Section title="Daily activity">
            <DailyChart data={data} empty={empty} />
          </Section>
        )}
        <Section title="Streaks">
          <Streaks data={data} />
        </Section>
        <Section title="When you read">
          <Heatmap data={data} empty={empty} />
        </Section>
        <Section title="Reading habits">
          <Behavior data={data} empty={empty} />
        </Section>
        <Section title="Sources">
          <Sources data={data} onOpen={setFeed} />
        </Section>
        <Section title="Never opened">
          <NeverOpened data={data} />
        </Section>
      </div>
    );
  }

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-3">
        <h1 className="pt-2 pb-2 text-xl font-bold" tabIndex={-1} data-route-heading>
          Stats
        </h1>
        {on && !(data && !data.enabled) ? (
          <div className="flex items-end gap-3">
            <div className="min-w-0 flex-1">
              <Segmented legend="Range" value={range} onChange={pick} options={SCREEN_RANGES} />
            </div>
            <Button onClick={() => setExporting(true)}>Export</Button>
          </div>
        ) : null}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-6">{body}</div>
      <StatsExportDialog open={exporting} onOpenChange={setExporting} defaultRange={apiRange(range)} />
      <FeedSheet feed={feed} range={range} onClose={() => setFeed(null)} />
    </div>
  );
}
