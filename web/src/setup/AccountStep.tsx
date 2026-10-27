import { useRef, useState, type FormEvent } from "react";
import { Button } from "@/ui/button";
import { Checkbox, Field, Notice, PasswordField, RadioCard, inputCls } from "@/ui/kit";
import { MIN_PASSWORD_LEN, accountFailure, createAccount, openReasonText, passwordProblem, type AccountBody, type SetupOptions } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import { stepById } from "./steps";

type Choice = "password" | "access" | "open";

const USERNAME = /^[A-Za-z0-9._-]{1,64}$/;

/** Whether choosing "no password, open" can work from this browser's position (GET /api/instance), and if not why. */
function openAvailability(open: SetupOptions["open"]): { ok: boolean; why: string | null } {
  return open.reason === null ? { ok: true, why: null } : { ok: false, why: openReasonText(open.reason) };
}

/**
 * Step 1: the account, the first screen of a Kipple that has none. A username, then how to sign in: a password (the normal choice), no password behind
 * Cloudflare Access (only offered when Access is set up and this very request came through it), or no password at all
 * (open mode, which is only safe when Kipple can be reached from this computer, your local network and Tailscale and nowhere else).
 */
export function AccountStep({
  state,
  onRestore,
  onCreated,
  onDone,
}: {
  state: SetupOptions;
  /** Opens the restore screen. */
  onRestore?: () => void;
  onCreated: () => void;
  onDone: () => void;
}) {
  const [username, setUsername] = useState("");
  // The live checks (too short, no match) speak once a field has been left, not on every keystroke: each one is an
  // alert, and a screen reader would read out a new one per character. Submitting shows them all regardless.
  const [left, setLeft] = useState<{ username?: boolean; password?: boolean }>({});
  const leave = (k: "username" | "password") => setLeft((l) => (l[k] ? l : { ...l, [k]: true }));
  const [choice, setChoice] = useState<Choice>("password");
  const [password, setPassword] = useState("");
  const [ack, setAck] = useState(false);
  const [fieldError, setFieldError] = useState<{ field: "username" | "password" | "open"; message: string } | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const userInput = useRef<HTMLInputElement>(null);
  const pwInput = useRef<HTMLInputElement>(null);

  const open = openAvailability(state.open);
  const usernameBad = left.username && username !== "" && !USERNAME.test(username) ? "Use 1 to 64 letters, digits, dots, dashes or underscores." : null;
  const pwBad = left.password && password !== "" ? passwordProblem(password) : null;

  const ready =
    USERNAME.test(username) &&
    (choice === "password" ? passwordProblem(password) === null : choice === "access" ? true : ack);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFieldError(null);
    setFormError(null);
    if (!ready) {
      setLeft({ username: true, password: true });
      if (!USERNAME.test(username)) {
        setFieldError({ field: "username", message: username === "" ? "Enter a username." : "Use 1 to 64 letters, digits, dots, dashes or underscores." });
        userInput.current?.focus();
      } else if (choice === "password") {
        setFieldError({ field: "password", message: password === "" ? "Enter a password." : (passwordProblem(password) ?? "Enter a password.") });
        pwInput.current?.focus();
      } else if (choice === "open" && !ack) setFieldError({ field: "open", message: "Tick the box to confirm you understand." });
      return;
    }
    const body: AccountBody =
      choice === "password"
        ? { username, password }
        : choice === "access"
          ? { username, passwordless: "access" }
          : { username, passwordless: "open", acknowledge_open: true };
    setBusy(true);
    try {
      await createAccount(body);
      onCreated();
    } catch (err) {
      const f = accountFailure(err);
      if (f.kind === "field") {
        setFieldError({ field: f.field, message: f.message });
        (f.field === "username" ? userInput : pwInput).current?.focus();
      } else if (f.kind === "done") {
        setDone(f.message);
      } else setFormError(f.message);
    } finally {
      setBusy(false);
    }
  };

  if (done) {
    return (
      <WizardFrame step={stepById("account")}>
        <Notice tone="warn" role="alert">{done}</Notice>
        <StepActions>
          <Button variant="solid" onClick={onDone}>
            Reload
          </Button>
        </StepActions>
      </WizardFrame>
    );
  }

  const accessOffered = state.access.enabled;
  const accessOk = state.access.enabled && state.access.verified;

  return (
    <WizardFrame step={stepById("account")} description="Create the account you will sign in with.">
      <form onSubmit={(e) => void submit(e)} className="flex flex-1 flex-col gap-5" noValidate>
        {formError ? <Notice tone="error">{formError}</Notice> : null}
        {onRestore ? (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-line bg-surface p-3">
            <p className="text-sm">Already have a Kipple backup?</p>
            <Button onClick={onRestore} disabled={busy}>
              Restore from a backup
            </Button>
          </div>
        ) : null}
        <Field label="Username" help="Letters, digits, dots, dashes and underscores, up to 64. Your sync apps use this too." error={fieldError?.field === "username" ? fieldError.message : usernameBad}>
          {(a) => (
            <input
              {...a}
              ref={userInput}
              name="username"
              autoComplete="username"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              required
              value={username}
              onBlur={() => leave("username")}
              onChange={(e) => {
                setUsername(e.target.value);
                setFieldError(null);
              }}
              className={inputCls}
            />
          )}
        </Field>

        <fieldset className="flex min-w-0 flex-col gap-3">
          <legend className="mb-2 text-sm font-semibold">How do you want to sign in?</legend>

          <RadioCard name="signin-choice" value="password" checked={choice === "password"} onSelect={() => setChoice("password")} title="With a password">
            <p>The usual choice. Anyone who wants in needs it.</p>
            {choice === "password" ? (
              <div className="flex flex-col gap-3 text-fg">
                <PasswordField
                  label="Password"
                  help={`At least ${MIN_PASSWORD_LEN} characters. Longer is better; a few words in a row works well.`}
                  error={fieldError?.field === "password" ? fieldError.message : pwBad}
                  inputRef={pwInput}
                  name="new-password"
                  autoComplete="new-password"
                  value={password}
                  onBlur={() => leave("password")}
                  onChange={(v) => {
                    setPassword(v);
                    setFieldError(null);
                  }}
                />
              </div>
            ) : null}
          </RadioCard>

          {accessOffered ? (
            <RadioCard name="signin-choice" value="access" checked={choice === "access"} disabled={!accessOk} onSelect={() => setChoice("access")} title="No password, through Cloudflare Access">
              <p>Cloudflare Access checks who you are before Kipple opens, so Kipple doesn't ask for a password of its own.</p>
              {!accessOk ? <p>To choose this, open Kipple through its Cloudflare Access address, so Kipple can see you signed in there.</p> : null}
            </RadioCard>
          ) : null}

          <RadioCard name="signin-choice" value="open" checked={choice === "open"} disabled={!open.ok} onSelect={() => setChoice("open")} title="No password at all">
            <p>Nothing to remember and nothing to type. This computer, every device on your local network and your Tailscale devices can open Kipple. Only for when Kipple can be reached from nowhere else.</p>
            {!open.ok ? <p role="note">{open.why}</p> : null}
            {choice === "open" ? (
              <div className="flex flex-col gap-3 text-fg">
                <Notice tone="warn">
                  <p className="font-semibold">Anyone who can reach this address can read and change everything.</p>
                  <p className="mt-1">Only choose this if Kipple is reachable only from this computer, your local network or your Tailscale network. In Docker, Kipple can't tell your network from the internet: publish its port only on your local network or Tailscale address, never on a public one. You can set a password later in Settings.</p>
                </Notice>
                <Checkbox
                  label="I understand, and Kipple is only reachable from this computer, my local network or Tailscale"
                  checked={ack}
                  onChange={(v) => {
                    setAck(v);
                    setFieldError(null);
                  }}
                  error={fieldError?.field === "open" ? fieldError.message : null}
                />
              </div>
            ) : null}
          </RadioCard>
        </fieldset>

        <StepActions>
          <Button type="submit" variant="solid" disabled={busy}>
            {busy ? "Creating your account" : "Create my account"}
          </Button>
        </StepActions>
      </form>
    </WizardFrame>
  );
}
