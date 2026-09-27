import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, api, authStore, errorMessage } from "@/api/client";
import { applyRetention, changePassword, exportBackup, generateApiPassword, type BackupInfo } from "@/api/admin";
import { useBootstrap } from "@/api/queries";
import { bytesLabel, fullDate } from "@/lib/format";
import { wipeOfflineData } from "@/lib/offline";
import { buttonVariants } from "@/ui/button";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, inputCls } from "@/ui/kit";
import { cn } from "@/lib/cn";
import { announce, toast } from "@/shell/toasts";

/** Message for a failed account or backup call. */
export function accountError(e: unknown): string {
  if (e instanceof ApiError) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    if (e.code === "bad_password") return "The current password isn't right.";
    if (e.code === "bad_new_password") return msg || "The new password must be 5 to 256 characters.";
    if (e.status === 429) return "Too many attempts. Try again in a few minutes.";
    if (e.status === 409 && e.code === "busy") {
      const s = typeof e.body?.retry_after === "number" ? ` Try again in about ${e.body.retry_after} seconds.` : " Try again in a moment.";
      return `Kipple is busy with a database snapshot or another export.${s}`;
    }
    if (e.status === 507) return "The server doesn't have enough free disk space to build a backup. Free some space and try again.";
    if (e.code === "gone" || e.status === 404) return "The backup is no longer available. Export again.";
    if (e.code === "timeout") return "The backup is taking too long. Check the server and try again.";
    if (e.status === 413) return "The backup would be larger than 4 GiB, which is more than Kipple will export in one file.";
  }
  return errorMessage(e);
}

function ChangePasswordDialog({ onClose }: { onClose: () => void }) {
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const tooShort = next.length > 0 && next.length < 5;
  const mismatch = again.length > 0 && again !== next;
  const ok = cur && next.length >= 5 && next.length <= 256 && next === again;
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await changePassword(cur, next);
      toast("Password changed. Other devices are signed out.");
      onClose();
    } catch (e) {
      setError(accountError(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title="Change web password"
      description="This is the password you use to sign in to Kipple in a browser. Your sync apps use a separate API password."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="solid" disabled={!ok || busy} onClick={() => void submit()}>
            Change password
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <Field label="Current password">{(a) => <input {...a} type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} className={inputCls} />}</Field>
      <Field label="New password" help="5 to 256 characters." error={tooShort ? "Use at least 5 characters." : null}>
        {(a) => <input {...a} type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} className={inputCls} />}
      </Field>
      <Field label="New password again" error={mismatch ? "The passwords don't match." : null}>
        {(a) => <input {...a} type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} className={inputCls} />}
      </Field>
    </Modal>
  );
}

function ApiPasswordDialog({ username, onClose }: { username: string; onClose: () => void }) {
  const [cur, setCur] = useState("");
  const [pw, setPw] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const server = `${window.location.origin}/api/greader.php`;
  const generate = async () => {
    setBusy(true);
    setError(null);
    try {
      setPw((await generateApiPassword(cur)).api_password);
    } catch (e) {
      setError(accountError(e));
    } finally {
      setBusy(false);
    }
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(pw ?? "");
      setCopied(true);
      announce("API password copied");
    } catch {
      toast("Couldn't copy. Select the password and copy it by hand.", "error");
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={pw ? "Your new API password" : "Generate API password"}
      description={pw ? "Copy it now. Kipple can't show it again." : "Sync apps such as Reeder and NetNewsWire sign in with this. Generating a new one signs those apps out until you enter it."}
      footer={
        pw ? (
          <Button variant="solid" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="solid" disabled={!cur || busy} onClick={() => void generate()}>
              Generate
            </Button>
          </>
        )
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {pw ? (
        <>
          <div className="flex items-center gap-2">
            <code data-testid="api-password" className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 py-2 font-mono text-sm break-all select-all">
              {pw}
            </code>
            <Button onClick={() => void copy()}>{copied ? "Copied" : "Copy"}</Button>
          </div>
          <div className="text-sm">
            <p className="font-semibold">In Reeder or NetNewsWire</p>
            <p className="mt-1 text-fg2">
              Add a FreshRSS (Google Reader compatible) account. Server URL: <span className="font-mono break-all text-fg">{server}</span>. Username: <span className="font-mono text-fg">{username}</span>. Password: the one above.
            </p>
          </div>
        </>
      ) : (
        <Field label="Your web password" help="Kipple asks for it before creating a new API password.">
          {(a) => <input {...a} type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} className={inputCls} />}
        </Field>
      )}
    </Modal>
  );
}

/** Build the export, show what is in it, then let the browser's save dialog take over. */
function BackupDialog({ info, onClose }: { info: BackupInfo; onClose: () => void }) {
  const c = info.contents;
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title="Download backup"
      description={info.warning}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <a href={info.url} download={info.filename} onClick={() => setTimeout(onClose, 500)} className={cn(buttonVariants({ variant: "solid" }))}>
            Download {info.filename}
          </a>
        </>
      }
    >
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt className="text-fg2">Size</dt>
        <dd>{bytesLabel(info.bytes)}</dd>
        <dt className="text-fg2">Feeds</dt>
        <dd>{c.feeds}</dd>
        <dt className="text-fg2">Articles</dt>
        <dd>
          {c.items} ({c.starred} starred)
        </dd>
        <dt className="text-fg2">Database</dt>
        <dd>{bytesLabel(c.db_bytes)}</dd>
        <dt className="text-fg2">Kipple version</dt>
        <dd>
          {c.kipple_version} (schema {c.schema_version})
        </dd>
        <dt className="text-fg2">Made</dt>
        <dd>{fullDate(c.created_at)}</dd>
      </dl>
      <p className="text-sm text-fg2">
        Your browser will ask where to save it. The link works once and expires in {Math.round(info.expires_in / 60)} minutes.
      </p>
    </Modal>
  );
}

/** Account actions: passwords, backup, retention, sign out. */
export function AccountActions() {
  const boot = useBootstrap();
  const qc = useQueryClient();
  const [dialog, setDialog] = useState<"password" | "api" | null>(null);
  const [backup, setBackup] = useState<BackupInfo | null>(null);
  const [busy, setBusy] = useState<"backup" | "retention" | null>(null);
  const [backupError, setBackupError] = useState<string | null>(null);

  const doBackup = async () => {
    setBusy("backup");
    setBackupError(null);
    try {
      setBackup(await exportBackup());
    } catch (e) {
      setBackupError(accountError(e));
    } finally {
      setBusy(null);
    }
  };
  const doRetention = async () => {
    setBusy("retention");
    try {
      await applyRetention();
      toast("Kipple is trimming feeds to their retention limits now.");
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(null);
    }
  };
  const signOut = async () => {
    try {
      await api("/api/auth/logout", { method: "POST" });
    } catch (e) {
      toast(errorMessage(e), "error");
      return;
    }
    // Clearing the cache ends any reading session (the setting reads unknown); one that stops after the wipe below
    // is dropped by the stats sender, which queues nothing from the wipe until the next sign-in.
    qc.clear();
    // Wait for the copies to be gone before the sign-in screen: the next person at this browser must not be able
    // to read them offline, and a request answered meanwhile must not find them still there.
    await wipeOfflineData();
    authStore.set("out");
  };

  return (
    <>
      <p className="text-sm text-fg2">
        {boot.data ? `Signed in as ${boot.data.user.username}.` : "Signed in."}
        {boot.data ? ` Kipple ${boot.data.version}.` : ""}
      </p>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setDialog("password")}>Change web password</Button>
        <Button onClick={() => setDialog("api")}>Generate API password</Button>
      </div>
      <div>
        <Button disabled={busy === "backup"} onClick={() => void doBackup()}>
          {busy === "backup" ? "Preparing backup" : "Export backup"}
        </Button>
        <p className="mt-1 text-xs text-fg2">Saves everything (feeds, articles, read and starred state, settings) as one file you choose where to keep.</p>
        {backupError ? (
          <div className="mt-2">
            <Notice tone="error">{backupError}</Notice>
          </div>
        ) : null}
      </div>
      <div>
        <Button disabled={busy === "retention"} onClick={() => void doRetention()}>
          Apply retention now
        </Button>
        <p className="mt-1 text-xs text-fg2">Trims every feed to its "articles to keep" limit right away. Starred articles are never removed.</p>
      </div>
      <Button onClick={() => void signOut()} className="self-start">
        Sign out
      </Button>
      {dialog === "password" ? <ChangePasswordDialog onClose={() => setDialog(null)} /> : null}
      {dialog === "api" ? <ApiPasswordDialog username={boot.data?.user.username ?? ""} onClose={() => setDialog(null)} /> : null}
      {backup ? <BackupDialog info={backup} onClose={() => setBackup(null)} /> : null}
    </>
  );
}
