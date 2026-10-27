import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useWrappedSummary } from "@/api/stats";
import { READ_RULE, durationLabel, hourLabel, plural, todayString, weekdayName } from "@/lib/statsFormat";
import {
  CARD_H,
  CARD_W,
  DEFAULT_OPTIONS,
  buildWrappedCard,
  currentYear,
  deriveWrapped,
  hoursLabel,
  isLowData,
  monthName,
  summaryMatchesYear,
  useWrappedState,
  wrappedText,
  yearRange,
  yearSpan,
  type WrappedModel,
  type WrappedOptions,
} from "@/lib/wrapped";
import { CARD_TOKENS, canRenderImage, copyWrappedText, downloadBlob, renderCardPng, shareWrappedBlob } from "@/lib/wrappedShare";
import { canNativeShare } from "@/lib/share";
import { useNarrowText } from "@/lib/useNarrowText";
import { useStore } from "@/lib/store";
import { activeThemeStore } from "@/theme/theme";
import { toast } from "@/shell/toasts";
import { BackButton } from "@/ui/BackButton";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { FailedBlock } from "@/ui/FailedBlock";
import { TitleRow } from "@/ui/TitleRow";
import { Checkbox, Modal, Notice, Skeleton, inputCls } from "@/ui/kit";

function Card({ title, children, hero = false }: { title: string; children: ReactNode; hero?: boolean }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className={`wrapped-card rounded-2xl bg-surface px-5 ${hero ? "py-8" : "py-6"}`}>
      <h2 id={id} className="text-sm font-medium text-fg2">
        {title}
      </h2>
      <div className="mt-2">{children}</div>
    </section>
  );
}

/** The card's value alone; its title sits above, so nothing is said twice. */
const Big = ({ children, hero = false }: { children: ReactNode; hero?: boolean }) => (
  <p className={`${hero ? "text-6xl text-accent" : "text-4xl"} leading-tight font-bold tabular-nums`}>{children}</p>
);
const Sub = ({ children }: { children: ReactNode }) => <p className="mt-1 text-base text-fg2">{children}</p>;

/**
 * The year as cards, in the order of the share image: articles read, active reading, days with reading, then (from a
 * week of reading on) the longest streak and the busiest day, hour and month, then the top sources and the longest read.
 */
export function WrappedCards({ m }: { m: WrappedModel }) {
  const low = isLowData(m);
  return (
    <div className="mx-auto flex max-w-xl flex-col gap-4">
      <Card title={`Articles read in ${m.year}`} hero>
        <Big hero>{m.itemsRead}</Big>
        {m.avgReadSeconds != null ? <Sub>An average read took {durationLabel(m.avgReadSeconds)}.</Sub> : null}
      </Card>
      <Card title="Active reading">
        <Big>{m.activeSeconds > 0 ? hoursLabel(m.activeSeconds) : "Not recorded"}</Big>
      </Card>
      <Card title="Days with reading">
        <Big>{m.daysActive}</Big>
      </Card>
      {!low && m.longestStreak > 0 ? (
        <Card title="Longest streak">
          <Big>{plural(m.longestStreak, "day")}</Big>
        </Card>
      ) : null}
      {!low && m.busiestWeekday != null ? (
        <Card title="Busiest day">
          <Big>{weekdayName(m.busiestWeekday)}</Big>
        </Card>
      ) : null}
      {!low && m.busiestHour != null ? (
        <Card title="Busiest hour">
          <Big>{hourLabel(m.busiestHour)}</Big>
        </Card>
      ) : null}
      {!low && m.busiestMonth != null ? (
        <Card title="Busiest month">
          <Big>{monthName(m.busiestMonth)}</Big>
        </Card>
      ) : null}
      {m.topSources.length > 0 ? (
        <Card title="Top sources">
          <ol className="flex flex-col gap-1 text-lg">
            {m.topSources.map((s) => (
              <li key={s.feedId} className="flex items-baseline justify-between gap-3">
                <span className="min-w-0 font-medium [overflow-wrap:anywhere]">{s.name}</span>
                <span className="shrink-0 tabular-nums text-fg2">{s.items}</span>
              </li>
            ))}
          </ol>
        </Card>
      ) : null}
      {m.longestRead ? (
        <Card title="Longest read">
          <p className="line-clamp-2 text-2xl leading-snug font-bold [overflow-wrap:anywhere]">{m.longestRead.title}</p>
          <Sub>
            {m.longestRead.feed}, {durationLabel(m.longestRead.seconds)}
          </Sub>
        </Card>
      ) : null}
      {low ? (
        <p className="rounded-xl bg-surface px-3 py-2 text-sm text-fg2">
          Only {plural(m.historyDays, "day")} of reading in {m.year} so far. Your busiest day, hour and month and your longest streak appear after a week of reading, and then you can share your year.
        </p>
      ) : null}
    </div>
  );
}

/** The share card drawn as SVG from the same instruction list the PNG uses: what you see is what is shared. */
export function CardPreview({ m, o }: { m: WrappedModel; o: WrappedOptions }) {
  const ops = useMemo(() => buildWrappedCard(m, o), [m, o]);
  return (
    <svg
      role="img"
      aria-label={wrappedText(m, o).replace(/\n/g, " ")}
      viewBox={`0 0 ${CARD_W} ${CARD_H}`}
      className="mx-auto block max-h-[min(45dvh,32rem)] w-auto max-w-full rounded-xl border border-line"
      data-testid="wrapped-preview"
    >
      {ops.map((op, i) =>
        op.kind === "rect" ? (
          <rect key={i} x={op.x} y={op.y} width={op.w} height={op.h} rx={op.radius} fill={`var(${CARD_TOKENS[op.color]})`} />
        ) : (
          <text key={i} x={op.x} y={op.y} fontSize={op.size} fontWeight={op.weight} fill={`var(${CARD_TOKENS[op.color]})`}>
            {op.text}
          </text>
        ),
      )}
    </svg>
  );
}

type RenderState = "loading" | "ready" | "error";

/** Below this width, counted in text-size-1 pixels, the share buttons stack. */
const STACK_BELOW_PX = 420;

/**
 * Opt-in every time: the dialog opens with the aggregates only.
 *
 * The PNG is rendered as soon as the dialog opens (and again if the options or the theme change), never inside a
 * button handler: iOS treats the click as a user gesture with a short activation window, and awaiting fonts plus a
 * canvas encode there can outlast it, turning `navigator.share` into a silent `NotAllowedError`. Rendering ahead of
 * time means every button just acts on the blob that is already sitting in memory.
 */
function ShareBody({ m, onOpenChange }: { m: WrappedModel; onOpenChange: (o: boolean) => void }) {
  const native = canNativeShare();
  const image = canRenderImage();
  // Stacked until the window, counted in text-size-1 pixels, has room for the row: a media query cannot see the text size.
  const stacked = useNarrowText(STACK_BELOW_PX);
  const [o, setO] = useState<WrappedOptions>(DEFAULT_OPTIONS);
  const [renderState, setRenderState] = useState<RenderState>(() => (image ? "loading" : "ready"));
  const [blob, setBlob] = useState<Blob | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [copyFailed, setCopyFailed] = useState(false);
  const previewRef = useRef<HTMLDivElement>(null);
  // Not read directly: the scheme showing changing (a pick, the OS appearance, a scheduled switch) is the signal to
  // re-render the card in the current theme's colors.
  const theme = useStore(activeThemeStore);
  const text = useMemo(() => wrappedText(m, o), [m, o]);

  useEffect(() => {
    if (!image) return;
    let cancelled = false;
    const go = async () => {
      setRenderState("loading");
      const font = previewRef.current ? getComputedStyle(previewRef.current).fontFamily : undefined;
      const png = await renderCardPng(m, o, font);
      if (cancelled) return;
      setBlob(png);
      setRenderState(png ? "ready" : "error");
    };
    void go();
    return () => {
      cancelled = true;
    };
    // `theme` is a re-render trigger only, not read here — the exhaustive-deps rule is fine with that.
  }, [m, o, image, theme]);

  const preparing = renderState === "loading";

  const doShare = async () => {
    setBusy(true);
    setProblem(null);
    try {
      const r = await shareWrappedBlob(blob, m, o);
      if (r === "failed") setProblem("Couldn't share. Try Copy as text.");
      else if (r === "shared") onOpenChange(false);
      // "cancelled": the sheet was dismissed; nothing to say.
    } finally {
      setBusy(false);
    }
  };

  const doDownload = () => {
    if (!blob) {
      setProblem("Couldn't make the image.");
      return;
    }
    setProblem(null);
    downloadBlob(blob, m.year);
    toast("Image saved");
  };

  const doCopy = async () => {
    setBusy(true);
    setCopyFailed(false);
    try {
      const r = await copyWrappedText(m, o);
      if (r === "failed") setCopyFailed(true);
      else toast("Copied");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onOpenChange={onOpenChange}
      title="Share your year"
      description="Only the summary below is shared."
      footer={
        // The main action first, so the Tab order and the visual order agree: a column until the window has room for one row.
        <div className={`flex w-full gap-2 ${stacked ? "flex-col" : "flex-row justify-end"}`}>
          {image ? (
            <Button variant="solid" disabled={busy || preparing || !blob} onClick={doDownload}>
              Download image
            </Button>
          ) : null}
          {native ? (
            <Button disabled={busy || preparing} onClick={() => void doShare()}>
              Share
            </Button>
          ) : null}
          <Button disabled={busy} onClick={() => void doCopy()}>
            Copy as text
          </Button>
          <Button onClick={() => onOpenChange(false)}>Close</Button>
        </div>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-col gap-1">
          <Checkbox label="Include my top sources" help="Feed names." checked={o.topSources} onChange={(v) => setO((p) => ({ ...p, topSources: v }))} />
          <Checkbox label="Include my longest read" help="The article title." checked={o.longestRead} onChange={(v) => setO((p) => ({ ...p, longestRead: v }))} />
        </div>
        <div ref={previewRef}>
          <CardPreview m={m} o={o} />
        </div>
        {image && preparing ? (
          <p className="text-sm text-fg2" role="status">
            Preparing…
          </p>
        ) : null}
        {image && renderState === "error" ? <Notice tone="error">Couldn't make the image. You can still share or copy the text.</Notice> : null}
        {problem ? <Notice tone="error">{problem}</Notice> : null}
        {copyFailed ? (
          <div className="flex flex-col gap-2">
            <Notice tone="error">Couldn't copy. Select the text below and copy it yourself.</Notice>
            <textarea
              readOnly
              aria-label="Your year, as text"
              value={text}
              onFocus={(e) => e.currentTarget.select()}
              rows={6}
              className={`${inputCls} font-mono text-xs`}
            />
          </div>
        ) : null}
      </div>
    </Modal>
  );
}

export function WrappedShareDialog({ m, open, onOpenChange }: { m: WrappedModel; open: boolean; onOpenChange: (o: boolean) => void }) {
  return open ? <ShareBody m={m} onOpenChange={onOpenChange} /> : null;
}

export function WrappedScreen() {
  useDocumentTitle("Your year");
  const state = useWrappedState();
  const today = todayString();
  const [year, setYear] = useState(currentYear(today));
  const [sharing, setSharing] = useState(false);
  const headingId = useId();
  const on = state === "on";
  const q = useWrappedSummary(year, yearSpan(year, today), on);
  const data = q.data;
  // With `keepPreviousData`, `data` can still be the previously selected year's response while this year's request
  // is in flight — never derive a model (or enable Share) from that until the response actually matches `year`.
  const dataForYear = data && summaryMatchesYear(data, year) ? data : undefined;
  const years = yearRange(data?.first_event_date, today);
  const model = dataForYear?.enabled ? deriveWrapped(dataForYear, year, today) : null;
  const low = model != null && isLowData(model);

  let body: ReactNode;
  if (state === "unknown") {
    body = <Skeleton rows={4} label="Loading your year" />;
  } else if (!on || (dataForYear && !dataForYear.enabled)) {
    body = (
      <EmptyState
        title="Your year is off"
        action={
          <Link to="/settings/statistics" className="text-link underline underline-offset-2">
            Turn it on in Settings &gt; Stats
          </Link>
        }
      />
    );
  } else if (q.isError && !dataForYear) {
    // Before the skeleton: a failed request (or a failed switch to another year, where `data` may still be the old
    // year's) never becomes this year's data, so the skeleton would stay up for good with no way to retry.
    body = <FailedBlock what="Your year" error={q.error} busy={q.isFetching} onRetry={() => void q.refetch()} />;
  } else if (q.isPending || !dataForYear) {
    body = <Skeleton rows={4} label="Loading your year" />;
  } else if (model) {
    body = (
      <div aria-busy={q.isPlaceholderData} className={q.isPlaceholderData ? "opacity-60" : undefined}>
        {model.empty ? (
          <EmptyState title={`Nothing read in ${model.year} yet`} why={READ_RULE} action={null} />
        ) : (
          <>
            <WrappedCards m={model} />
          </>
        )}
      </div>
    );
  }

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-3">
        <TitleRow title="Your year" id={headingId} leading={<BackButton to="/stats" ariaLabel="Back to Stats" />} className="gap-x-3">
          {on ? (
            <>
              <label className="flex items-center gap-2 text-sm">
                <span className="text-fg2">Year</span>
                <select className={inputCls} value={year} onChange={(e) => setYear(Number(e.target.value))}>
                  {[...new Set([...years, year])].sort((a, b) => b - a).map((y) => (
                    <option key={y} value={y}>
                      {y}
                    </option>
                  ))}
                </select>
              </label>
              {/* Nothing read this year, or under a week of it: there is no summary worth sending. */}
              {model && !model.empty && !low ? <Button onClick={() => setSharing(true)}>Share</Button> : null}
            </>
          ) : null}
        </TitleRow>
      </header>
      {/* The cards (and an empty year's cards) hold nothing focusable, so with a model the region itself takes focus and
          a keyboard can scroll it. The other states have their own link or button, or nothing to scroll. */}
      <div
        className="kp-scroll-region min-h-0 flex-1 overflow-y-auto px-4 py-4"
        tabIndex={model ? 0 : undefined}
        role="region"
        aria-labelledby={headingId}
      >
        {body}
      </div>
      {model ? <WrappedShareDialog m={model} open={sharing} onOpenChange={setSharing} /> : null}
    </div>
  );
}
