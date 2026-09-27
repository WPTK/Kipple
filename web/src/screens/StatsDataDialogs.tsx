import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, ApiError } from "@/api/client";
import type { StatsRange } from "@/api/types";
import { DICTIONARY_MD_URL, exportUrl, rangeProblem, startDownload, type ExportContent, type ExportFormat, type ExportRange } from "@/lib/statsExport";
import { RANGES, plural, todayString } from "@/lib/statsFormat";
import { toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Switch, inputCls } from "@/ui/kit";
import { Segmented } from "@/ui/segmented";

const DANGER = "border-danger text-danger";

/** A server error's own message when it sent one (a 400 says what was wrong), else the generic wording. */
function serverMessage(e: unknown): string {
  if (e instanceof ApiError && e.status === 400) {
    const b = e.body;
    const m = b && (typeof b.message === "string" ? b.message : typeof b.error === "string" ? b.error : null);
    if (m) return m;
  }
  return errorMessage(e);
}

function daysAgo(n: number): string {
  const d = new Date();
  d.setDate(d.getDate() - n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

// ---- Export -----------------------------------------------------------------------------------------------------

export function StatsExportDialog({ open, onOpenChange, defaultRange }: { open: boolean; onOpenChange: (o: boolean) => void; defaultRange: StatsRange }) {
  const [format, setFormat] = useState<ExportFormat>("csv");
  const [content, setContent] = useState<ExportContent>("raw");
  const [range, setRange] = useState<ExportRange>(defaultRange);
  const [from, setFrom] = useState(() => daysAgo(30));
  const [to, setTo] = useState(() => todayString());
  const [titles, setTitles] = useState(true);
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) setRange(defaultRange); // each opening starts from the range on screen
  }
  const summary = content === "summary";
  const problem = range === "custom" ? rangeProblem(from, to) : null;
  const download = () => {
    if (problem) return;
    startDownload(exportUrl({ format, content, range, from, to, titles }));
    onOpenChange(false);
    toast("Download started");
  };
  return (
    <Modal
      open={open}
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
      <Segmented<ExportRange> legend="Range" value={range} onChange={setRange} options={[...RANGES, { value: "custom" as const, label: "Custom" }]} />
      {range === "custom" ? (
        <div className="grid grid-cols-2 gap-3">
          <Field label="From" error={problem}>
            {(p) => <input {...p} type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} className={inputCls} />}
          </Field>
          <Field label="To">{(p) => <input {...p} type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} className={inputCls} />}</Field>
        </div>
      ) : null}
      <Switch label="Include article titles and links" help="Turn off if you plan to share the file; feed and folder names stay." checked={titles} onChange={setTitles} />
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

export function DeleteRangeDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  // Everything below is tied to the dates it was made for, so a change of date drops it without an effect.
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
    if (!open || !ready) return;
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
  }, [open, ready, from, to]);

  const run = async () => {
    setBusy(true);
    setFailed(null);
    try {
      const r = await api<DeleteAnswer>("/api/stats/delete", { method: "POST", body: { from, to, dry_run: false } });
      void qc.invalidateQueries({ queryKey: ["stats"] });
      toast(`Deleted ${plural(r.deleted, "event")}`);
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
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          setFrom("");
          setTo("");
        }
        onOpenChange(o);
      }}
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
      <p role="status" className="text-sm">
        {countError ? null : !ready ? "Choose both dates to see how many events this removes." : count == null ? "Counting…" : zero ? "No events in that range" : `${plural(count, "event")} will be deleted`}
      </p>
      {confirming && count ? <p className="text-sm font-semibold">Delete {plural(count, "event")}? This can&apos;t be undone. Press the button again to confirm.</p> : null}
      {countError ? <Notice tone="error">{countError}</Notice> : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

export const DELETE_ALL_PHRASE = "DELETE ALL";

export function DeleteAllDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await api<DeleteAnswer>("/api/stats/delete", { method: "POST", body: { all: true, confirm: DELETE_ALL_PHRASE } });
      void qc.invalidateQueries({ queryKey: ["stats"] });
      toast(`Deleted ${plural(r.deleted, "event")}`);
      setTyped("");
      onOpenChange(false);
    } catch (e) {
      setError(serverMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          setTyped("");
          setError(null);
        }
        onOpenChange(o);
      }}
      title="Delete all statistics"
      description="This removes every recorded statistic: reading time, opens, stars and shares. It does not remove articles or read state. It can't be undone."
      footer={
        <>
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button className={DANGER} disabled={busy || typed !== DELETE_ALL_PHRASE} onClick={() => void run()}>
            Delete all statistics
          </Button>
        </>
      }
    >
      <Field label={`Type ${DELETE_ALL_PHRASE} to confirm`}>
        {(p) => <input {...p} value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" autoCapitalize="characters" spellCheck={false} className={inputCls} />}
      </Field>
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

// ---- Settings section -------------------------------------------------------------------------------------------

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
