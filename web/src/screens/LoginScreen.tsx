import { useId, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, api, busyMessage, SESSION_EXPIRED, sessionLostStore, setSignedIn } from "@/api/client";
import { reloadToSignIn } from "@/lib/reload";
import { useStore } from "@/lib/store";
import { INSTANCE_KEY } from "@/setup/api";
import { Button } from "@/ui/button";

/**
 * Sign-in (POST /api/auth/login). A 401 anywhere in the app lands here. The password may be left empty: the server
 * accepts that only for an account without a web password, reached through a verified Cloudflare Access sign-in.
 */
export function LoginScreen() {
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  /** The sign-in in front of Kipple (the access proxy) expired: only a reload through it helps, not a password. */
  const [expired, setExpired] = useState(false);
  const [busy, setBusy] = useState(false);
  const lost = useStore(sessionLostStore);
  const uid = useId();
  const pid = useId();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
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
      else if (err instanceof ApiError && err.status === 401)
        setError(
          password
            ? "That username or password didn't match."
            : // The server does not say which: no password typed, a wrong username, or no Access sign-in.
              "That didn't work. Enter your password, or, for an account without one, check the username and open Kipple through Cloudflare Access.",
        );
      else if (err instanceof ApiError && err.code === "access_unavailable")
        setError("Kipple can't check your Cloudflare Access sign-in right now. Try again in a moment.");
      else if (err instanceof ApiError && err.status === 0) setError("Kipple couldn't reach the server.");
      else setError("Something went wrong. Try again.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="pt-safe pb-safe flex h-full items-center justify-center overflow-y-auto px-4">
      <form onSubmit={(e) => void submit(e)} className="w-full max-w-sm space-y-4" aria-labelledby="login-title">
        <h1 id="login-title" className="text-2xl font-bold">
          Sign in to Kipple
        </h1>
        {lost && !error ? (
          <p role="status" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg2">
            You were signed out: your session ended, or the password was changed on another device. Sign in again.
          </p>
        ) : null}
        {error ? (
          <div role="alert" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-danger">
            <p>{error}</p>
            {expired ? (
              <Button type="button" className="mt-2" onClick={() => reloadToSignIn()}>
                Reload
              </Button>
            ) : null}
          </div>
        ) : null}
        <div className="flex flex-col gap-1">
          <label htmlFor={uid} className="text-sm font-medium">
            Username
          </label>
          <input
            id={uid}
            name="username"
            autoComplete="username"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="min-h-11 rounded-lg border border-line bg-surface px-3 text-base text-fg"
          />
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor={pid} className="text-sm font-medium">
            Password
          </label>
          <input
            id={pid}
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="min-h-11 rounded-lg border border-line bg-surface px-3 text-base text-fg"
          />
        </div>
        <Button type="submit" variant="solid" className="w-full" disabled={busy}>
          {busy ? "Signing in" : "Sign in"}
        </Button>
      </form>
    </main>
  );
}
