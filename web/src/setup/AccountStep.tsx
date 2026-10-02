import { useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/button";
import { Field, Notice, inputCls } from "@/ui/kit";
import { accountFailure, createAccount, openReasonText, passwordProblem, type AccountBody, type SetupOptions } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import { setupSecret } from "./session";
import { stepById } from "./steps";

type Choice = "password" | "access" | "open";

const USERNAME = /^[A-Za-z0-9._-]{1,64}$/;

/** Whether choosing "no password, open" can work from this browser's position (GET /api/instance), and if not why. */
export function openAvailability(open: SetupOptions["open"]): { ok: boolean; why: string | null } {
  return open.reason === null ? { ok: true, why: null } : { ok: false, why: openReasonText(open.reason) };
}

function Option({
  id,
  value,
  checked,
  disabled,
  onSelect,
  title,
  children,
}: {
  id: string;
  value: Choice;
  checked: boolean;
  disabled?: boolean;
  onSelect: (c: Choice) => void;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className={cn("rounded-xl border p-3", checked ? "border-accent bg-selection" : "border-line bg-surface", disabled && "opacity-70")}>
      <label htmlFor={id} className="flex min-h-11 cursor-pointer items-start gap-3 has-[:disabled]:cursor-not-allowed">
        <input
          id={id}
          type="radio"
          name="signin-choice"
          value={value}
          checked={checked}
          disabled={disabled}
          onChange={() => onSelect(value)}
          aria-describedby={`${id}-d`}
          className="mt-1 size-5 shrink-0 accent-[var(--kp-accent)]"
        />
        <span className="text-base font-semibold">{title}</span>
      </label>
      <div id={`${id}-d`} className="ml-8 flex flex-col gap-3 text-sm text-fg2">
        {children}
      </div>
    </div>
  );
}

/**
 * Step 1: the account, the first screen of a Kipple that has none. A user name, then how to sign in: a password (the normal choice), no password behind
 * Cloudflare Access (only offered when Access is set up and this very request came through it), or no password at all
 * (open mode, which is only safe when Kipple can be reached from this computer, your local network and Tailscale and nowhere else).
 */
export function AccountStep({
  state,
  onCreated,
  onDone,
}: {
  state: SetupOptions;
  onCreated: () => void;
  onDone: () => void;
}) {
  const uid = useId();
  const [username, setUsername] = useState("");
  // The live checks (too short, no match) speak once a field has been left, not on every keystroke: each one is an
  // alert, and a screen reader would read out a new one per character. Submitting shows them all regardless.
  const [left, setLeft] = useState<{ username?: boolean; password?: boolean; again?: boolean }>({});
  const leave = (k: "username" | "password" | "again") => setLeft((l) => (l[k] ? l : { ...l, [k]: true }));
  const [choice, setChoice] = useState<Choice>("password");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
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
  const mismatch = left.again && again !== "" && again !== password ? "The two passwords don't match." : null;

  const ready =
    USERNAME.test(username) &&
    (choice === "password" ? passwordProblem(password) === null && password === again : choice === "access" ? true : ack);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFieldError(null);
    setFormError(null);
    if (!ready) {
      setLeft({ username: true, password: true, again: true });
      if (!USERNAME.test(username)) {
        setFieldError({ field: "username", message: username === "" ? "Enter a user name." : "Use 1 to 64 letters, digits, dots, dashes or underscores." });
        userInput.current?.focus();
      } else if (choice === "password") {
        setFieldError({ field: "password", message: password === "" ? "Enter a password." : (passwordProblem(password) ?? "The two passwords don't match.") });
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
      setupSecret.set(choice === "password" ? password : null);
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
    <WizardFrame step={stepById("account")} description="Kipple has no account yet. This is the one you'll sign in with. There is only one, and it's yours.">
      <form onSubmit={(e) => void submit(e)} className="flex flex-1 flex-col gap-5" noValidate>
        {formError ? <Notice tone="error">{formError}</Notice> : null}
        <Field label="User name" help="Letters, digits, dots, dashes and underscores, up to 64. Your sync apps use this too." error={fieldError?.field === "username" ? fieldError.message : usernameBad}>
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

          <Option id={`${uid}-pw`} value="password" checked={choice === "password"} onSelect={setChoice} title="With a password">
            <p>The usual choice. Anyone who wants in needs it.</p>
            {choice === "password" ? (
              <div className="flex flex-col gap-3 text-fg">
                <Field label="Password" help="At least 5 characters. Longer is better; a few words in a row works well." error={fieldError?.field === "password" ? fieldError.message : pwBad}>
                  {(a) => (
                    <input
                      {...a}
                      ref={pwInput}
                      name="new-password"
                      type="password"
                      autoComplete="new-password"
                      value={password}
                      onBlur={() => leave("password")}
                      onChange={(e) => {
                        setPassword(e.target.value);
                        setFieldError(null);
                      }}
                      className={inputCls}
                    />
                  )}
                </Field>
                <Field label="Password again" error={mismatch}>
                  {(a) => <input {...a} name="new-password-again" type="password" autoComplete="new-password" value={again} onBlur={() => leave("again")} onChange={(e) => setAgain(e.target.value)} className={inputCls} />}
                </Field>
              </div>
            ) : null}
          </Option>

          {accessOffered ? (
            <Option id={`${uid}-access`} value="access" checked={choice === "access"} disabled={!accessOk} onSelect={setChoice} title="No password, through Cloudflare Access">
              <p>Cloudflare Access checks who you are before Kipple opens, so Kipple doesn't ask for a password of its own.</p>
              {!accessOk ? <p>To choose this, open Kipple through its Cloudflare Access address, so Kipple can see you signed in there.</p> : null}
            </Option>
          ) : null}

          <Option id={`${uid}-open`} value="open" checked={choice === "open"} disabled={!open.ok} onSelect={setChoice} title="No password at all">
            <p>Nothing to remember and nothing to type. This computer, every device on your local network and your Tailscale devices can open Kipple. Only for when Kipple can be reached from nowhere else.</p>
            {!open.ok ? <p role="note">{open.why}</p> : null}
            {choice === "open" ? (
              <div className="flex flex-col gap-3 text-fg">
                <Notice tone="warn">
                  <p className="font-semibold">Anyone who can reach this address can read and change everything.</p>
                  <p className="mt-1">Only choose this if Kipple is reachable only from this computer, your local network or your Tailscale network. In Docker, Kipple can't tell your network from the internet: publish its port only on your local network or Tailscale address, never on a public one. You can set a password later in Settings.</p>
                </Notice>
                <div className="flex flex-col gap-1">
                  <label className="flex min-h-11 cursor-pointer items-start gap-3 text-sm">
                    <input
                      type="checkbox"
                      checked={ack}
                      onChange={(e) => {
                        setAck(e.target.checked);
                        setFieldError(null);
                      }}
                      aria-invalid={fieldError?.field === "open" ? true : undefined}
                      aria-describedby={fieldError?.field === "open" ? `${uid}-ack-e` : undefined}
                      className="mt-0.5 size-5 shrink-0 accent-[var(--kp-accent)]"
                    />
                    <span className="font-semibold">I understand, and Kipple is only reachable from this computer, my local network or Tailscale</span>
                  </label>
                  {fieldError?.field === "open" ? (
                    <p id={`${uid}-ack-e`} role="alert" className="ml-8 text-sm text-danger">
                      {fieldError.message}
                    </p>
                  ) : null}
                </div>
              </div>
            ) : null}
          </Option>
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
