import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchResetInfo, resetKipple } from "@/api/admin";
import { wipeOfflineData } from "@/lib/offline";
import { UNCLAIMED_NOTICE } from "@/setup/RestoreWaiting";
import { resetting } from "@/setup/session";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, inputCls } from "@/ui/kit";
import { accountError } from "./AccountSection";

/** The phrase that confirms a reset (the server compares it without regard to case or surrounding spaces). */
export const RESET_PHRASE = "reset kipple";

function ResetDialog({ hasPassword, envAccount, onClose }: { hasPassword: boolean; envAccount: boolean; onClose: () => void }) {
  const [password, setPassword] = useState("");
  const [phrase, setPhrase] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ready = phrase.trim().toLowerCase() === RESET_PHRASE && (!hasPassword || password !== "");

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await resetKipple({ password, phrase: phrase.trim() });
      // The erased library's offline copies must not outlive it in this browser.
      await wipeOfflineData().catch(() => undefined);
      resetting.set({ estimateSeconds: r.estimate_seconds });
    } catch (e) {
      setError(accountError(e));
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title="Reset Kipple and start over"
      description="This erases all your feeds, folders, reading history, settings and your account, and returns Kipple to setup, where you create a new account or restore a backup."
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="solid" disabled={!ready || busy} onClick={() => void submit()}>
            Reset Kipple
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <p className="text-sm text-fg2">
        Kipple keeps a safety copy of your library in <code>backup/pre-restore-*</code> in your data folder, and <code>kipple restore</code> can bring it back. Export a backup first if you want one you can keep elsewhere. The address and access settings (public address, allowed host names, trusted proxies, Cloudflare Access) are kept.
      </p>
      <Notice tone="warn">{UNCLAIMED_NOTICE}</Notice>
      {envAccount ? (
        <Notice>Kipple will ignore KIPPLE_USERNAME and KIPPLE_PASSWORD until you create a new account. You can delete them from your compose file whenever convenient.</Notice>
      ) : null}
      {hasPassword ? (
        <Field label="Your web password">{(a) => <input {...a} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} className={inputCls} />}</Field>
      ) : null}
      <Field label={`Type "${RESET_PHRASE}" to confirm`}>
        {(a) => <input {...a} type="text" autoComplete="off" autoCapitalize="none" spellCheck={false} value={phrase} onChange={(e) => setPhrase(e.target.value)} className={inputCls} />}
      </Field>
    </Modal>
  );
}

/** Settings, Account & Devices: the danger section that returns Kipple to setup. */
export function ResetSection({ hasPassword }: { hasPassword: boolean }) {
  const [open, setOpen] = useState(false);
  const info = useQuery({ queryKey: ["reset", "info"], queryFn: fetchResetInfo, enabled: open, staleTime: 0, retry: false });
  return (
    <section aria-labelledby="reset-heading" className="mt-2 flex flex-col gap-2 rounded-xl border border-danger px-4 py-3">
      <h3 id="reset-heading" className="text-sm font-semibold text-danger">
        Reset Kipple
      </h3>
      <p className="text-xs text-fg2">Erases your feeds, folders, history, settings and account, and starts over in setup. A safety copy is kept.</p>
      <Button className="self-start" onClick={() => setOpen(true)}>
        Reset Kipple and start over
      </Button>
      {open && info.isSuccess ? <ResetDialog hasPassword={hasPassword} envAccount={info.data.env_account} onClose={() => setOpen(false)} /> : null}
      {open && info.isError ? (
        <Modal open onOpenChange={(o) => !o && setOpen(false)} title="Reset Kipple and start over" footer={<Button onClick={() => setOpen(false)}>Close</Button>}>
          <Notice tone="error">{accountError(info.error)}</Notice>
        </Modal>
      ) : null}
    </section>
  );
}
