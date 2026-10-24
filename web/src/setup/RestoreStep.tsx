import { useEffect, useRef, useState, type DragEvent, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/button";
import { Notice, PasswordField } from "@/ui/kit";
import { MIN_PASSWORD_LEN, passwordProblem } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import {
  cancelRestore,
  confirmRestore,
  fetchRestoreStatus,
  restoreErrorText,
  sizeText,
  uploadRestoreFile,
  type BackupSummary,
  type RestoreStatus,
} from "./restoreApi";
import { useLatestRef } from "@/lib/useLatestRef";

const RESTORE_TITLE = "Restore from a backup";

/** A feeds file sent to the restore screen: the server reads it and keeps nothing, and this says where such a file goes. */
const NOT_A_BACKUP = "That is a feeds file, not a Kipple backup. Create your account, then use Import a feeds file in the feeds step.";

/** A network failure during the upload has no answer to quote: the usual causes are a full disk or a proxy size limit. */
const UPLOAD_FAILED = "The upload was refused or interrupted. Check that the server has enough free disk space and that any proxy in front of Kipple allows a file this size.";

/**
 * The server holds no upload of this page's any more: another browser's upload replaced it (the server's message says
 * so and asks for the file again), or it expired. The summary on screen is stale, so the page goes back to choosing a
 * file.
 */
const uploadGone = (e: unknown): boolean => e instanceof ApiError && (e.code === "restore_replaced" || e.code === "no_upload");

/** How often the page asks the server whether its check of the backup is done. */
export const CHECK_POLL_MS = 2000;

/** pick: choosing a file. sending: the upload runs. waiting: the server holds an upload (receiving or checking it). review: a backup is ready to confirm. */
type View = "pick" | "sending" | "waiting" | "review";

/** What the restore screen hands on. */
export interface RestoreHandlers {
  onBack: () => void;
  /** Opened from the setup account form, so it counts as step 1 of the wizard; from Settings there is no wizard and no counter. */
  inWizard: boolean;
  /** The restore was confirmed and the server is restarting. `username` is null when this page never saw the backup. */
  onConfirmed: (info: { estimateSeconds: number; username: string | null }) => void;
}

/** Why a backup needs a password chosen here, in the reader's words. */
function passwordReason(s: BackupSummary): string {
  if (s.new_password_reason === "open_refused") return "This backup has no password, and Kipple doesn't allow that from this address. Choose a password for it.";
  if (s.new_password_reason === "access_unavailable") return "This backup relies on Cloudflare Access to sign in, and Kipple can't see your Access sign-in from this page. Choose a password to use instead.";
  return "This backup needs a password to sign in from this page. Choose one.";
}

/**
 * Before the account exists: restore a Kipple backup (a zip). The file goes to the server as it is, with a progress
 * bar; the server then checks it, which this page waits for, and shows what is in it. Confirming makes the server replace
 * itself with the backup and restart. Nothing can be confirmed until the backup's contents are on screen.
 * `resume`: the server already holds a restore from before a reload, and this page asks it where it is.
 */
export function RestoreStep({ resume, inWizard, onBack, onConfirmed }: RestoreHandlers & { resume: boolean }) {
  const [view, setView] = useState<View>(resume ? "waiting" : "pick");
  const viewNow = useLatestRef(view);
  const [file, setFile] = useState<File | null>(null);
  const [progress, setProgress] = useState<{ sent: number; total: number } | null>(null);
  const [serverState, setServerState] = useState<"uploading" | "checking">("checking");
  const [backup, setBackup] = useState<BackupSummary | null>(null);
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busyUpload, setBusyUpload] = useState(false);
  const [pwError, setPwError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [over, setOver] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const pwInput = useRef<HTMLInputElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const handoff = useLatestRef({ onConfirmed });

  // An upload still in flight when this screen goes away is stopped.
  useEffect(() => () => abort.current?.abort(), []);

  const passwordNeeded = backup?.needs_new_password === true;

  /** Shows what the server says about its restore. */
  const apply = (st: RestoreStatus) => {
    if (st.state === "ready" && st.summary) {
      setBackup(st.summary);
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
    setProgress({ sent: 0, total: f.size });
    setView("sending");
    const ctl = new AbortController();
    abort.current = ctl;
    try {
      const r = await uploadRestoreFile(f, (sent, total) => setProgress({ sent, total }), ctl.signal);
      if ("kind" in r) {
        setError(NOT_A_BACKUP);
        setFile(null);
        setProgress(null);
        setView("pick");
      } else {
        setServerState("checking");
        setView("waiting");
      }
    } catch (e) {
      if (e instanceof ApiError && e.code === "aborted") return;
      let busy = e instanceof ApiError && e.code === "restore_busy";
      if (e instanceof ApiError && e.status === 0) {
        // The connection was cut. If the server stopped the upload (too slow), it says so in the status.
        const st = await fetchRestoreStatus().catch(() => null);
        // The server refuses an upload it cannot take before it reads the file and closes the connection, which the
        // browser reports as a cut one: the status carries the refusal in the server's own words.
        busy = st?.refusal?.code === "restore_busy";
        setError(st?.refusal?.message ? st.refusal.message : st?.state === "failed" && st.error?.message ? st.error.message : UPLOAD_FAILED);
      }
      // 411 length_required, 400 upload_incomplete, 409 restore_cancelled and the rest carry a message written for the reader.
      else setError(restoreErrorText(e));
      setBusyUpload(busy);
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

  /** Forgets the upload on screen, keeping the error, and shows the file picker. */
  const backToPick = () => {
    setBackup(null);
    setFile(null);
    setProgress(null);
    setPassword("");
    setView("pick");
  };

  /** Throws the upload away (on the server too) and goes back to choosing a file. */
  const discard = async () => {
    abort.current?.abort();
    setBusy(true);
    try {
      await cancelRestore();
    } catch (e) {
      // 409 restore_pending: already confirmed, so it cannot be cancelled. Any other failure (another browser's upload
      // replaced this page's, say) leaves nothing here to cancel.
      if (e instanceof ApiError && e.code === "restore_pending") {
        setError(restoreErrorText(e));
        setBusy(false);
        return;
      }
    }
    setBusy(false);
    setError(null);
    setBusyUpload(false);
    setPwError(null);
    backToPick();
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setPwError(null);
    const wants = passwordNeeded || password !== "";
    if (wants) {
      const problem = password === "" ? "Enter a password." : passwordProblem(password);
      if (problem) {
        setPwError(problem);
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
      if (uploadGone(err)) backToPick();
    }
  };

  return (
    <WizardFrame step={{ id: "restore", n: inWizard ? 1 : 0, title: RESTORE_TITLE }} description="Restore a Kipple backup: your feeds and folders, read and starred items, settings and account.">
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
              <input
                ref={fileInput}
                type="file"
                aria-label="Backup file"
                tabIndex={-1}
                className="hidden"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  e.target.value = "";
                  if (f) void start(f);
                }}
              />
              <div className="flex flex-wrap items-center gap-3">
                <Button variant="solid" className="min-h-11" onClick={() => fileInput.current?.click()}>
                  Choose a backup file
                </Button>
                <span className="text-sm text-fg2">or drop it here</span>
              </div>
              <p className="text-sm text-fg2">Images and icons are downloaded again the first time you view them.</p>
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
              {serverState === "uploading" ? "A backup is being uploaded from this browser." : "Checking your backup..."}
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
            {backup ? (
              <div className="flex flex-col gap-3">
                {passwordNeeded ? (
                  <Notice tone="warn" role="status">
                    {passwordReason(backup)}
                  </Notice>
                ) : null}
                <PasswordField
                  label={passwordNeeded ? "New password" : "Set a new password (optional)"}
                  help={passwordNeeded ? `At least ${MIN_PASSWORD_LEN} characters.` : "Leave empty to keep the password in the backup."}
                  error={pwError}
                  inputRef={pwInput}
                  name="new-password"
                  autoComplete="new-password"
                  required={passwordNeeded}
                  value={password}
                  onChange={(v) => {
                    setPassword(v);
                    setPwError(null);
                  }}
                />
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
                {busy ? "Working" : "Restore this backup"}
              </Button>
            </StepActions>
          </form>
        ) : null}
      </div>
    </WizardFrame>
  );
}
