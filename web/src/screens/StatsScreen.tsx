import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useId, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { ChevronDown, ChevronRight } from "lucide-react";
import { useSpanSummary, useStatsSummary } from "@/api/stats";
import type { StatsSummary } from "@/api/types";
import {
  dateWithYear,
  daysBetween,
  durationLabel,
  feedRows,
  folderRows,
  hourLabel,
  loadScreenRange,
  apiRange,
  SCREEN_RANGES,
  changeLabel,
  previousPeriod,
  type ScreenRange,
  mostStarred,
  READ_RULE,
  pctLabel,
  plural,
  readRateNote,
  saveRange,
  shortDate,
  sortRows,
  todayString,
  weekdayName,
  type SourceMetric,
  type SourceRow,
} from "@/lib/statsFormat";
import { articleTo } from "@/lib/routes";
import { useStatsEnabled } from "@/lib/statsSender";
import { useNarrowText } from "@/lib/useNarrowText";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { FailedBlock } from "@/ui/FailedBlock";
import { Modal, Notice, Section, Skeleton, inputCls } from "@/ui/kit";
import { Segmented } from "@/ui/segmented";
import { useWrappedEnabled } from "@/lib/wrapped";
import { DailyChart, Empty, Heatmap, MonthlyChart } from "./StatsCharts";
import { StatsDataSection, StatsExportDialog } from "./StatsDataDialogs";

export { DailyChart, Heatmap, MonthlyChart } from "./StatsCharts";

/** Under this width, counted in text-size-1 pixels (so 1.5x text on a 375 px phone is 250), the Range is a select and the tiles stack. */
const NARROW_TEXT_PX = 360;

// ---- 1. Summary -------------------------------------------------------------------------------------------------

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
      {/* The value shrinks to the room the tile has (its width less the padding and the chevron), so "12h 40m" never runs under the chevron or wraps. */}
      <span
        className="block text-2xl font-bold whitespace-nowrap tabular-nums"
        style={{ fontSize: `min(1.5rem, calc((100cqw - 2.5rem) / ${(Math.max(value.length, 3) * 0.68).toFixed(2)}))` }}
      >
        {value}
      </span>
      <span className="block text-xs text-fg2">{label}</span>
      {note ? <span className="mt-1 block text-xs font-medium tabular-nums">{note}</span> : null}
    </>
  );
  return (
    <div className="@container min-w-0 rounded-xl bg-surface">
      {onToggle ? (
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={note != null}
          aria-describedby={note != null ? describedBy : undefined}
          className="hit relative block min-h-11 w-full min-w-0 rounded-xl py-3 pl-3 pr-7 text-left hover:bg-selection active:bg-selection"
        >
          {inner}
          <ChevronDown aria-hidden="true" data-cue="tile" className={`absolute right-2 top-3.5 size-4 text-fg2 transition-transform ${note != null ? "rotate-180" : ""}`} />
        </button>
      ) : (
        <div className="px-3 py-3">{inner}</div>
      )}
    </div>
  );
}

type Tile = "items" | "time" | "days";

export function SummaryStrip({ data, compare = false }: { data: StatsSummary; compare?: boolean }) {
  return (
    <Section title="Summary">
      <SummaryTiles data={data} compare={compare} streaks />
    </Section>
  );
}

/** Current and longest streak, as two tiles; nothing when there has been none. */
function StreakTiles({ data }: { data: StatsSummary }) {
  const s = data.streaks;
  if (!s || (s.current === 0 && s.longest === 0)) return null;
  const today = data.range?.to ?? todayString();
  const running = s.current > 0 && (s.current >= s.longest || (s.longest_end != null && daysBetween(s.longest_end, today) <= 1));
  const longestLabel = running ? "Longest streak, still going" : s.longest_end ? `Longest streak, ended ${shortDate(s.longest_end)}` : "Longest streak";
  return (
    <div className="mt-2 grid grid-cols-2 gap-2">
      <Stat label="Current streak" value={plural(s.current, "day")} />
      <Stat label={longestLabel} value={plural(s.longest, "day")} />
    </div>
  );
}

/**
 * The three summary tiles of `data`, each comparable on a tap, then (with `streaks`) the streaks, and what counts as a
 * read. With `feed`, the earlier period is that feed's too.
 */
export function SummaryTiles({ data, compare = false, feed, streaks = false }: { data: StatsSummary; compare?: boolean; feed?: string; streaks?: boolean }) {
  const t = data.totals;
  const legacy = t?.legacy_opens ?? 0;
  const noteId = useId();
  const narrow = useNarrowText(NARROW_TEXT_PX);
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
      <div className={`grid gap-2 ${narrow ? "grid-cols-1" : "grid-cols-3"}`}>
        {tile("items", "Articles read", t?.items_read ?? 0, cur?.items_read, prev?.items_read, num)}
        {tile("time", "Active time", t?.active_seconds ?? 0, cur?.active_seconds, prev?.active_seconds, durationLabel)}
        {tile("days", "Reading days", t?.days_active ?? 0, cur?.days_active, prev?.days_active, num)}
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
      {streaks ? <StreakTiles data={data} /> : null}
      <p className="mt-2 text-xs text-fg2">
        {READ_RULE}
        {legacy > 0
          ? ` ${plural(legacy, "article")} ${legacy === 1 ? "was" : "were"} read before reading time was recorded. ${legacy === 1 ? "It counts" : "They count"} as read but add${legacy === 1 ? "s" : ""} no time.`
          : ""}
      </p>
    </>
  );
}

// ---- 2. Reading habits ------------------------------------------------------------------------------------------

/**
 * Label and value pairs; the longest read is a row that opens the article. `busiest` adds the busiest day and hour,
 * which the screen's heatmap already answers and leaves out.
 */
export function Behavior({ data, empty, busiest = true }: { data: StatsSummary; empty: boolean; busiest?: boolean }) {
  const b = data.behavior;
  if (empty || !b) return <Empty>No reading in this range.</Empty>;
  const amount = (x: { active_seconds: number; opens: number }) => (x.active_seconds > 0 ? durationLabel(x.active_seconds) : plural(x.opens, "article"));
  const rows: { label: string; value: ReactNode; sub?: string }[] = [];
  if (busiest && b.busiest_weekday) rows.push({ label: "Busiest day", value: weekdayName(b.busiest_weekday.weekday), sub: amount(b.busiest_weekday) });
  if (busiest && b.busiest_hour) rows.push({ label: "Busiest hour", value: hourLabel(b.busiest_hour.hour), sub: amount(b.busiest_hour) });
  if (b.avg_read_seconds != null) rows.push({ label: "Average read", value: durationLabel(b.avg_read_seconds) });
  if (b.longest_read) {
    const l = b.longest_read;
    rows.push({
      label: "Longest read",
      value: (
        <Link to={articleTo(l.item_id, { view: "all" })} className="line-clamp-2 text-link underline underline-offset-2">
          {l.title}
        </Link>
      ),
      sub: `${l.feed_title}, ${durationLabel(l.seconds)}, ${shortDate(l.date)}`,
    });
  }
  if (rows.length === 0) return <Empty>No reading in this range.</Empty>;
  return (
    <dl className="flex flex-col gap-2 text-sm">
      {rows.map((r) => (
        <div key={r.label} className="rounded-xl bg-surface px-3 py-2">
          <dt className="text-xs text-fg2">{r.label}</dt>
          <dd className="font-medium">{r.value}</dd>
          {r.sub ? <dd className="text-xs text-fg2">{r.sub}</dd> : null}
        </div>
      ))}
    </dl>
  );
}

// ---- 3. Sources -------------------------------------------------------------------------------------------------

const SHOWN = 25;

/** A feed added only a day or two ago has had no time to be opened: "Never opened" waits this many days. */
const NEVER_OPENED_AFTER_DAYS = 3;

/** A source's name (up to two lines), its amount with the unit, and the bar; for a feed, a button that opens its sheet. */
function SourceRowItem({ r, by, amount, share, onOpen }: { r: SourceRow; by: "feeds" | "folders"; amount: string; share: number; onOpen?: (r: SourceRow) => void }) {
  const head = (
    <>
      <span className="min-w-0 break-words font-medium [overflow-wrap:anywhere] line-clamp-2">
        {r.name}
        {by === "feeds" && !r.subscribed ? <span className="whitespace-pre font-normal text-fg2"> (unsubscribed)</span> : null}
        {by === "folders" ? <span className="whitespace-pre font-normal text-fg2"> ({plural(r.count, "feed")})</span> : null}
      </span>
      <span className="ml-auto shrink-0 tabular-nums">{amount}</span>
    </>
  );
  return (
    <li className="py-2 text-sm">
      {by === "feeds" && onOpen ? (
        <button type="button" aria-haspopup="dialog" onClick={() => onOpen(r)} className="flex min-h-11 w-full min-w-0 items-center gap-2 rounded text-left hover:text-link">
          {head}
          <ChevronRight aria-hidden="true" className="size-4 shrink-0 text-fg2" />
        </button>
      ) : (
        <div className="flex items-baseline gap-2">{head}</div>
      )}
      <div aria-hidden="true" className="mt-1 h-1.5 rounded-full bg-surface">
        <div className="h-full rounded-full bg-accent" style={{ width: `${Math.max(2, share * 100)}%`, opacity: share > 0 ? 1 : 0 }} />
      </div>
    </li>
  );
}

export function Sources({ data, onOpen }: { data: StatsSummary; onOpen?: (r: SourceRow) => void }) {
  const [metric, setMetric] = useState<SourceMetric>("items");
  const [by, setBy] = useState<"feeds" | "folders">("feeds");
  const [all, setAll] = useState(false);
  const sources = data.sources;
  const rows = useMemo(() => {
    const base = by === "feeds" ? feedRows(sources ?? []) : folderRows(sources ?? []);
    return sortRows(base, metric).filter((r) => r.items_read > 0 || r.active_seconds > 0 || r.opens > 0 || r.stars > 0);
  }, [sources, metric, by]);
  if (rows.length === 0 && (sources?.length ?? 0) === 0) return <Empty>No reading in this range.</Empty>;
  const val = (r: SourceRow) => (metric === "items" ? r.items_read : r.active_seconds);
  const amount = (r: SourceRow) => (metric === "items" ? plural(r.items_read, "article") : durationLabel(r.active_seconds));
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
            { value: "items", label: "Articles" },
            { value: "minutes", label: "Time" },
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
      <ul aria-label={`${by === "feeds" ? "Feeds" : "Folders"} by ${metric === "items" ? "articles read" : "reading time"}`} className="flex flex-col divide-y divide-line">
        {shown.map((r) => (
          <SourceRowItem key={r.key} r={r} by={by} amount={amount(r)} share={val(r) / max} onOpen={onOpen} />
        ))}
      </ul>
      {rows.length > SHOWN ? (
        <Button onClick={() => setAll((a) => !a)}>
          {all ? `Show the top ${SHOWN}` : `Show all ${rows.length}`}
        </Button>
      ) : null}
    </div>
  );
}

/** Feeds with no article opened yet, once they are a few days old. */
export function NeverOpened({ data }: { data: StatsSummary }) {
  const today = data.range?.to ?? todayString();
  const list = (data.never_opened ?? []).filter((f) => daysBetween(f.subscribed_on, today) >= NEVER_OPENED_AFTER_DAYS);
  if (list.length === 0) return null;
  return (
    <Section sub title="Never opened">
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
    </Section>
  );
}

// ---- 4. One feed ------------------------------------------------------------------------------------------------

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

/** The feed's read rate as a percentage (or "Not enough data yet"); a tap shows the counts behind it. */
function ReadRateTerm({ data }: { data: StatsSummary }) {
  const [shown, setShown] = useState(false);
  const noteId = useId();
  const r = data.read_rate;
  const pct = pctLabel(r?.rate);
  return (
    <div className="hit relative rounded-xl bg-surface px-3 py-2 has-[button:hover]:bg-selection has-[button:active]:bg-selection">
      <dt className="text-xs text-fg2">Read rate</dt>
      <dd>
        {/* The name stays "Read rate 25%" whether or not the counts are showing; they are the button's controlled region. */}
        <button
          type="button"
          aria-label={`Read rate ${pct}`}
          aria-expanded={shown}
          aria-controls={noteId}
          onClick={() => setShown((s) => !s)}
          className="block pr-6 text-left text-base font-bold tabular-nums after:absolute after:inset-0 after:rounded-xl"
        >
          {pct}
          <ChevronDown aria-hidden="true" data-cue="tile" className={`absolute right-2 top-2.5 size-4 text-fg2 transition-transform ${shown ? "rotate-180" : ""}`} />
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
  if (!data) return <FailedBlock error={q.error} busy={q.isFetching} onRetry={() => void q.refetch()} />;
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
              ["Articles opened", String(t?.opens ?? 0)],
              ["Average read", durationLabel(s?.avg_read_seconds)],
              ["Skimmed", pctLabel(s?.bounce_rate)],
              ["Went to the original", pctLabel(s?.open_original_rate)],
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
  useDocumentTitle("Stats");
  const on = useStatsEnabled();
  const wrapped = useWrappedEnabled();
  const [range, setRange] = useState<ScreenRange>(loadScreenRange);
  const q = useStatsSummary(apiRange(range), on);
  const data = q.data;
  const [exporting, setExporting] = useState(false);
  const [feed, setFeed] = useState<SourceRow | null>(null);
  const narrow = useNarrowText(NARROW_TEXT_PX);
  const pick = (r: ScreenRange) => {
    setRange(r);
    saveRange(r);
  };

  let body: ReactNode;
  if (!on || (data && !data.enabled)) {
    body = (
      <div className="py-8">
        <EmptyState
          title="Statistics are off"
          why="Kipple isn't recording what you read, so there is nothing to show."
          action={
            <Link to="/settings/statistics" className="text-link underline underline-offset-2">
              Turn them on in Settings &gt; Stats
            </Link>
          }
        />
        <div className="mt-6 text-left">
          <StatsDataSection defaultRange={apiRange(range)} />
        </div>
      </div>
    );
  } else if (q.isPending) {
    body = <Skeleton rows={5} label="Loading statistics" />;
  } else if (q.isError && !data) {
    body = <FailedBlock error={q.error} busy={q.isFetching} onRetry={() => void q.refetch()} />;
  } else if (data) {
    const t = data.totals;
    const empty = !t || (t.items_read === 0 && t.opens === 0 && t.active_seconds === 0);
    const today = data.range?.to ?? todayString();
    const history = data.first_event_date ? daysBetween(data.first_event_date, today) + 1 : null;
    // Nothing has ever been read: one screen-level explanation instead of a page of zeros and dashed boxes.
    const never = empty && !data.first_event_date;
    const refreshFailed = q.isError ? (
      <div className="mt-3">
        <Notice tone="warn">
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            Couldn't refresh; showing earlier numbers.
            <Button onClick={() => void q.refetch()}>Try again</Button>
          </span>
        </Notice>
      </div>
    ) : null;
    body = never ? (
      <div aria-busy={q.isPlaceholderData}>
        {refreshFailed}
        <EmptyState
          title="Nothing read yet"
          why={`Your reading shows up here as you go. ${READ_RULE}`}
          action={null}
        />
      </div>
    ) : (
      <div aria-busy={q.isPlaceholderData} className={q.isPlaceholderData ? "opacity-60" : undefined}>
        {refreshFailed}
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
        {apiRange(range) !== "all" && !q.isPlaceholderData && history != null && history >= 1 && history < 7 && (t?.days_active ?? 0) > 0 ? (
          <p className="mt-3 rounded-xl bg-surface px-3 py-2 text-sm text-fg2">
            Only {plural(history, "day")} of reading so far.
          </p>
        ) : null}
        <SummaryStrip key={`${data.range?.key}-${data.range?.from}`} data={data} compare />
        {empty ? (
          <EmptyState title="Nothing read in this range" why={READ_RULE} action={null} />
        ) : (
          <>
            {range === "months" ? (
              <Section title="Monthly activity">
                <MonthlyChart data={data} empty={empty} />
              </Section>
            ) : (
              <Section title="Daily activity">
                <DailyChart data={data} empty={empty} />
              </Section>
            )}
            <Section title="When you read">
              <Heatmap data={data} empty={empty} />
            </Section>
            <Section title="Reading habits">
              <Behavior data={data} empty={empty} busiest={false} />
            </Section>
            <Section title="Sources" stack>
              <Sources data={data} onOpen={setFeed} />
              <NeverOpened data={data} />
            </Section>
          </>
        )}
        <Section title="Your data">
          <Button onClick={() => setExporting(true)}>Export</Button>
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
          narrow ? (
            <label className="flex items-center gap-2 text-sm">
              <span className="text-fg2">Range</span>
              <select className={`${inputCls} min-w-0 flex-1`} value={range} onChange={(e) => pick(e.target.value as ScreenRange)}>
                {SCREEN_RANGES.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </select>
            </label>
          ) : (
            <Segmented legend="Range" value={range} onChange={pick} options={SCREEN_RANGES} />
          )
        ) : null}
      </header>
      <div className="relative min-h-0 flex-1 overflow-y-auto px-4 pb-6">{body}</div>
      <StatsExportDialog open={exporting} onOpenChange={setExporting} defaultRange={apiRange(range)} />
      <FeedSheet feed={feed} range={range} onClose={() => setFeed(null)} />
    </div>
  );
}
