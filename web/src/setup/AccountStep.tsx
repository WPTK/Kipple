import { useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/button";
import { Field, Notice, inputCls } from "@/ui/kit";
import { accountFailure, createAccount, openReasonText, passwordProblem, type AccountBody, type SetupState } from "./api";
import { StepActions, WizardFrame } from "./Frame";
import { setupSecret } from "./secret";
import { stepById } from "./steps";

type Choice = "password" | "access" | "open";

const USERNAME = /^[A-Za-z0-9._-]{1,64}$/;

/** What choosing "no password, open" needs from this browser's position, from GET /api/setup/state. */
export function openAvailability(open: SetupState["open"]): { ok: boolean; needsLan: boolean; why: string | null } {
  if (open.reason === null) return { ok: true, needsLan: false, why: null };
  if (open.lan_reason === null) return { ok: true, needsLan: true, why: openReasonText(open.reason) };
  return { ok: false, needsLan: false, why: openReasonText(open.lan_reason) };
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
 * Step 2: the account. A user name, then how to sign in: a password (the normal choice), no password behind
 * Cloudflare Access (only offered when Access is set up and this very request came through it), or no password at all
 * (open mode, which is only safe when Kipple can be reached from this computer or over Tailscale and nowhere else).
 */
export function AccountStep({ state, onCreated, onRestart, onDone }: { state: SetupState; onCreated: () => void; onRestart: () => void; onDone: () => void }) {
  const uid = useId();
  const [username, setUsername] = useState("");
  const [choice, setChoice] = useState<Choice>("password");
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [ack, setAck] = useState(false);
  const [lan, setLan] = useState(false);
  const [fieldError, setFieldError] = useState<{ field: "username" | "password" | "open"; message: string } | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const userInput = useRef<HTMLInputElement>(null);
  const pwInput = useRef<HTMLInputElement>(null);

  const open = openAvailability(state.open);
  const usernameBad = username !== "" && !USERNAME.test(username) ? "Use 1 to 64 letters, digits, dots, dashes or underscores." : null;
  const pwBad = password !== "" ? passwordProblem(password) : null;
  const mismatch = again !== "" && again !== password ? "The two passwords don't match." : null;

  const ready =
    USERNAME.test(username) &&
    (choice === "password" ? passwordProblem(password) === null && password === again : choice === "access" ? true : ack && (!open.needsLan || lan));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setFieldError(null);
    setFormError(null);
    if (!ready) {
      if (!USERNAME.test(username)) {
        setFieldError({ field: "username", message: username === "" ? "Enter a user name." : "Use 1 to 64 letters, digits, dots, dashes or underscores." });
        userInput.current?.focus();
      } else if (choice === "password") {
        setFieldError({ field: "password", message: password === "" ? "Enter a password." : (passwordProblem(password) ?? "The two passwords don't match.") });
        pwInput.current?.focus();
      } else if (choice === "open" && !ack) setFieldError({ field: "open", message: "Tick the box to confirm you understand." });
      else if (choice === "open") setFormError("Tick \"Also allow devices on my local network\" to continue, or choose a password instead.");
      return;
    }
    const body: AccountBody =
      choice === "password"
        ? { username, password }
        : choice === "access"
          ? { username, passwordless: "access" }
          : { username, passwordless: "open", acknowledge_open: true, ...(open.needsLan ? { open_lan: true } : {}) };
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
      } else if (f.kind === "restart") {
        onRestart();
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
    <WizardFrame step={stepById("account")} description="This is the account you'll sign in to Kipple with. There is only one, and it's yours.">
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
                      onChange={(e) => {
                        setPassword(e.target.value);
                        setFieldError(null);
                      }}
                      className={inputCls}
                    />
                  )}
                </Field>
                <Field label="Password again" error={mismatch}>
                  {(a) => <input {...a} name="new-password-again" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} className={inputCls} />}
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
            <p>Nothing to remember and nothing to type. Only for when Kipple can be reached from this computer, or over Tailscale, and nowhere else.</p>
            {!open.ok ? <p role="note">{open.why}</p> : null}
            {choice === "open" ? (
              <div className="flex flex-col gap-3 text-fg">
                <Notice tone="warn">
                  <p className="font-semibold">Anyone who can reach this address can read and change everything.</p>
                  <p className="mt-1">Only choose this if Kipple is reachable only from this computer (localhost) or over Tailscale. It is your choice, and you can set a password later in Settings.</p>
                </Notice>
                {open.needsLan ? (
                  <div className="flex flex-col gap-1">
                    <label className="flex min-h-11 cursor-pointer items-start gap-3 text-sm">
                      <input type="checkbox" checked={lan} onChange={(e) => setLan(e.target.checked)} aria-describedby={`${uid}-lan`} className="mt-0.5 size-5 shrink-0 accent-[var(--kp-accent)]" />
                      <span className="font-semibold">Also allow devices on my local network</span>
                    </label>
                    <p id={`${uid}-lan`} className="ml-8 text-xs text-fg2">
                      {open.why} Kipple needs this on to accept you from here. It means every device on your home or office network can open Kipple without a password.
                    </p>
                  </div>
                ) : null}
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
                    <span className="font-semibold">I understand, and Kipple is only reachable from this computer or over Tailscale</span>
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
