import { useEffect, useRef, useState } from "react";
import { liveStore } from "@/api/events";
import { ApiError, errorMessage } from "@/api/client";
import { previewAutoRead, runAutoRead, type AutoReadPreview } from "@/api/autoRead";
import { useStoreSelector } from "@/lib/store";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice } from "@/ui/kit";

const n = (x: number): string => x.toLocaleString();

/** What a failed preview or run says. `busy` and `confirm_required` have their own wording; everything else is generic. */
export function catchUpError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 409 && e.code === "busy") return "Another catch-up is already running. Wait for it to finish, then try again.";
    if (e.status === 404) return "That feed no longer exists.";
    if (e.status === 400) return typeof e.body?.message === "string" ? e.body.message : "That number of days isn't accepted.";
  }
  return errorMessage(e);
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
  offer,
  blockedReason,
  runBlocked,
}: {
  feedId?: string;
  days?: number;
  offer?: boolean;
  /** Why Preview is unavailable right now. */
  blockedReason?: string;
  /** Why the run button is replaced by this text (the feed editor: a change that is not saved yet). */
  runBlocked?: string;
}) {
  const key = `${feedId ?? ""}|${days ?? ""}`;
  const [held, setPreview] = useState<{ p: AutoReadPreview; days?: number; key: string } | null>(null);
  // Numbers counted for another threshold or feed are not shown.
  const preview = held && held.key === key ? held : null;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const runs = useStoreSelector(liveStore, (s) => s.runs);
  const active = Object.values(runs).find((r) => r.kind === "auto_read");
  const body = { ...(feedId ? { feed_id: feedId } : {}), ...(days !== undefined ? { days } : {}) };


  // A run that finishes: say how many it marked. The count is what the last progress event reported.
  const last = useRef(active);
  useEffect(() => {
    const before = last.current;
    last.current = active;
    if (before && !active) {
      const changed = before.changed ?? before.done;
      setNote(changed > 0 ? `Marked ${n(changed)} older article${changed === 1 ? "" : "s"} as read.` : "Nothing was left to mark.");
      setPreview(null);
    }
  }, [active]);

  const doPreview = async () => {
    setBusy(true);
    setError(null);
    setNote(null);
    try {
      const p = await previewAutoRead(body);
      setPreview({ p, days, key });
    } catch (e) {
      setError(catchUpError(e));
    } finally {
      setBusy(false);
    }
  };

  const doRun = async (confirmed: boolean) => {
    if (!preview) return;
    setBusy(true);
    setError(null);
    try {
      await runAutoRead({ ...body, ...(confirmed ? { confirm: true } : {}) });
      setConfirm(false);
      announce("Marking old articles as read");
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.code === "confirm_required") {
        // More than the threshold to mark, or the count moved since the preview: ask again with the server's number.
        const total = typeof e.body?.total === "number" ? e.body.total : preview.p.total;
        setPreview({ ...preview, p: { ...preview.p, total } });
        setConfirm(true);
      } else {
        setConfirm(false);
        setError(catchUpError(e));
      }
    } finally {
      setBusy(false);
    }
  };

  const total = preview?.p.total ?? 0;
  const needsConfirm = preview ? total > preview.p.confirm_above : false;
  const running = !!active;

  return (
    <div className="flex flex-col gap-2">
      {offer ? (
        <p className="text-sm">
          Saved. Nothing was marked. Articles that are already older than this stay unread until you clear them below; from now on each one is marked as it reaches that age.
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <Button onClick={() => void doPreview()} disabled={busy || running || !!blockedReason}>
          Preview
        </Button>
        {blockedReason ? <span className="text-xs text-fg2">{blockedReason}</span> : <span className="text-xs text-fg2">Counts the older unread articles without marking any.</span>}
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
            <p className="text-sm">Nothing is older than that. There is nothing to mark.</p>
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
                  <Button variant="solid" onClick={() => (needsConfirm ? setConfirm(true) : void doRun(false))} disabled={busy}>
                    Mark {n(total)} older article{total === 1 ? "" : "s"} as read now
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
          title={`Mark ${n(total)} older articles as read?`}
          description="They are unread, not starred, not muted and older than the limit. There is no Undo button for this. You can mark articles unread again from any list."
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirm(false)} disabled={busy}>
                Cancel
              </Button>
              <Button variant="solid" onClick={() => void doRun(true)} disabled={busy}>
                {busy ? "Starting" : `Mark ${n(total)} as read`}
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
