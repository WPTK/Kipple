import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useWrappedSummary } from "@/api/stats";
import { errorMessage } from "@/api/client";
import { durationLabel, hourLabel, plural, todayString, weekdayName } from "@/lib/statsFormat";
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
import { useStore } from "@/lib/store";
import { themeStore } from "@/theme/theme";
import { toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton, Switch, inputCls } from "@/ui/kit";

function Card({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="wrapped-card rounded-2xl bg-surface px-5 py-6">
      <h2 id={id} className="text-sm font-medium text-fg2">
        {title}
      </h2>
      <div className="mt-2">{children}</div>
    </section>
  );
}

const Big = ({ children }: { children: ReactNode }) => <p className="text-4xl leading-tight font-bold tabular-nums">{children}</p>;
const Sub = ({ children }: { children: ReactNode }) => <p className="mt-1 text-base text-fg2">{children}</p>;
const Nothing = ({ children }: { children: ReactNode }) => <p className="text-base text-fg2">{children}</p>;

export function WrappedCards({ m }: { m: WrappedModel }) {
  return (
    <div className="flex flex-col gap-4">
      <Card title="Items read">
        {m.itemsRead > 0 ? (
          <>
            <Big>You read {plural(m.itemsRead, "item")} in {m.year}</Big>
            {m.avgReadSeconds != null ? <Sub>An average read took {durationLabel(m.avgReadSeconds)}.</Sub> : null}
          </>
        ) : (
          <Nothing>No items read in {m.year}.</Nothing>
        )}
      </Card>
      <Card title="Active reading">
        {m.activeSeconds > 0 ? <Big>{hoursLabel(m.activeSeconds)} of active reading</Big> : <Nothing>No reading time recorded in {m.year}.</Nothing>}
      </Card>
      <Card title="Days with reading">
        {m.daysActive > 0 ? (
          <>
            <Big>{plural(m.daysActive, "day")} with reading</Big>
            <Sub>Longest streak: {plural(m.longestStreak, "day")}.</Sub>
          </>
        ) : (
          <Nothing>No days with reading in {m.year}.</Nothing>
        )}
      </Card>
      <Card title="When you read">
        {m.busiestWeekday != null || m.busiestHour != null ? (
          <Big>
            {m.busiestWeekday != null ? `Your busiest day was ${weekdayName(m.busiestWeekday)}` : "Your busiest hour"}
            {m.busiestWeekday != null && m.busiestHour != null ? `; your busiest hour ${hourLabel(m.busiestHour)}` : m.busiestHour != null ? ` was ${hourLabel(m.busiestHour)}` : ""}
          </Big>
        ) : (
          <Nothing>Not enough reading to name a busiest day or hour.</Nothing>
        )}
      </Card>
      <Card title="Busiest month">
        {m.busiestMonth != null ? <Big>Busiest month: {monthName(m.busiestMonth)}</Big> : <Nothing>No month stands out yet.</Nothing>}
      </Card>
      <Card title="Top sources">
        {m.topSources.length > 0 ? (
          <ol className="flex flex-col gap-1 text-lg">
            {m.topSources.map((s) => (
              <li key={s.feedId} className="flex items-baseline justify-between gap-3">
                <span className="min-w-0 truncate font-medium">{s.name}</span>
                <span className="shrink-0 tabular-nums text-fg2">{s.items}</span>
              </li>
            ))}
          </ol>
        ) : (
          <Nothing>Sources appear here once you have read something.</Nothing>
        )}
      </Card>
      <Card title="Longest read">
        {m.longestRead ? (
          <>
            <p className="text-2xl leading-snug font-bold">{m.longestRead.title}</p>
            <Sub>
              {m.longestRead.feed}, {durationLabel(m.longestRead.seconds)}
            </Sub>
          </>
        ) : (
          <Nothing>No timed reads yet.</Nothing>
        )}
      </Card>
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
      className="mx-auto block max-h-[45dvh] w-auto max-w-full rounded-xl border border-line"
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
  const [o, setO] = useState<WrappedOptions>(DEFAULT_OPTIONS);
  const [renderState, setRenderState] = useState<RenderState>(() => (image ? "loading" : "ready"));
  const [blob, setBlob] = useState<Blob | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [copyFailed, setCopyFailed] = useState(false);
  const previewRef = useRef<HTMLDivElement>(null);
  // Not read directly: its identity changing is the signal to re-render the card in the current theme's colors.
  const theme = useStore(themeStore);
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
      description="Only the summary below is shared, and only when you choose to. Nothing is sent until then."
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Close</Button>
          {native ? (
            <Button variant="solid" disabled={busy || preparing} onClick={() => void doShare()}>
              Share
            </Button>
          ) : null}
          {image ? (
            <Button disabled={busy || preparing || !blob} onClick={doDownload}>
              Download image
            </Button>
          ) : null}
          <Button disabled={busy} onClick={() => void doCopy()}>
            Copy as text
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <div ref={previewRef}>
          <CardPreview m={m} o={o} />
        </div>
        {image && preparing ? (
          <p className="text-sm text-fg2" role="status">
            Preparing…
          </p>
        ) : null}
        {image && renderState === "error" ? <Notice tone="error">Couldn't make the image. You can still share or copy the text.</Notice> : null}
        <Switch label="Include my top sources" help="Feed names." checked={o.topSources} onChange={(v) => setO((p) => ({ ...p, topSources: v }))} />
        <Switch label="Include my longest read" help="The article title." checked={o.longestRead} onChange={(v) => setO((p) => ({ ...p, longestRead: v }))} />
        {problem ? <Notice tone="error">{problem}</Notice> : null}
        {copyFailed ? (
          <div className="flex flex-col gap-2">
            <Notice tone="error">Couldn't copy. Here's the text — select it and copy by hand.</Notice>
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
  const state = useWrappedState();
  const today = todayString();
  const [year, setYear] = useState(currentYear(today));
  const [sharing, setSharing] = useState(false);
  const on = state === "on";
  const q = useWrappedSummary(year, yearSpan(year, today), on);
  const data = q.data;
  // With `keepPreviousData`, `data` can still be the previously selected year's response while this year's request
  // is in flight — never derive a model (or enable Share) from that until the response actually matches `year`.
  const dataForYear = data && summaryMatchesYear(data, year) ? data : undefined;
  const years = yearRange(data?.first_event_date, today);
  const model = dataForYear?.enabled ? deriveWrapped(dataForYear, year, today) : null;

  let body: ReactNode;
  if (state === "unknown") {
    body = <Skeleton rows={4} label="Loading your year" />;
  } else if (!on || (dataForYear && !dataForYear.enabled)) {
    body = (
      <p className="py-8 text-center text-sm text-fg2" role="status">
        Wrapped is off. Turn it on in{" "}
        <Link to="/settings" className="text-link underline underline-offset-2">
          Settings &gt; Statistics
        </Link>
        .
      </p>
    );
  } else if (q.isPending || !dataForYear) {
    body = <Skeleton rows={4} label="Loading your year" />;
  } else if (q.isError && !data) {
    body = (
      <div role="alert" className="flex flex-col items-start gap-3 py-6">
        <Notice tone="error">{errorMessage(q.error)}</Notice>
        <Button onClick={() => void q.refetch()}>Try again</Button>
      </div>
    );
  } else if (model) {
    body = (
      <div aria-busy={q.isPlaceholderData} className={q.isPlaceholderData ? "opacity-60" : undefined}>
        {isLowData(model) && !model.empty && !q.isPlaceholderData ? (
          <p className="mb-4 rounded-xl bg-surface px-3 py-2 text-sm text-fg2">
            Only {plural(model.historyDays, "day")} of reading in {model.year} so far, so this is a small picture. It fills in as you read.
          </p>
        ) : null}
        {model.empty ? <p className="mb-4 rounded-xl bg-surface px-3 py-2 text-sm text-fg2">Nothing read in {model.year} yet.</p> : null}
        <WrappedCards m={model} />
      </div>
    );
  }

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-3">
        <h1 className="pt-2 pb-2 text-xl font-bold" tabIndex={-1} data-route-heading>
          Your year
        </h1>
        <div className="flex items-end gap-3">
          <Link to="/stats" className="min-h-11 content-center text-sm text-link underline underline-offset-2">
            Back to Stats
          </Link>
          {on ? (
            <>
              <label className="ml-auto flex items-center gap-2 text-sm">
                <span className="text-fg2">Year</span>
                <select className={inputCls} value={year} onChange={(e) => setYear(Number(e.target.value))}>
                  {[...new Set([...years, year])].sort((a, b) => b - a).map((y) => (
                    <option key={y} value={y}>
                      {y}
                    </option>
                  ))}
                </select>
              </label>
              <Button disabled={!model} onClick={() => setSharing(true)}>
                Share
              </Button>
            </>
          ) : null}
        </div>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4">{body}</div>
      {model ? <WrappedShareDialog m={model} open={sharing} onOpenChange={setSharing} /> : null}
    </div>
  );
}
