import { useId, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useStatsSummary } from "@/api/stats";
import { errorMessage } from "@/api/client";
import type { StatsRange, StatsSummary } from "@/api/types";
import {
  RANGES,
  dateWithYear,
  durationLabel,
  feedRows,
  folderRows,
  heatLevel,
  hourLabel,
  loadRange,
  mostStarred,
  pctLabel,
  plural,
  saveRange,
  shortDate,
  sortRows,
  weekOrder,
  weekdayName,
  type SourceMetric,
  type SourceRow,
} from "@/lib/statsFormat";
import { useStatsEnabled } from "@/lib/statsSender";
import { useMedia } from "@/lib/useMedia";
import { Button } from "@/ui/button";
import { Notice, Skeleton } from "@/ui/kit";
import { Segmented } from "@/ui/segmented";

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="border-b border-line py-5">
      <h2 id={id} className="mb-3 text-lg font-bold">
        {title}
      </h2>
      {children}
    </section>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <p className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-sm text-fg2">{children}</p>;
}

// ---- 1. Summary strip -------------------------------------------------------------------------------------------

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-xl bg-surface px-3 py-3">
      <dd className="text-xl font-bold tabular-nums">{value}</dd>
      <dt className="text-xs text-fg2">{label}</dt>
    </div>
  );
}

export function SummaryStrip({ data }: { data: StatsSummary }) {
  const t = data.totals;
  return (
    <Section title="Summary">
      <dl className="grid grid-cols-3 gap-2">
        <Stat label="Items read" value={String(t?.items_read ?? 0)} />
        <Stat label="Active time" value={durationLabel(t?.active_seconds ?? 0)} />
        <Stat label="Days with reading" value={String(t?.days_active ?? 0)} />
      </dl>
    </Section>
  );
}

// ---- 2. Daily activity ------------------------------------------------------------------------------------------

const BAR_W = 10;
const PAD = 8;
const CHART_H = 120;

export function DailyChart({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const daily = data.daily ?? [];
  if (empty || daily.length === 0) return <Empty>No reading in this range yet. Bars appear here as you read.</Empty>;
  const max = Math.max(1, ...daily.map((d) => d.items_read));
  const total = daily.reduce((a, d) => a + d.items_read, 0);
  const busiest = daily.reduce((a, d) => (d.items_read > a.items_read ? d : a), daily[0]!);
  const summary = `Items read per day, ${shortDate(daily[0]!.date)} to ${shortDate(daily[daily.length - 1]!.date)}. ${plural(total, "item")} in total; the most in one day was ${busiest.items_read} on ${shortDate(busiest.date)}.`;
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
      <p className="mt-1 text-xs text-fg2">Most in a day: {busiest.items_read}. Tallest bar: {max} {max === 1 ? "item" : "items"}.</p>
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

// ---- 3. Streaks -------------------------------------------------------------------------------------------------

export function Streaks({ data }: { data: StatsSummary }) {
  const s = data.streaks;
  if (!s || (s.current === 0 && s.longest === 0)) return <Empty>No streak yet. A streak counts consecutive days with something read.</Empty>;
  return (
    <dl className="grid grid-cols-2 gap-2">
      <Stat label="Current streak" value={plural(s.current, "day")} />
      <Stat label={s.longest_end ? `Longest streak, ended ${shortDate(s.longest_end)}` : "Longest streak"} value={plural(s.longest, "day")} />
    </dl>
  );
}

// ---- 4. Heatmap -------------------------------------------------------------------------------------------------

const LEVEL_MIX = [0, 22, 42, 66, 90] as const;
const cellFill = (level: number) =>
  level === 0 ? "var(--color-surface)" : `color-mix(in srgb, var(--color-accent) ${LEVEL_MIX[level]}%, var(--color-surface))`;

export function Heatmap({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const cells = data.heatmap ?? [];
  if (empty || cells.length === 0) return <Empty>The heatmap fills in once there is some reading to place on it.</Empty>;
  const useTime = cells.some((c) => c.active_seconds > 0);
  const val = (c: { active_seconds: number; opens: number }) => (useTime ? c.active_seconds : c.opens);
  const by = new Map(cells.map((c) => [`${c.weekday}:${c.hour}`, c]));
  const max = Math.max(0, ...cells.map(val));
  const hours = Array.from({ length: 24 }, (_, h) => h);
  const days = weekOrder(data.week_start);
  return (
    <div>
      <p className="mb-2 text-xs text-fg2">{useTime ? "Active reading time by weekday and hour." : "Articles opened by weekday and hour."}</p>
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
                  const label = `${weekdayName(wd)} ${hourLabel(h)}: ${v > 0 ? (useTime ? durationLabel(v) : plural(v, "open")) : "none"}`;
                  return (
                    <td
                      key={h}
                      title={label}
                      aria-label={label}
                      data-level={heatLevel(v, max)}
                      className="h-5 rounded-sm border border-line p-0"
                      style={{ background: cellFill(heatLevel(v, max)) }}
                    />
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
          <span key={l} className="inline-block size-3 rounded-sm border border-line" style={{ background: cellFill(l) }} />
        ))}
        More
      </div>
    </div>
  );
}

// ---- 5. Behavior facts ------------------------------------------------------------------------------------------

export function Behavior({ data, empty }: { data: StatsSummary; empty: boolean }) {
  const b = data.behavior;
  if (empty || !b) return <Empty>Observations show up after a few articles have been read.</Empty>;
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
  if (facts.length === 0) return <Empty>Observations show up after a few articles have been read.</Empty>;
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

export function Sources({ data }: { data: StatsSummary }) {
  const [metric, setMetric] = useState<SourceMetric>("items");
  const [by, setBy] = useState<"feeds" | "folders">("feeds");
  const [all, setAll] = useState(false);
  const wide = useMedia("(min-width: 640px)");
  const sources = data.sources;
  const rows = useMemo(() => {
    const base = by === "feeds" ? feedRows(sources ?? []) : folderRows(sources ?? []);
    return sortRows(base, metric).filter((r) => r.items_read > 0 || r.active_seconds > 0 || r.opens > 0 || r.stars > 0);
  }, [sources, metric, by]);
  if (rows.length === 0 && (sources?.length ?? 0) === 0) return <Empty>Sources are listed here once you have read something.</Empty>;
  const val = (r: SourceRow) => (metric === "items" ? r.items_read : r.active_seconds);
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
      {starred.length > 0 ? (
        <p className="text-sm text-fg2">Most starred: {starred.map((r) => `${r.name} (${r.stars})`).join(", ")}.</p>
      ) : null}
      {wide ? (
      <div>
        <table className="w-full text-left text-sm">
          <caption className="sr-only">
            {by === "feeds" ? "Feeds" : "Folders"} by {metric === "items" ? "items read" : "reading time"}
          </caption>
          <thead className="text-xs text-fg2">
            <tr className="border-b border-line">
              <th scope="col" className="py-2 pr-2 font-medium">
                {by === "feeds" ? "Feed" : "Folder"}
              </th>
              <th scope="col" className="px-2 py-2 text-right font-medium">
                Avg read
              </th>
              <th scope="col" className="px-2 py-2 text-right font-medium">
                Quick bounce
              </th>
              <th scope="col" className="px-2 py-2 text-right font-medium">
                Opened original
              </th>
              <th scope="col" className="py-2 pl-2 text-right font-medium">
                Stars
              </th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.key} className="border-b border-line align-top">
                <th scope="row" className="min-w-40 py-2 pr-2 text-left font-normal">
                  <div className="flex items-baseline justify-between gap-2">
                    <span className="min-w-0 truncate font-medium">
                      {r.name}
                      {by === "feeds" && !r.subscribed ? <span className="font-normal text-fg2"> (unsubscribed)</span> : null}
                      {by === "folders" ? <span className="font-normal text-fg2"> ({plural(r.count, "feed")})</span> : null}
                    </span>
                    <span className="shrink-0 tabular-nums">{metric === "items" ? r.items_read : durationLabel(r.active_seconds)}</span>
                  </div>
                  <div aria-hidden="true" className="mt-1 h-1.5 rounded-full bg-surface">
                    <div className="h-full rounded-full bg-accent" style={{ width: `${Math.max(2, (val(r) / max) * 100)}%`, opacity: val(r) > 0 ? 1 : 0 }} />
                  </div>
                </th>
                <td className="px-2 py-2 text-right tabular-nums">{durationLabel(r.avg_read_seconds)}</td>
                <td className="px-2 py-2 text-right tabular-nums">{pctLabel(r.bounce_rate)}</td>
                <td className="px-2 py-2 text-right tabular-nums">{pctLabel(r.open_original_rate)}</td>
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
            <div className="flex items-baseline justify-between gap-2">
              <span className="min-w-0 truncate font-medium">
                {r.name}
                {by === "feeds" && !r.subscribed ? <span className="font-normal text-fg2"> (unsubscribed)</span> : null}
                {by === "folders" ? <span className="font-normal text-fg2"> ({plural(r.count, "feed")})</span> : null}
              </span>
              <span className="shrink-0 tabular-nums">{metric === "items" ? r.items_read : durationLabel(r.active_seconds)}</span>
            </div>
            <div aria-hidden="true" className="mt-1 h-1.5 rounded-full bg-surface">
              <div className="h-full rounded-full bg-accent" style={{ width: `${Math.max(2, (val(r) / max) * 100)}%`, opacity: val(r) > 0 ? 1 : 0 }} />
            </div>
            <p className="mt-1 flex flex-wrap gap-x-3 text-xs text-fg2">
              <span>Avg read {durationLabel(r.avg_read_seconds)}</span>
              <span>Quick bounce {pctLabel(r.bounce_rate)}</span>
              <span>Opened original {pctLabel(r.open_original_rate)}</span>
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
  if (list.length === 0) return <Empty>No feeds to list here. Every subscribed feed has had an article opened.</Empty>;
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

// ---- Screen -----------------------------------------------------------------------------------------------------

export function StatsScreen() {
  const on = useStatsEnabled();
  const [range, setRange] = useState<StatsRange>(loadRange);
  const q = useStatsSummary(range, on);
  const data = q.data;
  const pick = (r: StatsRange) => {
    setRange(r);
    saveRange(r);
  };

  let body: ReactNode;
  if (!on || (data && !data.enabled)) {
    body = (
      <div className="py-8 text-center" role="status">
        <p className="text-sm text-fg2">
          Statistics are off. Turn them on in{" "}
          <Link to="/settings" className="text-link underline underline-offset-2">
            Settings &gt; Statistics
          </Link>
          .
        </p>
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
    const days = t?.days_active ?? 0;
    body = (
      <div aria-busy={q.isPlaceholderData} className={q.isPlaceholderData ? "opacity-60" : undefined}>
        {range !== "all" && days > 0 && days < 7 ? (
          <p role="status" className="mt-3 rounded-xl bg-surface px-3 py-2 text-sm text-fg2">
            Only {plural(days, "day")} of reading so far; charts fill in as you read.
          </p>
        ) : null}
        <SummaryStrip data={data} />
        <Section title="Daily activity">
          <DailyChart data={data} empty={empty} />
        </Section>
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
          <Sources data={data} />
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
        {on ? (
          <Segmented
            legend="Range"
            value={range}
            onChange={pick}
            options={RANGES}
          />
        ) : null}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-6">{body}</div>
    </div>
  );
}
