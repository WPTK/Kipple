import { useId, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError, api, authStore } from "@/api/client";
import { Button } from "@/ui/button";

/** Sign-in (POST /api/auth/login). A 401 anywhere in the app lands here. */
export function LoginScreen() {
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const uid = useId();
  const pid = useId();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api("/api/auth/login", { method: "POST", body: { username, password } });
      authStore.set("in");
      await qc.invalidateQueries();
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setError("Too many attempts. Try again in a few minutes.");
      else if (err instanceof ApiError && err.status === 401) setError("That username or password didn't match.");
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
        {error ? (
          <p role="alert" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-danger">
            {error}
          </p>
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
            required
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
