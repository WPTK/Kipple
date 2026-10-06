import { useEffect, useId, useRef, useState, type DragEvent, type FormEvent, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/button";
import { Field, Notice, inputCls } from "@/ui/kit";
import { passwordProblem } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import {
  cancelRestore,
  confirmRestore,
  fetchBackupFeeds,
  fetchRestoreStatus,
  restoreErrorText,
  sizeText,
  uploadRestoreFile,
  type BackupSummary,
  type OpmlSummary,
  type RestoreStatus,
} from "./restoreApi";

const RESTORE_STEP = { id: "restore", n: 0, title: "Restore from a backup" } as const;

/** A network failure during the upload has no answer to quote: the usual causes are a full disk or a proxy size limit. */
const UPLOAD_FAILED = "The upload was refused or interrupted. Check that the server has enough free disk space and that any proxy in front of Kipple allows a file this size.";

/** How often the page asks the server whether its check of the backup is done. */
export const CHECK_POLL_MS = 2000;

type Mode = "everything" | "feeds";
/** pick: choosing a file. sending: the upload runs. waiting: the server holds an upload (receiving or checking it). review: a backup or OPML file is ready to confirm. */
type View = "pick" | "sending" | "waiting" | "review";

const EVERYTHING_HELP = "Everything brings back your feeds and folders, your read and starred items, your settings and your account.";
const FEEDS_HELP = "Feeds only brings your subscriptions and folders. Read and starred items, settings and your account stay behind.";

/** What the restore screen hands on. */
export interface RestoreHandlers {
  onBack: () => void;
  /** "Feeds only" (or an OPML file): the file to offer in the import step, once the account exists. */
  onFeedsOnly: (feeds: File) => void;
  /** "Everything" was confirmed and the server is restarting. `username` is null when this page never saw the backup. */
  onConfirmed: (info: { estimateSeconds: number; username: string | null }) => void;
}

/** Why a backup needs a password chosen here, in the reader's words. */
function passwordReason(s: BackupSummary): string {
  if (s.new_password_reason === "open_refused") return "This backup has no password, and Kipple doesn't allow that from this address. Choose a password for it.";
  if (s.new_password_reason === "access_unavailable") return "This backup relies on Cloudflare Access to sign in, and Kipple can't see your Access sign-in from this page. Choose a password to use instead.";
  return "This backup needs a password to sign in from this page. Choose one.";
}

/**
 * Before the account exists: restore a Kipple backup (a zip) or take the feeds of an OPML file. The file goes to the
 * server as it is, with a progress bar; the server then checks it, which this page waits for. A backup then offers
 * "Everything" (the server replaces itself with it and restarts) or "Feeds only" (the normal setup continues and the
 * import step is offered the feeds). Nothing can be confirmed until the backup's contents are on screen.
 * `resume`: the server already holds a restore from before a reload, and this page asks it where it is.
 */
export function RestoreStep({ resume, onBack, onFeedsOnly, onConfirmed }: RestoreHandlers & { resume: boolean }) {
  const uid = useId();
  const [view, setView] = useState<View>(resume ? "waiting" : "pick");
  const viewNow = useRef(view);
  useEffect(() => {
    viewNow.current = view;
  });
  const [file, setFile] = useState<File | null>(null);
  const [progress, setProgress] = useState<{ sent: number; total: number } | null>(null);
  const [serverState, setServerState] = useState<"uploading" | "checking">("checking");
  const [backup, setBackup] = useState<BackupSummary | null>(null);
  const [opml, setOpml] = useState<OpmlSummary | null>(null);
  const [mode, setMode] = useState<Mode>("everything");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busyUpload, setBusyUpload] = useState(false);
  const [pwError, setPwError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const pwInput = useRef<HTMLInputElement>(null);
  const handoff = useRef({ onConfirmed });
  useEffect(() => {
    handoff.current = { onConfirmed };
  });

  // An upload still in flight when this screen goes away is stopped.
  useEffect(() => () => abort.current?.abort(), []);

  const passwordNeeded = backup?.needs_new_password === true;
  const everything = mode === "everything" && !opml;

  /** Shows what the server says about its restore. */
  const apply = (st: RestoreStatus) => {
    if (st.state === "ready" && st.summary) {
      setBackup(st.summary);
      setMode("everything");
      setView("review");
    } else if (st.state === "failed") {
      setError(st.error?.message || "The backup could not be read. Try another file.");
      setView("pick");
    } else if (st.state === "confirmed") {
      handoff.current.onConfirmed({ estimateSeconds: st.estimate_seconds ?? st.summary?.estimate_seconds ?? 300, username: st.summary?.username ?? null });
    } else if (st.state === "uploading" || st.state === "checking") {
      setServerState(st.state);
      setView("waiting");
    } else {
      setView("pick");
    }
  };

  // Opened at the picker: if the server holds a restore after all, show that, unless the person has already started one here.
  useEffect(() => {
    if (resume) return;
    let stop = false;
    fetchRestoreStatus()
      .then((st) => {
        if (!stop && st.state !== "none" && viewNow.current === "pick") apply(st);
      })
      .catch(() => undefined);
    return () => {
      stop = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // While the server holds an upload that is not ready (this page's own, or one from before a reload), ask until it is.
  useEffect(() => {
    if (view !== "waiting") return;
    let stop = false;
    const ask = async () => {
      try {
        const st = await fetchRestoreStatus();
        if (!stop) apply(st);
      } catch {
        /* the server is busy or away for a moment: ask again */
      }
    };
    void ask();
    const t = window.setInterval(() => void ask(), CHECK_POLL_MS);
    return () => {
      stop = true;
      window.clearInterval(t);
    };
  }, [view]);

  const start = async (f: File) => {
    setFile(f);
    setError(null);
    setBusyUpload(false);
    setBackup(null);
    setOpml(null);
    setProgress({ sent: 0, total: f.size });
    setView("sending");
    const ctl = new AbortController();
    abort.current = ctl;
    try {
      const r = await uploadRestoreFile(f, (sent, total) => setProgress({ sent, total }), ctl.signal);
      if ("kind" in r) {
        setOpml(r);
        setMode("feeds");
        setView("review");
      } else {
        setServerState("checking");
        setView("waiting");
      }
    } catch (e) {
      if (e instanceof ApiError && e.code === "aborted") return;
      if (e instanceof ApiError && e.status === 0) setError(UPLOAD_FAILED);
      // 411 length_required, 400 upload_incomplete, 409 restore_cancelled and the rest carry a message written for the reader.
      else setError(restoreErrorText(e));
      setBusyUpload(e instanceof ApiError && e.code === "restore_busy");
      setFile(null);
      setProgress(null);
      setView("pick");
    } finally {
      abort.current = null;
    }
  };

  const drop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    const f = e.dataTransfer.files[0];
    if (f && view === "pick" && !busy) void start(f);
  };

  /** Throws the upload away (on the server too) and goes back to choosing a file. */
  const discard = async () => {
    abort.current?.abort();
    setBusy(true);
    try {
      await cancelRestore();
    } catch (e) {
      // 409: already confirmed, so it cannot be cancelled. Any other failure leaves nothing here to cancel.
      if (e instanceof ApiError && e.status === 409) {
        setError(restoreErrorText(e));
        setBusy(false);
        return;
      }
    }
    setBusy(false);
    setBackup(null);
    setOpml(null);
    setProgress(null);
    setFile(null);
    setPassword("");
    setAgain("");
    setError(null);
    setBusyUpload(false);
    setPwError(null);
    setView("pick");
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setPwError(null);
    if (!everything) {
      setBusy(true);
      try {
        onFeedsOnly(opml && file ? file : await fetchBackupFeeds());
      } catch (err) {
        setError(restoreErrorText(err));
        setBusy(false);
      }
      return;
    }
    const wants = passwordNeeded || password !== "";
    if (wants) {
      const problem = password === "" ? "Enter a password." : passwordProblem(password);
      if (problem || password !== again) {
        setPwError(problem ?? "The two passwords don't match.");
        pwInput.current?.focus();
        return;
      }
    }
    setBusy(true);
    try {
      const r = await confirmRestore(wants ? password : undefined);
      onConfirmed({ estimateSeconds: r.estimate_seconds, username: backup?.username ?? null });
    } catch (err) {
      setBusy(false);
      if (err instanceof ApiError && err.code === "bad_new_password") {
        setPwError(restoreErrorText(err));
        pwInput.current?.focus();
        return;
      }
      setError(restoreErrorText(err));
    }
  };

  return (
    <WizardFrame step={RESTORE_STEP} description="Restore a Kipple backup, or bring only the feeds from a backup or an OPML file.">
      <div className="flex flex-1 flex-col gap-5">
        {error ? (
          <Notice tone="error">
            <p>{error}</p>
            {busyUpload ? (
              <Button className="mt-2" disabled={busy} onClick={() => void discard()}>
                Cancel the other upload
              </Button>
            ) : null}
          </Notice>
        ) : null}

        {view === "pick" ? (
          <>
            <div
              onDragOver={(e) => {
                e.preventDefault();
                setOver(true);
              }}
              onDragLeave={() => setOver(false)}
              onDrop={drop}
              className={cn("flex flex-col gap-3 rounded-xl border-2 border-dashed p-4", over ? "border-accent bg-selection" : "border-line")}
            >
              <p className="text-sm text-fg2">Drop a file here, or choose one. A Kipple backup (a .zip file) or an OPML file from another reader.</p>
              <Field label="Backup or OPML file" help="Images and icons are downloaded again the first time you view them.">
                {(a) => (
                  <input
                    {...a}
                    type="file"
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f) void start(f);
                    }}
                    className={`${inputCls} py-2`}
                  />
                )}
              </Field>
            </div>
            <StepActions back={<Button onClick={onBack}>Back</Button>}>{null}</StepActions>
          </>
        ) : null}

        {view === "sending" && file ? (
          <div className="flex flex-1 flex-col gap-3">
            <p className="font-semibold" role="status">
              Uploading {file.name}
            </p>
            <progress className="h-3 w-full accent-[var(--kp-accent)]" aria-label="Upload progress" value={progress?.sent ?? 0} max={progress?.total || 1} />
            <p className="text-sm text-fg2">
              {sizeText(progress?.sent ?? 0)} of {sizeText(progress?.total ?? 0)}. Keep this page open.
            </p>
            <StepActions>
              <Button onClick={() => void discard()}>Cancel upload</Button>
            </StepActions>
          </div>
        ) : null}

        {view === "waiting" ? (
          <div className="flex flex-1 flex-col gap-3">
            <p className="font-semibold" role="status">
              {serverState === "uploading" ? "A backup is being uploaded to this Kipple." : "Checking your backup..."}
            </p>
            <p className="text-sm text-fg2">{serverState === "uploading" ? "Wait for it to finish, or cancel it and start again." : "This can take a minute for a large backup. Keep this page open."}</p>
            <StepActions>
              <Button disabled={busy} onClick={() => void discard()}>
                Cancel
              </Button>
            </StepActions>
          </div>
        ) : null}

        {view === "review" ? (
          <form onSubmit={(e) => void submit(e)} className="flex flex-1 flex-col gap-5" noValidate>
            {backup ? (
              <section className="flex flex-col gap-1 rounded-xl border border-line bg-surface p-3" aria-label="Backup contents" data-testid="backup-summary">
                <p className="font-semibold">Kipple backup from {new Date(backup.created_at).toLocaleDateString(undefined, { dateStyle: "long" })}</p>
                <p className="text-sm text-fg2">
                  Made by Kipple {backup.kipple_version}. {backup.feeds} feeds, {backup.items} items, {backup.starred} starred.
                </p>
                <p className="text-sm">You will sign in as {backup.username}.</p>
              </section>
            ) : null}
            {opml ? (
              <section className="flex flex-col gap-1 rounded-xl border border-line bg-surface p-3" aria-label="File contents" data-testid="opml-summary">
                <p className="font-semibold">OPML file</p>
                <p className="text-sm text-fg2">{opml.feeds} feeds found.</p>
              </section>
            ) : null}

            {opml ? (
              <p className="text-sm text-fg2">{FEEDS_HELP}</p>
            ) : (
              <fieldset className="flex min-w-0 flex-col gap-3">
                <legend className="mb-2 text-sm font-semibold">What do you want to bring back?</legend>
                <Choice id={`${uid}-all`} checked={mode === "everything"} onSelect={() => setMode("everything")} title="Everything" help={EVERYTHING_HELP} />
                <Choice id={`${uid}-feeds`} checked={mode === "feeds"} onSelect={() => setMode("feeds")} title="Feeds only" help={FEEDS_HELP} />
              </fieldset>
            )}

            {everything && backup ? (
              <div className="flex flex-col gap-3">
                {passwordNeeded ? (
                  <Notice tone="warn" role="status">
                    {passwordReason(backup)}
                  </Notice>
                ) : null}
                <Field
                  label={passwordNeeded ? "New password" : "Set a new password (optional)"}
                  help={passwordNeeded ? "At least 5 characters." : "Leave empty to keep the password in the backup."}
                  error={pwError}
                >
                  {(a) => (
                    <input
                      {...a}
                      ref={pwInput}
                      name="new-password"
                      type="password"
                      autoComplete="new-password"
                      required={passwordNeeded}
                      value={password}
                      onChange={(e) => {
                        setPassword(e.target.value);
                        setPwError(null);
                      }}
                      className={inputCls}
                    />
                  )}
                </Field>
                {passwordNeeded || password !== "" ? (
                  <Field label="Password again">
                    {(a) => <input {...a} name="new-password-again" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} className={inputCls} />}
                  </Field>
                ) : null}
                <p className="text-sm text-fg2">Kipple restarts to restore the backup. Images and icons are downloaded again the first time you view them.</p>
              </div>
            ) : null}

            <StepActions
              back={
                <Button disabled={busy} onClick={() => void discard()}>
                  Cancel
                </Button>
              }
            >
              <Button type="submit" variant="solid" disabled={busy}>
                {busy ? "Working" : everything ? "Restore everything" : "Continue"}
              </Button>
            </StepActions>
          </form>
        ) : null}
      </div>
    </WizardFrame>
  );
}

function Choice({ id, checked, onSelect, title, help }: { id: string; checked: boolean; onSelect: () => void; title: string; help: ReactNode }) {
  return (
    <div className={cn("rounded-xl border p-3", checked ? "border-accent bg-selection" : "border-line bg-surface")}>
      <label htmlFor={id} className="flex min-h-11 cursor-pointer items-start gap-3">
        <input id={id} type="radio" name="restore-mode" checked={checked} onChange={onSelect} aria-describedby={`${id}-d`} className="mt-1 size-5 shrink-0 accent-[var(--kp-accent)]" />
        <span className="text-base font-semibold">{title}</span>
      </label>
      <p id={`${id}-d`} className="ml-8 text-sm text-fg2">
        {help}
      </p>
    </div>
  );
}
