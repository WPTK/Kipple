import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, api, busyMessage, errorMessage, SESSION_EXPIRED, sessionLostStore, setSignedIn, signedOutStore } from "@/api/client";
import { reloadToSignIn } from "@/lib/reload";
import { useStore } from "@/lib/store";
import { INSTANCE_KEY } from "@/setup/api";
import { Button } from "@/ui/button";
import { Notice, PasswordField, inputCls } from "@/ui/kit";

/**
 * Sign-in (POST /api/auth/login). A 401 anywhere in the app lands here. The password may be left empty: the server
 * accepts that only for an account without a web password, reached through a verified Cloudflare Access sign-in.
 */
export function LoginScreen() {
  useDocumentTitle("Sign in to Kipple", true);
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  /** A failure that belongs to the whole form (the network, a pause, an expired proxy sign-in): a note above the fields. */
  const [error, setError] = useState<string | null>(null);
  /** The 401: it is about what was typed, so it sits under the password field and both fields are marked invalid. */
  const [badCredentials, setBadCredentials] = useState<string | null>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const usernameRef = useRef<HTMLInputElement>(null);
  /** The sign-in in front of Kipple (the access proxy) expired: only a reload through it helps, not a password. */
  const [expired, setExpired] = useState(false);
  const [busy, setBusy] = useState(false);
  const lost = useStore(sessionLostStore);
  const signedOut = useStore(signedOutStore);
  const uid = useId();
  const eid = useId();

  // Coming back after a session ended (or a sign-out) starts at the username; a first visit does not take focus.
  useEffect(() => {
    if (lost || signedOut) usernameRef.current?.focus();
  }, [lost, signedOut]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setBadCredentials(null);
    setExpired(false);
    try {
      await api("/api/auth/login", { method: "POST", body: { username, password } });
      setSignedIn();
      await qc.invalidateQueries();
    } catch (err) {
      // Checked before the status: an expired proxy sign-in also arrives as a 401, and it is not a wrong password.
      if (err instanceof ApiError && err.code === SESSION_EXPIRED) {
        setExpired(true);
        setError("The sign-in in front of Kipple has expired. Reload to sign in again.");
      } else if (err instanceof ApiError && err.status === 409 && err.code === "open_mode") {
        // This Kipple has no password (it went into open mode since the form was drawn): ask again what it is, and the
        // app switches to the silent open-mode sign-in by itself.
        setError("This Kipple doesn't use a password. Signing you in.");
        // If the answer does not move the app on, say what to do rather than leave that message up.
        void qc.invalidateQueries({ queryKey: INSTANCE_KEY }).then(() => setError("This Kipple doesn't use a password. Reload the page to sign in."));
      } else if (err instanceof ApiError && err.status === 409 && err.code === "setup_required") {
        // This Kipple has no account yet (a stale tab): ask again what it is, and the app moves to the account form.
        setError("Kipple has no account yet. Taking you to set it up.");
        void qc.invalidateQueries({ queryKey: INSTANCE_KEY }).then(() => setError("Kipple has no account yet. Reload the page to set it up."));
      } else if (err instanceof ApiError && err.status === 503 && err.code === "busy") setError(busyMessage(err));
      else if (err instanceof ApiError && err.status === 401) {
        setBadCredentials(
          password
            ? "That username or password didn't match."
            : // The server does not say which: no password typed, a wrong username, or no Access sign-in.
              "That didn't work. Enter your password, or, for an account without one, check the username and open Kipple through Cloudflare Access.",
        );
        passwordRef.current?.focus();
      }
      else if (err instanceof ApiError && err.code === "access_unavailable")
        setError("Kipple can't check your Cloudflare Access sign-in right now. Try again in a moment.");
      else setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="pt-safe pb-safe flex h-full justify-center overflow-y-auto px-4">
      {/* Anchored near the top, not centered: a centered form moves every field when an error line is added. */}
      <form onSubmit={(e) => void submit(e)} className="mt-[min(18vh,8rem)] mb-8 w-full max-w-sm space-y-4" aria-labelledby="login-title">
        <img src="/favicon.svg" alt="" className="size-10 rounded-lg" />
        <h1 id="login-title" className="text-2xl font-bold">
          Sign in to Kipple
        </h1>
        {!error && signedOut ? (
          <p role="status" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg2">
            You signed out.
          </p>
        ) : null}
        {!error && lost ? (
          <p role="status" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg2">
            Your session ended. Sign in again.
          </p>
        ) : null}
        {error ? (
          <div id={eid}>
            <Notice tone="error">
              <p>{error}</p>
              {expired ? (
                <Button type="button" className="mt-2" onClick={() => reloadToSignIn()}>
                  Reload
                </Button>
              ) : null}
            </Notice>
          </div>
        ) : null}
        <div className="flex flex-col gap-1">
          <label htmlFor={uid} className="text-sm font-semibold">
            Username
          </label>
          <input
            id={uid}
            ref={usernameRef}
            name="username"
            autoComplete="username"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            aria-describedby={error ? eid : undefined}
            aria-invalid={badCredentials ? true : undefined}
            className={inputCls}
          />
        </div>
        <PasswordField label="Password" name="password" value={password} onChange={setPassword} autoComplete="current-password" inputRef={passwordRef} error={badCredentials} {...(error ? { "aria-describedby": eid } : {})} />
        <Button type="submit" variant="solid" className="w-full" disabled={busy}>
          {busy ? "Signing in" : "Sign in"}
        </Button>
        <p className="text-sm text-fg2">
          Forgot your password? Run <code className="font-mono">kipple password</code> on the machine that runs Kipple.
        </p>
      </form>
    </main>
  );
}
