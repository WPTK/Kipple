import { useState } from "react";
import { generateApiPassword, useSettings } from "@/api/admin";
import { useBootstrap } from "@/api/queries";
import { announce, toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, inputCls } from "@/ui/kit";
import { accountError } from "./AccountSection";

/** The path a Google Reader compatible app is pointed at. */
const SYNC_PATH = "/api/greader.php";

/** One value for a sync app's form, with the button that copies it. */
function CopyRow({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      announce(`${label} copied`);
    } catch {
      toast(`Couldn't copy. Select the ${label.toLowerCase()} and copy it by hand.`, "error");
    }
  };
  return (
    <div className="flex flex-col gap-1">
      <span className="text-sm font-semibold">{label}</span>
      <div className="flex items-center gap-2">
        <code data-testid={`sync-${label.toLowerCase()}`} className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 py-2 font-mono text-sm break-all select-all">
          {value}
        </code>
        <Button aria-label={`Copy ${label.toLowerCase()}`} onClick={() => void copy()}>
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
    </div>
  );
}

/** Makes a new app password, shown once with a Copy button. Asks for the web password first when the account has one. */
function AppPasswordDialog({ hasPassword, onClose }: { hasPassword: boolean; onClose: () => void }) {
  const [cur, setCur] = useState("");
  const [pw, setPw] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
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
      announce("App password copied");
    } catch {
      toast("Couldn't copy. Select the password and copy it by hand.", "error");
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={pw ? "Your new app password" : "Generate a new app password"}
      description={pw ? "Copy it now. Kipple can't show it again." : "Your sync apps are signed out until you enter the new one."}
      footer={
        pw ? (
          <Button variant="solid" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="solid" disabled={(hasPassword && !cur) || busy} onClick={() => void generate()}>
              Generate
            </Button>
          </>
        )
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {pw ? (
        <div className="flex items-center gap-2">
          <code data-testid="api-password" className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 py-2 font-mono text-sm break-all select-all">
            {pw}
          </code>
          <Button onClick={() => void copy()}>{copied ? "Copied" : "Copy"}</Button>
        </div>
      ) : hasPassword ? (
        <Field label="Your web password" help="Kipple asks for it before making a new app password.">
          {(a) => <input {...a} type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} className={inputCls} />}
        </Field>
      ) : null}
    </Modal>
  );
}

/**
 * Settings, Account: what a sync app needs, always on screen. The address is the public address when one is set (an app
 * on another device cannot use the one this browser happens to be on), else this page's own.
 */
export function SyncAppsSection({ hasPassword }: { hasPassword: boolean }) {
  const boot = useBootstrap();
  const settings = useSettings();
  const [open, setOpen] = useState(false);
  const publicUrl = settings.data?.settings.find((s) => s.key === "server.public_url")?.value;
  const base = typeof publicUrl === "string" && publicUrl.trim() !== "" ? publicUrl.trim().replace(/\/+$/, "") : window.location.origin;
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-fg2">In a sync app, add a Google Reader compatible account with this address and username, and an app password.</p>
      <CopyRow label="Address" value={`${base}${SYNC_PATH}`} />
      <CopyRow label="Username" value={boot.data?.user.username ?? ""} />
      <div>
        <Button onClick={() => setOpen(true)}>Generate new app password</Button>
        <p className="mt-1 text-xs text-fg2">A new app password signs your sync apps out until you enter it.</p>
      </div>
      {open ? <AppPasswordDialog hasPassword={hasPassword} onClose={() => setOpen(false)} /> : null}
    </div>
  );
}
