import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { liveStore, pollStatus, seedRun, type FinishedRun } from "@/api/events";
import { ApiError, errorMessage } from "@/api/client";
import { previewAutoRead, runAutoRead, type AutoReadPreview } from "@/api/autoRead";
import { useStoreSelector } from "@/lib/store";
import { markOlderConfirmLabel, markOlderLabel } from "@/lib/itemActionTable";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice } from "@/ui/kit";

const n = (x: number): string => x.toLocaleString();

/** A preview older than this is counted again before anything is marked. */
const PREVIEW_MAX_AGE_MS = 60_000;
/** The count grew this much (a fraction, or that many articles) since the preview: the person must see the new number. */
const GREW_FRACTION = 0.1;
const GREW_ARTICLES = 100;
const grewTooMuch = (before: number, after: number): boolean => after - before > Math.min(before * GREW_FRACTION, GREW_ARTICLES) && after > before;

/** What a failed preview or run says. `busy` and `confirm_required` have their own wording; everything else is generic. */
function catchUpError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 409 && e.code === "busy") return "Another catch-up is already running. Wait for it to finish, then try again.";
    if (e.status === 404) return "That feed no longer exists.";
    if (e.status === 400) return typeof e.body?.message === "string" ? e.body.message : "That number of days isn't accepted.";
  }
  return errorMessage(e);
}

function finishedNote(fin: FinishedRun | undefined, seen: number): string {
  const exact = fin?.changed !== undefined;
  const changed = fin?.changed ?? seen;
  const marked = changed > 0 ? `${exact ? "Marked" : "Marked at least"} ${n(changed)} older article${changed === 1 ? "" : "s"} as read.` : "Nothing was left to mark.";
  if (fin?.inferred) {
    // A status poll stopped listing the run and no run.done was heard: Kipple does not know whether it finished, so a
    // clean "nothing was left to mark" (or a count with no caveat) would be a guess.
    return `${changed > 0 ? `${marked} ` : ""}The catch-up has ended, but Kipple didn't hear how it went. Preview again to see what is left.`;
  }
  return fin?.error ? `${changed > 0 ? `${marked} ` : ""}The catch-up stopped early because of an error. Preview again to see what is left.` : marked;
}

/**
 * "Preview" and "Mark N older articles as read now" for auto-read (docs/design.md 7.1d). The threshold itself is
 * saved by its own control and never marks anything; this is the explicit, previewed catch-up for what is already
 * older. `days` is a what-if (preview and run use it without saving it); `feedId` limits it to one feed.
 * Above `confirm_above` articles the run asks first, and there is no Undo: the dialog says so.
 */
export function AutoReadCatchUp({
  feedId,
  days,
  autoPreview,
  blockedReason,
  runBlocked,
}: {
  feedId?: string;
  days?: number;
  /** Count as soon as this mounts (inside the dialog the Preview button opens); the button then counts again. */
  autoPreview?: boolean;
  /** Why Preview is unavailable right now. */
  blockedReason?: string;
  /** Why the run button is replaced by this text (the feed editor: a change that is not saved yet). */
  runBlocked?: string;
}) {
  const key = `${feedId ?? ""}|${days ?? ""}`;
  const qc = useQueryClient();
  const [held, setPreview] = useState<{ p: AutoReadPreview; days?: number; key: string; at: number } | null>(null);
  // Set when a fresh count came out much higher than the one the person looked at: they confirm the new number.
  const [grew, setGrew] = useState<{ from: number } | null>(null);
  // Numbers counted for another threshold or feed are not shown.
  const preview = held && held.key === key ? held : null;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  // The run that just ended and the count its last progress showed. The note is worked out from what the live state
  // knows about that run now, so a run.done that arrives after an inferred end replaces the vague note.
  const [over, setOver] = useState<{ id: string; seen: number } | null>(null);
  const fin = useStoreSelector(liveStore, (s) => (over ? s.finished?.[over.id] : undefined));
  const runs = useStoreSelector(liveStore, (s) => s.runs);
  const active = Object.values(runs).find((r) => r.kind === "auto_read");
  const body = { ...(feedId ? { feed_id: feedId } : {}), ...(days !== undefined ? { days } : {}) };


  // A run that finishes: say how many it marked. The last progress event is throttled and can be far behind, so the
  // number comes from the run.done the live store kept for it; without one (a missed event) the last progress is a
  // lower bound.
  const last = useRef(active);
  useEffect(() => {
    const before = last.current;
    last.current = active;
    if (before && !active) {
      setOver({ id: before.id, seen: before.changed ?? before.done });
      setPreview(null);
    }
  }, [active]);
  const note = over ? finishedNote(fin, over.seen) : null;

  const doPreview = async () => {
    setBusy(true);
    setError(null);
    setOver(null);
    try {
      const p = await previewAutoRead(body);
      setGrew(null);
      setPreview({ p, days, key, at: Date.now() });
    } catch (e) {
      setError(catchUpError(e));
    } finally {
      setBusy(false);
    }
  };

  const started = useRef(false);
  useEffect(() => {
    if (!autoPreview || started.current) return;
    started.current = true;
    void doPreview();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const doRun = async (confirmed: boolean, shown: AutoReadPreview) => {
    if (!preview) return;
    setBusy(true);
    setError(null);
    try {
      const r = await runAutoRead({ ...body, expect_total: shown.total, ...(confirmed ? { confirm: true } : {}) });
      // Show it running now: the stream's run.start may be far away (or the stream down).
      seedRun({ id: String(r.id), kind: "auto_read", done: r.done, total: r.total, changed: r.changed, new_items: r.new_items, errors: r.errors }, r.event_id);
      setConfirm(false);
      setGrew(null);
      announce("Marking old articles as read");
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.code === "total_changed") {
        // The server's recount is much higher than the number the person was shown: show the new one and ask again.
        const total = typeof e.body?.total === "number" ? e.body.total : shown.total;
        setGrew({ from: shown.total });
        setPreview({ p: { ...shown, total }, days, key, at: Date.now() });
        setConfirm(true);
      } else if (e instanceof ApiError && e.status === 409 && e.code === "confirm_required") {
        // More than the threshold to mark, or the count moved since the preview: ask again with the server's number.
        const total = typeof e.body?.total === "number" ? e.body.total : preview.p.total;
        setPreview({ ...preview, p: { ...preview.p, total } });
        setConfirm(true);
      } else {
        setConfirm(false);
        setError(catchUpError(e));
        // Busy: another run is going. Learn which one now, so the button is disabled and its progress shows.
        if (e instanceof ApiError && e.status === 409 && e.code === "busy") void pollStatus(qc).catch(() => undefined);
      }
    } finally {
      setBusy(false);
    }
  };

  /**
   * Called before anything is sent: a preview older than a minute is counted again. If that count is much higher
   * (more than 10% or 100 articles) the new number is shown and confirmed again. Resolves to the numbers to act on,
   * or null when the person has to look first.
   */
  const stillValid = async (): Promise<AutoReadPreview | null> => {
    if (!preview) return null;
    if (Date.now() - preview.at < PREVIEW_MAX_AGE_MS) return preview.p;
    setBusy(true);
    setError(null);
    try {
      const p = await previewAutoRead(body);
      const from = preview.p.total;
      setPreview({ p, days, key, at: Date.now() });
      if (grewTooMuch(from, p.total)) {
        setGrew({ from });
        setConfirm(true);
        return null;
      }
      return p;
    } catch (e) {
      setConfirm(false);
      setError(catchUpError(e));
      return null;
    } finally {
      setBusy(false);
    }
  };
  const start = async (confirmed: boolean) => {
    const p = await stillValid();
    if (!p) return;
    // The recount may have moved the total across the confirm threshold.
    if (!confirmed && p.total > p.confirm_above) return void setConfirm(true);
    await doRun(confirmed, p);
  };
  const askFirst = async () => {
    if (await stillValid()) setConfirm(true);
  };

  const total = preview?.p.total ?? 0;
  const needsConfirm = preview ? total > preview.p.confirm_above : false;
  const running = !!active;

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => void doPreview()} disabled={busy || running || !!blockedReason}>
          {autoPreview ? "Preview again" : "Preview"}
        </Button>
        {blockedReason ? <span className="text-xs text-fg2">{blockedReason}</span> : null}
      </div>
      {error ? <Notice tone="error">{error}</Notice> : null}
      {running ? (
        <p role="status" data-testid="auto-read-progress" className="text-sm text-fg2">
          Marking old articles as read: {n(active.done)} of {n(active.total)} checked
          {active.changed ? `, ${n(active.changed)} marked` : ""}.
        </p>
      ) : null}
      {note && !running ? (
        <p role="status" className="text-sm">
          {note}
        </p>
      ) : null}
      {preview && !running ? (
        <div data-testid="auto-read-preview" className="flex flex-col gap-2 rounded-xl border border-line bg-surface p-3">
          {total === 0 ? (
            <p className="text-sm">Nothing is older than that.</p>
          ) : (
            <>
              <p className="text-sm">
                <strong className="font-semibold">{n(total)}</strong> unread article{total === 1 ? " is" : "s are"} already older than the limit
                {preview.days !== undefined ? ` (using ${preview.days} days)` : ""}. Starred and muted articles are never touched.
              </p>
              <PerFeed feeds={preview.p.feeds} single={!!feedId} />
              {runBlocked ? (
                <p className="text-xs text-fg2">{runBlocked}</p>
              ) : (
                <div className="flex flex-col items-start gap-1">
                  <Button variant="solid" onClick={() => (needsConfirm ? void askFirst() : void start(false))} disabled={busy}>
                    {markOlderLabel(total)} now
                  </Button>
                  <p className="text-xs text-fg2">There is no Undo for this. You can mark articles unread again from any list.</p>
                </div>
              )}
            </>
          )}
        </div>
      ) : null}
      {confirm && preview ? (
        <Modal
          open
          onOpenChange={(o) => !o && !busy && setConfirm(false)}
          title={`${markOlderLabel(total)}?`}
          description={
            (grew ? `The count grew from ${n(grew.from)} to ${n(total)} since you previewed it. ` : "") +
            "They are unread, not starred, not muted and older than the limit. There is no Undo button for this. You can mark articles unread again from any list."
          }
          footer={
            <>
              <Button onClick={() => setConfirm(false)} disabled={busy}>
                Cancel
              </Button>
              <Button variant="solid" onClick={() => void start(true)} disabled={busy}>
                {markOlderConfirmLabel(total, busy)}
              </Button>
            </>
          }
        />
      ) : null}
    </div>
  );
}

/** The feeds behind a preview, biggest first; long lists show the first ten. */
function PerFeed({ feeds, single }: { feeds: AutoReadPreview["feeds"]; single: boolean }) {
  const [all, setAll] = useState(false);
  if (single || feeds.length === 0) return null;
  const shown = all ? feeds : feeds.slice(0, 10);
  return (
    <div>
      <ul aria-label="Articles to mark, by feed" className="divide-y divide-line text-sm">
        {shown.map((f) => (
          <li key={f.feed_id} className="flex items-baseline gap-2 py-1">
            <span className="min-w-0 flex-1 truncate">{f.title}</span>
            <span className="shrink-0 text-xs text-fg2">{f.days} days</span>
            <span className="w-14 shrink-0 text-right tabular-nums">{n(f.count)}</span>
          </li>
        ))}
      </ul>
      {feeds.length > 10 ? (
        <Button variant="link" className="min-h-11 px-0" onClick={() => setAll(!all)}>
          {all ? "Show fewer feeds" : `Show all ${feeds.length} feeds`}
        </Button>
      ) : null}
    </div>
  );
}
