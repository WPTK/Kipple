import { useState } from "react";
import { useStore } from "@/lib/store";
import { generateApiPassword } from "@/api/admin";
import { useBootstrap } from "@/api/queries";
import { accountError } from "@/screens/AccountSection";
import { announce, toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Notice, inputCls } from "@/ui/kit";
import { StepActions, WizardFrame } from "./Frame";
import { apiPasswordMade, setupSecret } from "./session";
import { stepById } from "./steps";

/**
 * Step 6: done. Optionally makes the API password that sync apps (Reeder, NetNewsWire) sign in with, shown once with a
 * copy button, then Finish ends the wizard. For a password account the web password typed in step 1 is still in memory
 * and is used; after a reload it is asked for again, as Settings does. An account with no password (open mode) needs none.
 */
export function FinishStep({ onBack, onFinish, busy }: { onBack: () => void; onFinish: () => void; busy?: boolean }) {
  const boot = useBootstrap();
  const remembered = useStore(setupSecret);
  const madeBefore = useStore(apiPasswordMade);
  const user = boot.data?.user;
  // Until the account is known nothing is asked: an account without a password (open mode) must not see the field flash by.
  const known = user !== undefined;
  const hasPassword = user?.password_set !== false;
  const open = user?.auth_mode === "open";
  const [typed, setTyped] = useState("");
  const [pw, setPw] = useState<string | null>(null);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const server = `${window.location.origin}/api/greader.php`;

  const current = hasPassword ? (remembered ?? typed) : open ? undefined : "";
  const needsPassword = known && hasPassword && remembered === null;

  const generate = async () => {
    setWorking(true);
    setError(null);
    try {
      setPw((await generateApiPassword(current)).api_password);
      apiPasswordMade.set(true);
    } catch (e) {
      setError(accountError(e));
    } finally {
      setWorking(false);
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
    <WizardFrame step={stepById("finish")} description="Kipple is ready to read. One last thing, only if you want to use a phone or desktop reading app with it.">
      <div className="flex flex-1 flex-col gap-5">
        <section className="flex flex-col gap-3 rounded-xl border border-line p-4" aria-labelledby="api-heading">
          <h2 id="api-heading" className="text-lg font-bold">
            Connect a reading app (optional)
          </h2>
          <p className="text-sm text-fg2">Apps like Reeder and NetNewsWire sign in with a separate API password, so your web password never leaves this browser. You can make one now or later in Settings, Account &amp; Devices.</p>
          {error ? <Notice tone="error">{error}</Notice> : null}
          {pw ? (
            <div className="flex flex-col gap-3">
              <Notice>Copy it now. Kipple can't show it again.</Notice>
              <div className="flex items-center gap-2">
                <code data-testid="api-password" className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 py-2 font-mono text-sm break-all select-all">
                  {pw}
                </code>
                <Button onClick={() => void copy()}>{copied ? "Copied" : "Copy"}</Button>
              </div>
              <div className="text-sm">
                <p className="font-semibold">In Reeder or NetNewsWire</p>
                <p className="mt-1 text-fg2">
                  Add a FreshRSS (Google Reader compatible) account. Server URL: <span className="font-mono break-all text-fg">{server}</span>. Username: <span className="font-mono text-fg">{user?.username ?? ""}</span>. Password: the one above.
                </p>
              </div>
            </div>
          ) : (
            <>
              {madeBefore ? (
                <Notice tone="warn">
                  You already made an API password during this setup, and Kipple can't show it again. Making another one replaces it, and any app using the old one will need the new one.
                </Notice>
              ) : null}
              {needsPassword ? (
                <Field label="Your web password" help="Kipple asks for it before making an API password.">
                  {(a) => <input {...a} type="password" autoComplete="current-password" value={typed} onChange={(e) => setTyped(e.target.value)} className={inputCls} />}
                </Field>
              ) : null}
              <Button className="self-start" disabled={working || !known || (needsPassword && typed === "")} onClick={() => void generate()}>
                {working ? "Generating" : madeBefore ? "Replace API password" : "Generate API password"}
              </Button>
            </>
          )}
        </section>
        <StepActions back={<Button onClick={onBack}>Back</Button>}>
          <Button variant="solid" disabled={busy} onClick={onFinish}>
            {busy ? "Finishing" : "Finish"}
          </Button>
        </StepActions>
      </div>
    </WizardFrame>
  );
}
