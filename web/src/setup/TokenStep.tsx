import { useRef, useState, type FormEvent } from "react";
import { Button } from "@/ui/button";
import { Disclosure, Field, Notice, inputCls } from "@/ui/kit";
import { claimError, claimSetup } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import { stepById } from "./steps";

/**
 * Step 1: the setup code Kipple printed when it started. Whoever can read the server's log can finish setup, so this
 * is what keeps a stranger who reaches the address first from taking the account. A link that carries the code
 * (`#setup=<code>`) fills it in; the caller has already removed it from the address bar.
 */
export function TokenStep({ initialCode = "", issuedAt, notice, onClaimed }: { initialCode?: string; issuedAt?: number; notice?: string; onClaimed: () => void }) {
  const [code, setCode] = useState(initialCode);
  const [fromLink, setFromLink] = useState(initialCode !== "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!code.trim()) {
      setError("Enter the setup code first.");
      input.current?.focus();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await claimSetup(code.trim());
      onClaimed();
    } catch (err) {
      setError(claimError(err));
      input.current?.focus();
    } finally {
      setBusy(false);
    }
  };

  const issued = issuedAt ? new Date(issuedAt * 1000).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }) : null;

  return (
    <WizardFrame step={stepById("token")} description="Kipple has no account yet. To make sure only you can create it, it printed a one-time setup code when it started.">
      <form onSubmit={(e) => void submit(e)} className="flex flex-1 flex-col gap-4" noValidate>
        {!error && notice ? <Notice tone="warn">{notice}</Notice> : null}
        {fromLink && !error ? <Notice>The code from your link is filled in. Press Continue.</Notice> : null}
        <Field label="Setup code" help={issued ? `Spaces, dashes and capital letters don't matter. This code was made ${issued}.` : "Spaces, dashes and capital letters don't matter."} error={error}>
          {(a) => (
            <input
              {...a}
              ref={input}
              name="setup-code"
              autoComplete="off"
              autoCapitalize="characters"
              autoCorrect="off"
              spellCheck={false}
              value={code}
              onChange={(e) => {
                setCode(e.target.value);
                setFromLink(false);
              }}
              className={`${inputCls} font-mono tracking-wider`}
            />
          )}
        </Field>
        <Disclosure label="Where do I find the code?">
          <p className="text-sm text-fg2">Kipple prints it in its log every time it starts without an account. Use whichever of these fits how you run Kipple:</p>
          <ul className="flex list-disc flex-col gap-2 pl-5 text-sm">
            <li>
              With Docker Compose: <code className="font-mono break-all">docker compose logs kipple</code>
            </li>
            <li>
              With docker run: <code className="font-mono break-all">docker logs &lt;container name&gt;</code>
            </li>
            <li>
              To show it again at any time: <code className="font-mono break-all">docker exec &lt;container name&gt; /kipple setup-token</code>, or just <code className="font-mono">kipple setup-token</code> when you run Kipple without Docker.
            </li>
          </ul>
        </Disclosure>
        <StepActions>
          <Button type="submit" variant="solid" disabled={busy}>
            {busy ? "Checking" : "Continue"}
          </Button>
        </StepActions>
      </form>
    </WizardFrame>
  );
}
