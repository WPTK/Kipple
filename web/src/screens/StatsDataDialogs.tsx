import { useEffect, useState } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, errorMessage, ApiError } from "@/api/client";
import type { StatsRange, StatsSummary } from "@/api/types";
import { DICTIONARY_MD_URL, exportUrl, rangeProblem, startDownload, type ExportContent, type ExportFormat, type ExportRange } from "@/lib/statsExport";
import { RANGES, addDays, plural, todayString } from "@/lib/statsFormat";
import { clearStatsQueue } from "@/lib/statsSender";
import { toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";
import { Segmented } from "@/ui/segmented";

const DANGER = "border-danger text-danger";

/*
 * Every dialog here is a wrapper that mounts its body only while open, so each opening starts from a fresh body: no
 * armed confirmation, typed phrase, count, error or date survives a Cancel, Esc, overlay click or a finished delete.
 * A delete in flight cannot be closed over, and what a delete does afterwards (toast, refresh, queue) runs from the
 * always-mounted StatsDataSection, not from the dialog.
 */

/** A server error's own message when it sent one, else the generic wording. */
function serverMessage(e: unknown): string {
  if (e instanceof ApiError) {
    const b = e.body;
    const m = b && (typeof b.message === "string" ? b.message : e.status === 400 && typeof b.error === "string" ? b.error : null);
    if (m) return m;
  }
  return errorMessage(e);
}

/** How many events a failed delete had already removed (the server answers `deleted` with `complete:false`). */
function partialCount(e: unknown): number {
  if (e instanceof ApiError && e.body && typeof e.body.deleted === "number" && e.body.deleted > 0) return e.body.deleted;
  return 0;
}

/**
 * Today in the statistics time zone: the server's `range.to` of a cached week, month or year summary (never the "all"
 * one, whose end can be a future date after a time zone change), else the browser's date. Never later than the
 * browser's tomorrow.
 */
export function serverToday(qc: QueryClient): string {
  const local = todayString();
  const limit = addDays(local, 1);
  for (const [key, d] of qc.getQueriesData<StatsSummary>({ queryKey: ["stats"] })) {
    const r = key[1];
    const to = d?.range?.to;
    if ((r === "week" || r === "month" || r === "year") && to && /^\d{4}-\d{2}-\d{2}$/.test(to) && to <= limit) return to;
  }
  return local;
}

const LATE_NOTE = "Reading from other devices that hasn't synced yet can still show up later.";
const ZONE_NOTE = "Days follow the statistics time zone.";

interface DialogProps {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}

/** What a delete attempt came to. `deleted` counts events removed even when the attempt then failed part-way. */
export interface DeleteOutcome {
  scope: { all: true } | { from: string; to: string };
  deleted: number;
  ok: boolean;
}

// ---- Export -----------------------------------------------------------------------------------------------------

export function StatsExportDialog(p: DialogProps & { defaultRange: StatsRange }) {
  return p.open ? <ExportBody onOpenChange={p.onOpenChange} defaultRange={p.defaultRange} /> : null;
}

function ExportBody({ onOpenChange, defaultRange }: { onOpenChange: (o: boolean) => void; defaultRange: StatsRange }) {
  const qc = useQueryClient();
  const [format, setFormat] = useState<ExportFormat>("csv");
  const [content, setContent] = useState<ExportContent>("raw");
  const [range, setRange] = useState<ExportRange>(defaultRange);
  const [to, setTo] = useState(() => serverToday(qc));
  const [from, setFrom] = useState(() => addDays(serverToday(qc), -30));
  const [titles, setTitles] = useState(true);
  const [bom, setBom] = useState(false);
  const summary = content === "summary";
  const problem = range === "custom" ? rangeProblem(from, to, true) : null;
  const download = () => {
    if (problem) return;
    startDownload(exportUrl({ format, content, range, from, to, titles, bom }));
    onOpenChange(false);
    toast("Download started");
  };
  return (
    <Modal
      open
      onOpenChange={onOpenChange}
      title="Export statistics"
      description="Download your reading statistics as a file."
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="solid" disabled={!!problem} onClick={download}>
            Download
          </Button>
        </>
      }
    >
      <Segmented<ExportContent>
        legend="Contents"
        hint={summary ? "Totals, streaks and charts, as on the Stats screen." : "One row per recorded action."}
        value={content}
        onChange={setContent}
        options={[
          { value: "raw", label: "Raw data" },
          { value: "summary", label: "Summary" },
        ]}
      />
      <Segmented<ExportFormat>
        legend="Format"
        hint={summary ? "Summary is only available as JSON." : undefined}
        value={summary ? "json" : format}
        onChange={setFormat}
        options={[
          { value: "csv", label: "CSV", disabled: summary },
          { value: "json", label: "JSON" },
          { value: "jsonl", label: "JSON Lines", disabled: summary },
        ]}
      />
      {!summary && format === "csv" ? <Switch label="Add a byte-order mark (helps Excel show accents)" checked={bom} onChange={setBom} /> : null}
      <Segmented<ExportRange> legend="Range" value={range} onChange={setRange} options={[...RANGES, { value: "custom" as const, label: "Custom" }]} />
      {range === "custom" ? (
        <>
          <div className="grid grid-cols-2 gap-3">
            <Field label="From" error={problem}>
              {(p) => <input {...p} type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} className={inputCls} />}
            </Field>
            <Field label="To">{(p) => <input {...p} type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} className={inputCls} />}</Field>
          </div>
          <p className="text-xs text-fg2">{ZONE_NOTE}</p>
        </>
      ) : null}
      <Switch
        label="Include article titles and links"
        help="Turn off before sharing the file. Feed and folder names, times and your time zone stay in."
        checked={titles}
        onChange={setTitles}
      />
      <p className="text-sm">
        <a href={DICTIONARY_MD_URL} download className="text-link underline underline-offset-2">
          Download data dictionary
        </a>
        <span className="mt-1 block text-xs text-fg2">Explains every column in the file.</span>
      </p>
    </Modal>
  );
}

// ---- Delete -----------------------------------------------------------------------------------------------------

interface DeleteAnswer {
  count: number;
  deleted: number;
}

export const COUNT_DEBOUNCE_MS = 400;

export function DeleteRangeDialog(p: DialogProps & { onSettled: (o: DeleteOutcome) => void }) {
  return p.open ? <DeleteRangeBody onOpenChange={p.onOpenChange} onSettled={p.onSettled} /> : null;
}

function DeleteRangeBody({ onOpenChange, onSettled }: { onOpenChange: (o: boolean) => void; onSettled: (o: DeleteOutcome) => void }) {
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  // Tied to the dates they were made for, so a change of date drops them without an effect.
  const key = `${from}|${to}`;
  const [found, setFound] = useState<{ key: string; count: number | null; error: string | null } | null>(null);
  const [confirmKey, setConfirmKey] = useState<string | null>(null);
  const [failed, setFailed] = useState<{ key: string; message: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [recount, setRecount] = useState(0);
  const problem = from === "" && to === "" ? null : rangeProblem(from, to);
  const ready = from !== "" && to !== "" && !rangeProblem(from, to);
  const here = ready && found?.key === key ? found : null;
  const count = here?.count ?? null;
  const countError = here?.error ?? null;
  const confirming = confirmKey === key;
  const error = failed?.key === key ? failed.message : null;

  useEffect(() => {
    if (!ready) return;
    let stale = false;
    const t = setTimeout(() => {
      api<DeleteAnswer>("/api/stats/delete", { method: "POST", body: { from, to, dry_run: true }, quiet: true })
        .then((r) => {
          if (!stale) setFound({ key: `${from}|${to}`, count: r.count, error: null });
        })
        .catch((e) => {
          if (!stale) setFound({ key: `${from}|${to}`, count: null, error: serverMessage(e) });
        });
    }, COUNT_DEBOUNCE_MS);
    return () => {
      stale = true;
      clearTimeout(t);
    };
  }, [ready, from, to, recount]);

  const run = async () => {
    if (busy) return;
    setBusy(true);
    setFailed(null);
    try {
      const r = await api<DeleteAnswer>("/api/stats/delete", { method: "POST", body: { from, to, dry_run: false } });
      onSettled({ scope: { from, to }, deleted: r.deleted, ok: true });
      setBusy(false);
      onOpenChange(false);
    } catch (e) {
      const n = partialCount(e);
      onSettled({ scope: { from, to }, deleted: n, ok: false });
      setFailed({ key, message: n > 0 ? `${serverMessage(e)} Deleted ${plural(n, "record")} before it stopped. Run it again to finish.` : serverMessage(e) });
      setConfirmKey(null);
      setFound(null); // the count above is out of date: ask again
      setRecount((c) => c + 1);
      setBusy(false);
    }
  };

  const zero = count === 0;
  return (
    <Modal
      open
      // A delete in flight is not closed over: nothing here can outlive its dialog or be submitted twice.
      onOpenChange={(o) => {
        if (!o && busy) return;
        onOpenChange(o);
      }}
      title="Delete a date range"
      description="Deletes the statistics for these days. Your articles and read state stay."
      footer={
        <>
          <Button disabled={busy} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button className={DANGER} disabled={busy || count == null || zero} onClick={() => (confirming ? void run() : setConfirmKey(key))}>
            {busy ? "Deleting…" : confirming ? `Yes, delete ${plural(count ?? 0, "record")}` : "Delete"}
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-2 gap-3">
        <Field label="From" error={problem}>
          {(p) => <input {...p} type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} className={inputCls} />}
        </Field>
        <Field label="To">{(p) => <input {...p} type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} className={inputCls} />}</Field>
      </div>
      <p className="text-xs text-fg2">{ZONE_NOTE}</p>
      <p role="status" className="text-sm">
        {busy ? "Deleting…" : countError ? null : !ready ? "Pick both dates to see how much will be deleted." : count == null ? "Counting…" : zero ? "No records in that range" : `${plural(count, "record")} will be deleted`}
      </p>
      {confirming && count && !busy ? <p className="text-sm font-semibold">Delete {plural(count, "record")}? This can&apos;t be undone. Press again to confirm.</p> : null}
      <p className="text-xs text-fg2">{LATE_NOTE}</p>
      {countError ? <Notice tone="error">{countError}</Notice> : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

export const DELETE_ALL_PHRASE = "DELETE ALL";

export function DeleteAllDialog(p: DialogProps & { onSettled: (o: DeleteOutcome) => void }) {
  return p.open ? <DeleteAllBody onOpenChange={p.onOpenChange} onSettled={p.onSettled} /> : null;
}

function DeleteAllBody({ onOpenChange, onSettled }: { onOpenChange: (o: boolean) => void; onSettled: (o: DeleteOutcome) => void }) {
  const [typed, setTyped] = useState("");
  const [blurred, setBlurred] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const trimmed = typed.trim();
  const matches = trimmed === DELETE_ALL_PHRASE;
  // Not on every prefix: once it is as long as the phrase and still wrong, or after leaving the field.
  const hint = !matches && trimmed !== "" && (trimmed.length >= DELETE_ALL_PHRASE.length || blurred);
  const run = async () => {
    if (!matches || busy) return;
    setBusy(true);
    setError(null);
    try {
      const r = await api<DeleteAnswer>("/api/stats/delete", { method: "POST", body: { all: true, confirm: DELETE_ALL_PHRASE } });
      onSettled({ scope: { all: true }, deleted: r.deleted, ok: true });
      setBusy(false);
      onOpenChange(false);
    } catch (e) {
      const n = partialCount(e);
      onSettled({ scope: { all: true }, deleted: n, ok: false });
      setError(n > 0 ? `${serverMessage(e)} Deleted ${plural(n, "record")} before it stopped. Run it again to finish.` : serverMessage(e));
      setBusy(false);
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => {
        if (!o && busy) return;
        onOpenChange(o);
      }}
      title="Delete all statistics"
      description="Deletes all your statistics: reading time, opens, stars and shares. Your articles and read state stay. This can't be undone."
      footer={
        <>
          <Button disabled={busy} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button className={DANGER} disabled={busy || !matches} onClick={() => void run()}>
            {busy ? "Deleting…" : "Delete all statistics"}
          </Button>
        </>
      }
    >
      <Field label={`Type ${DELETE_ALL_PHRASE} to confirm`} help={hint ? "Type DELETE ALL exactly." : undefined}>
        {(p) => (
          <input
            {...p}
            value={typed}
            onChange={(e) => {
              setTyped(e.target.value);
              setBlurred(false);
            }}
            onBlur={() => setBlurred(true)}
            disabled={busy}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="characters"
            spellCheck={false}
            className={inputCls}
          />
        )}
      </Field>
      {busy ? <p role="status" className="text-sm">Deleting…</p> : null}
      <p className="text-xs text-fg2">{LATE_NOTE}</p>
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

// ---- The three actions ------------------------------------------------------------------------------------------

/**
 * Export, delete a range, delete all. Always rendered where it is used: it needs no settings and works with statistics
 * off. It stays mounted while a dialog is open, so it is what does the work after a delete.
 */
export function StatsDataSection({ defaultRange, hideTitle }: { defaultRange: StatsRange; hideTitle?: boolean }) {
  const qc = useQueryClient();
  const [dlg, setDlg] = useState<"export" | "range" | "all" | null>(null);
  const close = (o: boolean) => {
    if (!o) setDlg(null);
  };
  const settled = (r: DeleteOutcome) => {
    void qc.invalidateQueries({ queryKey: ["stats"] }); // whatever happened, the numbers may have changed
    if (r.ok || r.deleted > 0) {
      // A queued event is stamped when the server receives it, so it can only land in today: it matters to a range only when that includes today.
      const today = serverToday(qc);
      if ("all" in r.scope || (r.scope.from <= today && today <= r.scope.to)) clearStatsQueue();
    }
    if (r.ok) toast(`Deleted ${plural(r.deleted, "record")}`);
  };
  return (
    <section aria-label={hideTitle ? undefined : "Your statistics data"} className="flex flex-col gap-3">
      {hideTitle ? null : <h3 className="text-sm font-semibold">Your statistics data</h3>}
      <p className="text-xs text-fg2">Export or delete what Kipple has recorded. Works with statistics on or off.</p>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setDlg("export")}>Export…</Button>
        <Button onClick={() => setDlg("range")}>Delete a date range…</Button>
        <Button className={DANGER} onClick={() => setDlg("all")}>
          Delete all statistics…
        </Button>
      </div>
      <StatsExportDialog open={dlg === "export"} onOpenChange={close} defaultRange={defaultRange} />
      <DeleteRangeDialog open={dlg === "range"} onOpenChange={close} onSettled={settled} />
      <DeleteAllDialog open={dlg === "all"} onOpenChange={close} onSettled={settled} />
    </section>
  );
}
