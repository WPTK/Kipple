import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, authStore, busyMessage, CAUSE_PHRASE, chooseSignOut, errorMessage, failed, SIGN_OUT_MS, SIGN_OUT_PATH } from "@/api/client";
import { changePassword, exportBackup, fetchMe, removePassword, useHealth, type BackupInfo } from "@/api/admin";
import { useBootstrap } from "@/api/queries";
import { keys } from "@/api/queryKeys";
import { bytesLabel, fullDate } from "@/lib/format";
import { wipeOfflineData } from "@/lib/offline";
import { buttonVariants } from "@/ui/button";
import { Button } from "@/ui/button";
import { Modal, Notice, PasswordField } from "@/ui/kit";
import { cn } from "@/lib/cn";
import { announce, toast } from "@/shell/toasts";
import { useSetupActions } from "@/setup/actions";
import { MIN_PASSWORD_LEN } from "@/setup/api";
import { ResetSection, RestoreRow } from "./ResetSection";

/** Message for a failed account or backup call. */
export function accountError(e: unknown): string {
  if (e instanceof ApiError) {
    const msg = typeof e.body?.message === "string" ? e.body.message : "";
    if (e.code === "bad_password") return "The current password isn't right.";
    if (e.code === "access_required") return "This needs your Cloudflare Access sign-in. Open Kipple through its Access address and try again.";
    if (e.code === "access_changed")
      return "The Cloudflare Access settings changed while this was being checked. Open Kipple through Access again and try again.";
    if (e.code === "access_unavailable") return "Kipple can't check your Cloudflare Access sign-in right now. Try again in a moment.";
    if (e.code === "access_not_configured")
      // The server words it for the case: removing a password, or an account without one.
      return msg ? `${msg.charAt(0).toUpperCase()}${msg.slice(1)}.` : "Cloudflare Access validation isn't set up on the server.";
    if (e.code === "bad_new_password") return msg || `The new password must be ${MIN_PASSWORD_LEN} to 256 characters.`;
    // A sign-in or password check that could not get its turn (a busy verifier, or many people on one address): nothing was checked.
    if (e.status === 503 && e.code === "busy") return busyMessage(e);
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

/** Change the web password, or set one when the account has none (the server then checks the Access sign-in instead). */
function ChangePasswordDialog({ hasPassword, onDone, onClose }: { hasPassword: boolean; onDone: () => void; onClose: () => void }) {
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const tooShort = next.length > 0 && next.length < MIN_PASSWORD_LEN;
  const ok = (cur || !hasPassword) && next.length >= MIN_PASSWORD_LEN && next.length <= 256;
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await changePassword(cur, next);
      toast(hasPassword ? "Password changed. Other devices are signed out." : "Password set. Other devices are signed out.");
      onDone();
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
      title={hasPassword ? "Change web password" : "Set web password"}
      description="Your sync apps use a separate app password."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="solid" disabled={!ok || busy} onClick={() => void submit()}>
            {hasPassword ? "Change password" : "Set password"}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {hasPassword ? (
        <PasswordField label="Current password" autoComplete="current-password" value={cur} onChange={setCur} />
      ) : null}
      <PasswordField label="New password" help={`${MIN_PASSWORD_LEN} to 256 characters.`} error={tooShort ? `Use at least ${MIN_PASSWORD_LEN} characters.` : null} autoComplete="new-password" value={next} onChange={setNext} />
    </Modal>
  );
}

/** Remove the web password: sign-in then works only through Cloudflare Access (the server insists on a verified Access sign-in). */
function RemovePasswordDialog({ email, onDone, onClose }: { email: string; onDone: () => void; onClose: () => void }) {
  const [cur, setCur] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await removePassword(cur);
      toast("Password removed. Other devices are signed out.");
      onDone();
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
      title="Remove web password"
      description={`You will sign in through Cloudflare Access (${email}) with no password. Signing in from an address that skips Access, such as your home network, won't work until you set a password again.`}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="solid" disabled={!cur || busy} onClick={() => void submit()}>
            Remove password
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <PasswordField label="Current password" autoComplete="current-password" value={cur} onChange={setCur} />
    </Modal>
  );
}

/**
 * Build the export, show what is in it, then let the browser's save dialog take over. Without `ready` it first asks
 * for the web password (the file holds the account's credentials) and builds the export itself; an account without
 * a web password has nothing to type, so its export is built before the dialog opens, as for the app password.
 */
function BackupDialog({ ready, onClose }: { ready: BackupInfo | null; onClose: () => void }) {
  const [info, setInfo] = useState(ready);
  const [cur, setCur] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Cancel, Escape or leaving the page stops the polling (the server finishes the build on its own).
  const ctrl = useRef<AbortController | null>(null);
  useEffect(() => {
    const c = new AbortController();
    ctrl.current = c;
    return () => c.abort();
  }, []);
  // When the password step turns into the download step, its controls go away: focus the download link and say so.
  const link = useRef<HTMLAnchorElement>(null);
  const built = useRef(false);
  useEffect(() => {
    if (!info || !built.current) return;
    link.current?.focus();
    announce("Backup ready to download");
  }, [info]);
  const build = async () => {
    if (busy || !cur) return;
    const signal = ctrl.current?.signal;
    setBusy(true);
    setError(null);
    try {
      const ans = await exportBackup({ current: cur, signal });
      built.current = true;
      setInfo(ans);
    } catch (e) {
      if (signal?.aborted) return;
      setError(accountError(e));
    } finally {
      setBusy(false);
    }
  };
  if (!info) {
    return (
      <Modal
        open
        onOpenChange={(o) => !o && onClose()}
        title="Export backup"
        description="The backup holds your account's passwords and sign-in secrets, so Kipple asks for your web password first."
        footer={
          <>
            <Button onClick={onClose}>Cancel</Button>
            {/* aria-disabled while busy, not disabled: a disabled button drops the keyboard's place in the dialog. */}
            <Button variant="solid" disabled={!cur} aria-disabled={busy || undefined} onClick={() => void build()}>
              {busy ? "Preparing backup" : "Export"}
            </Button>
          </>
        }
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
        <PasswordField label="Your web password" autoComplete="current-password" value={cur} onChange={setCur} onKeyDown={(e) => e.key === "Enter" && void build()} />
      </Modal>
    );
  }
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
          <a ref={link} href={info.url} download={info.filename} onClick={() => setTimeout(onClose, 500)} className={cn(buttonVariants({ variant: "solid" }))}>
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
        <dd>{fullDate(Date.parse(c.created_at) / 1000)}</dd>
      </dl>
      <p className="text-sm text-fg2">
        The link works once and expires in {Math.round(info.expires_in / 60)} minutes.
      </p>
    </Modal>
  );
}

/** The signed-in account as the server reports it right now. */
export function useAccount() {
  const boot = useBootstrap();
  const qc = useQueryClient();
  const user = boot.data?.user;
  // Live account state (never the offline copy): the password and Access controls follow it. Until it answers, and
  // when it fails (offline), the bootstrap's password_set stands in and no Access sign-in is assumed. An older server
  // does not send password_set: treat the password as set.
  // The server shows the email only once its cached Access key set can check the token (it never waits for Cloudflare),
  // so a null right after a start or a key rotation is asked again a few times. Window focus does not refetch here.
  const me = useQuery({
    queryKey: keys.me,
    queryFn: fetchMe,
    staleTime: 0,
    retry: false,
    refetchInterval: (q) => (q.state.data?.access_enabled && !q.state.data.access_email && q.state.dataUpdateCount < 4 ? 5_000 : false),
  });
  const hasPassword = (me.data?.password_set ?? user?.password_set) !== false;
  const accessEmail = me.data?.access_email ?? null;
  const refreshUser = () => {
    void qc.invalidateQueries({ queryKey: keys.me });
    void qc.invalidateQueries({ queryKey: keys.bootstrap });
  };
  return { user, hasPassword, accessEmail, refreshUser };
}

/** Settings, Account, Sign-in: who is signed in, sign out, and the web password. */
export function SignInSection() {
  const boot = useBootstrap();
  const qc = useQueryClient();
  const [dialog, setDialog] = useState<"password" | "remove" | null>(null);
  const { user, hasPassword, accessEmail, refreshUser } = useAccount();
  const signOut = async () => {
    chooseSignOut(true);
    // A sign-out that gets no answer is given up, like a network failure, so the app does not stay on hold.
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), SIGN_OUT_MS);
    try {
      await api(SIGN_OUT_PATH, { method: "POST", signal: ctl.signal });
    } catch (e) {
      // 401: the session had ended already, so the sign-out goes on.
      if (!(e instanceof ApiError && e.status === 401 && e.code === "auth")) {
        chooseSignOut(false);
        toast(ctl.signal.aborted ? `Couldn't sign out. ${CAUSE_PHRASE.unreachable}.` : failed("sign out", e), "error");
        return;
      }
    } finally {
      clearTimeout(timer);
    }
    try {
      // Clearing the cache ends any reading session (the setting reads unknown); one that stops after the wipe below
      // is dropped by the stats sender, which queues nothing from the wipe until the next sign-in.
      qc.clear();
      // Wait for the copies to be gone before the sign-in screen: the next person at this browser must not be able
      // to read them offline, and a request answered meanwhile must not find them still there.
      await wipeOfflineData();
    } catch (e) {
      // The sign-in screen has no toasts to say it in, and the session is over whatever happened here.
      console.error("Kipple could not delete all of this device's offline copies at sign-out", e);
    } finally {
      // The server has ended the session either way.
      authStore.set("out");
      chooseSignOut(false);
    }
  };

  return (
    <>
      <p className="text-sm text-fg2">
        {boot.data ? `Signed in as ${boot.data.user.username}.` : "Signed in."}
        {accessEmail ? ` Cloudflare Access: ${accessEmail}.` : ""}
        {hasPassword ? "" : user?.auth_mode === "open" ? " No password: Kipple opens without signing in, from this computer, your local network and over Tailscale." : " No web password: you sign in through Cloudflare Access."}
      </p>
      <div className="flex flex-wrap gap-2">
        {/* Without a password there is nothing to sign out of: Kipple would sign this browser straight back in. */}
        {user?.auth_mode === "open" ? null : <Button variant="danger" onClick={() => void signOut()}>Sign out</Button>}
        <Button onClick={() => setDialog("password")}>{hasPassword ? "Change web password" : "Set web password"}</Button>
        {hasPassword && accessEmail ? <Button onClick={() => setDialog("remove")}>Remove web password</Button> : null}
      </div>
      {dialog === "password" ? <ChangePasswordDialog hasPassword={hasPassword} onDone={refreshUser} onClose={() => setDialog(null)} /> : null}
      {dialog === "remove" && accessEmail ? <RemovePasswordDialog email={accessEmail} onDone={refreshUser} onClose={() => setDialog(null)} /> : null}
    </>
  );
}

/** What Kipple keeps on the server, so a person sizing a backup or a disk can see it: the database, the backups, the image cache and the unread total. */
function StorageFigures() {
  const health = useHealth();
  const d = health.data;
  if (!d) return null;
  const rows: [string, string][] = [
    ["Database", bytesLabel(d.db.db_bytes + (d.db.wal_bytes ?? 0))],
    ["Backups kept on the server", bytesLabel(d.db.backup_bytes)],
    ...(typeof d.db.imgcache_bytes === "number" ? ([["Image cache", bytesLabel(d.db.imgcache_bytes)]] as [string, string][]) : []),
    ["Unread articles", d.unread_total.toLocaleString()],
  ];
  return (
    <div>
      <h3 className="text-sm font-semibold">Storage used</h3>
      <dl data-testid="storage-figures" className="mt-1 grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5 text-sm">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-fg2">{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/** Settings, Account, Backup and data: export, restore, setup again, and the reset in its own danger block. */
export function BackupSection() {
  const { hasPassword } = useAccount();
  const setupActions = useSetupActions();
  const [dialog, setDialog] = useState<"backup" | null>(null);
  const [backup, setBackup] = useState<BackupInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const [backupError, setBackupError] = useState<string | null>(null);

  const doBackup = async () => {
    if (busy) return;
    setBackupError(null);
    if (hasPassword) {
      setDialog("backup");
      return;
    }
    setBusy(true);
    try {
      setBackup(await exportBackup());
    } catch (e) {
      setBackupError(accountError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <StorageFigures />
      <div>
        {/* aria-disabled, not disabled: a disabled button drops the keyboard's place, so the dialog it opens would have nothing to return focus to. */}
        <Button aria-disabled={busy || undefined} onClick={() => void doBackup()}>
          {busy ? "Preparing backup" : "Export backup"}
        </Button>
        <p className="mt-1 text-xs text-fg2">Your feeds, articles, read and starred state, and settings, in one file.</p>
        {backupError ? (
          <div className="mt-2">
            <Notice tone="error">{backupError}</Notice>
          </div>
        ) : null}
      </div>
      <RestoreRow hasPassword={hasPassword} />
      <div>
        <Button onClick={() => setupActions.restart()}>Run setup again</Button>
        <p className="mt-1 text-xs text-fg2">Your account and feeds stay as they are.</p>
      </div>
      <ResetSection hasPassword={hasPassword} />
      {dialog === "backup" ? <BackupDialog ready={null} onClose={() => setDialog(null)} /> : null}
      {backup ? <BackupDialog ready={backup} onClose={() => setBackup(null)} /> : null}
    </>
  );
}
