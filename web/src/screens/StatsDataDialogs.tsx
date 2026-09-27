import { useEffect, useState } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, errorMessage, ApiError } from "@/api/client";
import type { StatsRange, StatsSummary } from "@/api/types";
import { DICTIONARY_MD_URL, exportUrl, rangeProblem, startDownload, type ExportContent, type ExportFormat, type ExportRange } from "@/lib/statsExport";
import { RANGES, plural, todayString } from "@/lib/statsFormat";
import { clearStatsQueue } from "@/lib/statsSender";
import { toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";
import { Segmented } from "@/ui/segmented";

const DANGER = "border-danger text-danger";

/*
 * Every dialog here is a wrapper that mounts its body only while open, so each opening starts from a fresh body: no
 * armed confirmation, typed phrase, count, error or date survives a Cancel, Esc, overlay click or a finished delete.
 */

/** A server error's own message when it sent one (a 400 says what was wrong), else the generic wording. */
function serverMessage(e: unknown): string {
  if (e instanceof ApiError && e.status === 400) {
    const b = e.body;
    const m = b && (typeof b.message === "string" ? b.message : typeof b.error === "string" ? b.error : null);
    if (m) return m;
  }
  return errorMessage(e);
}

/** The statistics time zone's today when a summary is cached (the server's `range.to`), else the browser's. */
function statsToday(qc: QueryClient): string {
  for (const [, d] of qc.getQueriesData<StatsSummary>({ queryKey: ["stats"] })) {
    if (d?.range?.to) return d.range.to;
  }
  return todayString();
}

function shiftDay(date: string, days: number): string {
  return new Date(Date.parse(`${date}T00:00:00Z`) + days * 86_400_000).toISOString().slice(0, 10);
}

/** After any successful delete: the Stats numbers are stale, and what this device has not yet sent must not resurrect them. */
function afterDelete(qc: QueryClient, deleted: number) {
  clearStatsQueue();
  void qc.invalidateQueries({ queryKey: ["stats"] });
  toast(`Deleted ${plural(deleted, "event")}`);
}

const LATE_NOTE = "Events still waiting to be sent from other devices may appear later.";
const ZONE_NOTE = "Days follow the statistics time zone.";

// ---- Export -----------------------------------------------------------------------------------------------------

interface DialogProps {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}

export function StatsExportDialog(p: DialogProps & { defaultRange: StatsRange }) {
  return p.open ? <ExportBody onOpenChange={p.onOpenChange} defaultRange={p.defaultRange} /> : null;
}

function ExportBody({ onOpenChange, defaultRange }: { onOpenChange: (o: boolean) => void; defaultRange: StatsRange }) {
  const qc = useQueryClient();
  const [format, setFormat] = useState<ExportFormat>("csv");
  const [content, setContent] = useState<ExportContent>("raw");
  const [range, setRange] = useState<ExportRange>(defaultRange);
  const [to, setTo] = useState(() => statsToday(qc));
  const [from, setFrom] = useState(() => shiftDay(statsToday(qc), -30));
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
        hint={summary ? "The totals, streaks and charts as JSON, like the Stats screen." : "One row per recorded event."}
        value={content}
        onChange={setContent}
        options={[
          { value: "raw", label: "Raw events" },
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
        help="Turn off if you plan to share the file. Titles and links are removed; feed and folder names, times and your time zone stay."
        checked={titles}
        onChange={setTitles}
      />
      <p className="text-sm">
        <a href={DICTIONARY_MD_URL} download className="text-link underline underline-offset-2">
          Download data dictionary
        </a>
        <span className="mt-1 block text-xs text-fg2">Describes every column so a spreadsheet, script or AI tool can read the file.</span>
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

export function DeleteRangeDialog(p: DialogProps) {
  return p.open ? <DeleteRangeBody onOpenChange={p.onOpenChange} /> : null;
}

function DeleteRangeBody({ onOpenChange }: { onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  // Tied to the dates they were made for, so a change of date drops them without an effect.
  const key = `${from}|${to}`;
  const [found, setFound] = useState<{ key: string; count: number | null; error: string | null } | null>(null);
  const [confirmKey, setConfirmKey] = useState<string | null>(null);
  const [failed, setFailed] = useState<{ key: string; message: string } | null>(null);
  const [busy, setBusy] = useState(false);
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
  }, [ready, from, to]);

  const run = async () => {
    setBusy(true);
    setFailed(null);
    try {
      const r = await api<DeleteAnswer>("/api/stats/delete", { method: "POST", body: { from, to, dry_run: false } });
      afterDelete(qc, r.deleted);
      onOpenChange(false);
    } catch (e) {
      setFailed({ key, message: serverMessage(e) });
      setConfirmKey(null);
    } finally {
      setBusy(false);
    }
  };

  const zero = count === 0;
  return (
    <Modal
      open
      onOpenChange={onOpenChange}
      title="Delete a date range"
      description="Removes the recorded statistics for these days. Articles and read state are not affected."
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button className={DANGER} disabled={busy || count == null || zero} onClick={() => (confirming ? void run() : setConfirmKey(key))}>
            {confirming ? `Yes, delete ${plural(count ?? 0, "event")}` : "Delete"}
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
        {countError ? null : !ready ? "Choose both dates to see how many events this removes." : count == null ? "Counting…" : zero ? "No events in that range" : `${plural(count, "event")} will be deleted`}
      </p>
      {confirming && count ? <p className="text-sm font-semibold">Delete {plural(count, "event")}? This can&apos;t be undone. Press the button again to confirm.</p> : null}
      <p className="text-xs text-fg2">{LATE_NOTE}</p>
      {countError ? <Notice tone="error">{countError}</Notice> : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

export const DELETE_ALL_PHRASE = "DELETE ALL";

export function DeleteAllDialog(p: DialogProps) {
  return p.open ? <DeleteAllBody onOpenChange={p.onOpenChange} /> : null;
}

function DeleteAllBody({ onOpenChange }: { onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const matches = typed.trim() === DELETE_ALL_PHRASE;
  const run = async () => {
    if (!matches) return;
    setBusy(true);
    setError(null);
    try {
      const r = await api<DeleteAnswer>("/api/stats/delete", { method: "POST", body: { all: true, confirm: DELETE_ALL_PHRASE } });
      afterDelete(qc, r.deleted);
      onOpenChange(false);
    } catch (e) {
      setError(serverMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      open
      onOpenChange={onOpenChange}
      title="Delete all statistics"
      description="This removes every recorded statistic: reading time, opens, stars and shares. It does not remove articles or read state. It can't be undone."
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button className={DANGER} disabled={busy || !matches} onClick={() => void run()}>
            Delete all statistics
          </Button>
        </>
      }
    >
      <Field label={`Type ${DELETE_ALL_PHRASE} to confirm`} help={typed !== "" && !matches ? "Type DELETE ALL exactly." : undefined}>
        {(p) => (
          <input
            {...p}
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="characters"
            spellCheck={false}
            className={inputCls}
          />
        )}
      </Field>
      <p className="text-xs text-fg2">{LATE_NOTE}</p>
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

// ---- The three actions ------------------------------------------------------------------------------------------

/** Export, delete a range, delete all. Always rendered where it is used: it needs no settings and works with statistics off. */
export function StatsDataSection({ defaultRange }: { defaultRange: StatsRange }) {
  const [dlg, setDlg] = useState<"export" | "range" | "all" | null>(null);
  const close = (o: boolean) => {
    if (!o) setDlg(null);
  };
  return (
    <section aria-label="Your statistics data" className="flex flex-col gap-3">
      <h3 className="text-sm font-semibold">Your statistics data</h3>
      <p className="text-xs text-fg2">Export or delete what Kipple has recorded. This works whether statistics are on or off.</p>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setDlg("export")}>Export…</Button>
        <Button onClick={() => setDlg("range")}>Delete a date range…</Button>
        <Button className={DANGER} onClick={() => setDlg("all")}>
          Delete all statistics…
        </Button>
      </div>
      <StatsExportDialog open={dlg === "export"} onOpenChange={close} defaultRange={defaultRange} />
      <DeleteRangeDialog open={dlg === "range"} onOpenChange={close} />
      <DeleteAllDialog open={dlg === "all"} onOpenChange={close} />
    </section>
  );
}
