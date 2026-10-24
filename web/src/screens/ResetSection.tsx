import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchResetInfo, resetKipple } from "@/api/admin";
import { wipeOfflineData } from "@/lib/offline";
import { UNCLAIMED_NOTICE } from "@/setup/RestoreWaiting";
import { openRestoreAfterReset, resetting } from "@/setup/session";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, PasswordField, inputCls } from "@/ui/kit";
import { accountError } from "./AccountSection";

/** The phrase that confirms a reset (the server compares it without regard to case or surrounding spaces). */
export const RESET_PHRASE = "reset kipple";

/** What the dialog is for: erasing and starting over, or erasing to restore a backup. Both end in the same setup screens. */
type Purpose = "reset" | "restore";

const TITLES: Record<Purpose, string> = { reset: "Reset Kipple and start over", restore: "Restore from a backup" };

function ResetDialog({ purpose, hasPassword, envAccount, onClose }: { purpose: Purpose; hasPassword: boolean; envAccount: boolean; onClose: () => void }) {
  const [password, setPassword] = useState("");
  const [phrase, setPhrase] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ready = phrase.trim().toLowerCase() === RESET_PHRASE && (!hasPassword || password !== "");
  const restoring = purpose === "restore";

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await resetKipple({ password, phrase: phrase.trim() });
      // The erased library's offline copies must not outlive it in this browser.
      await wipeOfflineData().catch(() => undefined);
      if (restoring) openRestoreAfterReset();
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
      title={TITLES[purpose]}
      description={
        restoring
          ? "A backup takes the place of everything in this Kipple: feeds, folders, reading history, settings and your account. Kipple erases your current library first and keeps a safety copy on the server, then asks you to choose the backup file."
          : "This erases all your feeds, folders, reading history, settings and your account, and returns Kipple to setup, where you create a new account or restore a backup."
      }
      footer={
        <>
          <Button disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="danger" disabled={!ready || busy} onClick={() => void submit()}>
            {restoring ? "Erase and choose a backup" : "Reset Kipple"}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <p className="text-sm text-fg2">
        Kipple keeps a safety copy of your library on the server. Export a backup first if you want one you can keep elsewhere. Your address and sign-in protection settings are kept.
      </p>
      <Notice tone="warn">{UNCLAIMED_NOTICE}</Notice>
      {envAccount ? <Notice>Until you create a new account, Kipple ignores the account set in your server&apos;s settings. You can remove it there whenever convenient.</Notice> : null}
      {hasPassword ? (
        <PasswordField label="Your web password" autoComplete="current-password" value={password} onChange={setPassword} />
      ) : null}
      <Field label={`Type "${RESET_PHRASE}" to confirm`}>
        {(a) => <input {...a} type="text" autoComplete="off" autoCapitalize="none" spellCheck={false} value={phrase} onChange={(e) => setPhrase(e.target.value)} className={inputCls} />}
      </Field>
    </Modal>
  );
}

/** Opens a ResetDialog once the server has said whether the account comes from its own settings. */
function useResetDialog(purpose: Purpose, hasPassword: boolean) {
  const [open, setOpen] = useState(false);
  const info = useQuery({ queryKey: ["reset", "info"], queryFn: fetchResetInfo, enabled: open, staleTime: 0, retry: false });
  return {
    start: () => setOpen(true),
    checking: open && info.isFetching,
    dialog: (
      <>
        {open && info.isSuccess ? <ResetDialog purpose={purpose} hasPassword={hasPassword} envAccount={info.data.env_account} onClose={() => setOpen(false)} /> : null}
        {open && info.isError ? (
          <Modal open onOpenChange={(o) => !o && setOpen(false)} title={TITLES[purpose]} footer={<Button onClick={() => setOpen(false)}>Close</Button>}>
            <Notice tone="error">{accountError(info.error)}</Notice>
          </Modal>
        ) : null}
      </>
    ),
  };
}

/** Settings, Backup and data: restoring a backup. Kipple restores only while it is in setup, so this first erases the library (a safety copy is kept). */
export function RestoreRow({ hasPassword }: { hasPassword: boolean }) {
  const r = useResetDialog("restore", hasPassword);
  return (
    <div>
      <Button onClick={r.start} disabled={r.checking}>
        {r.checking ? "Checking" : "Restore from a backup"}
      </Button>
      <p className="mt-1 text-xs text-fg2">Replaces everything here with a backup file. You confirm first.</p>
      {r.dialog}
    </div>
  );
}

/** Settings, Backup and data: the danger block that returns Kipple to setup. */
export function ResetSection({ hasPassword }: { hasPassword: boolean }) {
  const r = useResetDialog("reset", hasPassword);
  return (
    <section aria-labelledby="reset-heading" className="mt-2 flex flex-col gap-2 rounded-xl border border-danger px-4 py-3">
      <h3 id="reset-heading" className="text-sm font-semibold text-danger">
        Danger zone
      </h3>
      <p className="text-xs text-fg2">Erases your feeds, folders, history, settings and account, and starts over in setup. A safety copy is kept.</p>
      <Button variant="danger" className="self-start" onClick={r.start} disabled={r.checking}>
        {r.checking ? "Checking" : "Reset Kipple and start over"}
      </Button>
      {r.dialog}
    </section>
  );
}
