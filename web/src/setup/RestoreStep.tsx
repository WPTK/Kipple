import { useEffect, useId, useRef, useState, type DragEvent, type FormEvent, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/button";
import { Field, Notice, inputCls } from "@/ui/kit";
import { passwordProblem } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import { cancelRestore, confirmRestore, fetchBackupFeeds, restoreErrorText, sizeText, uploadRestoreFile, type BackupSummary, type UploadSummary } from "./restoreApi";

const RESTORE_STEP = { id: "restore", n: 0, title: "Restore from a backup" } as const;

/** A network failure during the upload has no answer to quote: the usual causes are a full disk or a proxy size limit. */
const UPLOAD_FAILED = "The upload was refused or interrupted. Check that the server has enough free disk space and that any proxy in front of Kipple allows a file this size.";

type Mode = "everything" | "feeds";

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
function passwordReason(s: BackupSummary | null): string {
  if (s?.new_password_reason === "open_refused") return "This backup has no password, and Kipple doesn't allow that from this address. Choose a password for it.";
  if (s?.new_password_reason === "access_unavailable") return "This backup relies on Cloudflare Access to sign in, and Kipple can't see your Access sign-in from this page. Choose a password to use instead.";
  return "This backup needs a password to sign in from this page. Choose one.";
}

/**
 * Before the account exists: restore a Kipple backup (a zip) or take the feeds of an OPML file. The file goes to the
 * server as it is, with a progress bar. A backup then offers "Everything" (the server replaces itself with it and
 * restarts) or "Feeds only" (the normal setup continues and the import step is offered the feeds).
 * `resume`: the server already holds an upload from before a reload, and this page knows nothing about it.
 */
export function RestoreStep({ resume, onBack, onFeedsOnly, onConfirmed }: RestoreHandlers & { resume: boolean }) {
  const uid = useId();
  const [file, setFile] = useState<File | null>(null);
  const [progress, setProgress] = useState<{ sent: number; total: number } | null>(null);
  const [summary, setSummary] = useState<UploadSummary | null>(null);
  const [held, setHeld] = useState(resume);
  const [mode, setMode] = useState<Mode>("everything");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [needsPw, setNeedsPw] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pwError, setPwError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const pwInput = useRef<HTMLInputElement>(null);

  // An upload still in flight when this screen goes away is stopped.
  useEffect(() => () => abort.current?.abort(), []);

  const backup = summary?.kind === "backup" ? summary : null;
  const opml = summary?.kind === "opml" ? summary : null;
  const uploading = progress !== null && summary === null && error === null;
  const passwordNeeded = needsPw || backup?.needs_new_password === true;
  const everything = mode === "everything" && !opml;

  const start = async (f: File) => {
    setFile(f);
    setError(null);
    setSummary(null);
    setProgress({ sent: 0, total: f.size });
    const ctl = new AbortController();
    abort.current = ctl;
    try {
      const s = await uploadRestoreFile(f, (sent, total) => setProgress({ sent, total }), ctl.signal);
      setSummary(s);
      setMode(s.kind === "backup" ? "everything" : "feeds");
    } catch (e) {
      if (e instanceof ApiError && e.status === 0 && e.code !== "aborted") setError(UPLOAD_FAILED);
      // 411 length_required, 400 upload_incomplete, 409 restore_cancelled and the rest carry a message written for the reader.
      else if (!(e instanceof ApiError && e.code === "aborted")) setError(restoreErrorText(e));
      setProgress(null);
      setFile(null);
    } finally {
      abort.current = null;
    }
  };

  const drop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    const f = e.dataTransfer.files[0];
    if (f && !uploading && !busy) void start(f);
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
    setSummary(null);
    setProgress(null);
    setFile(null);
    setHeld(false);
    setNeedsPw(false);
    setPassword("");
    setAgain("");
    setError(null);
    setPwError(null);
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
      if (err instanceof ApiError && err.code === "password_required") {
        setNeedsPw(true);
        setPwError("This backup needs a password. Choose one.");
        return;
      }
      if (err instanceof ApiError && err.code === "bad_new_password") {
        setPwError(restoreErrorText(err));
        pwInput.current?.focus();
        return;
      }
      setError(restoreErrorText(err));
    }
  };

  const picking = summary === null && !held && !uploading;
  return (
    <WizardFrame step={RESTORE_STEP} description="Restore a Kipple backup, or bring only the feeds from a backup or an OPML file.">
      <div className="flex flex-1 flex-col gap-5">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {picking ? (
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

        {uploading && file ? (
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

        {!picking && !uploading ? (
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
            {held && !summary ? <Notice>A backup you added earlier is still waiting on this Kipple. Continue with it, or cancel and choose another file.</Notice> : null}

            {opml ? (
              <p className="text-sm text-fg2">{FEEDS_HELP}</p>
            ) : (
              <fieldset className="flex min-w-0 flex-col gap-3">
                <legend className="mb-2 text-sm font-semibold">What do you want to bring back?</legend>
                <Choice id={`${uid}-all`} checked={mode === "everything"} onSelect={() => setMode("everything")} title="Everything" help={EVERYTHING_HELP} />
                <Choice id={`${uid}-feeds`} checked={mode === "feeds"} onSelect={() => setMode("feeds")} title="Feeds only" help={FEEDS_HELP} />
              </fieldset>
            )}

            {everything ? (
              <div className="flex flex-col gap-3">
                {passwordNeeded ? <Notice tone="warn" role="status">{passwordReason(backup)}</Notice> : null}
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
