import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate } from "react-router";
import { ApiError } from "@/api/client";
import { Button } from "@/ui/button";
import { INSTANCE_KEY, isOpenRefused, openRefusedReason, signInOpen, type OpenReason, type SetupOptions } from "./api";
import { AccountStep } from "./AccountStep";
import { OpenRefusedScreen } from "./OpenRefused";
import { welcomeEntry } from "./session";

/**
 * Step 1, before there is an account: the account form is the first screen. It signs the browser in; the app then
 * continues at /welcome.
 */
export function SetupFlow({ options }: { options: SetupOptions }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { pathname } = useLocation();

  // Before there is an account there is no /welcome: a stale address (a bookmark, or a tab left from an earlier run)
  // would otherwise carry the new account past step 2 the moment it signs in. The first signed-in step always follows.
  useEffect(() => {
    if (pathname === "/welcome" || pathname.startsWith("/welcome/")) navigate("/", { replace: true });
  }, [pathname, navigate]);

  return (
    <AccountStep
      state={options}
      onCreated={() => {
        // The sign-in has just turned the app to signed in; step 2 comes first whatever address this page was opened at.
        navigate(welcomeEntry(), { replace: true });
        void qc.invalidateQueries();
      }}
      onDone={() => window.location.reload()}
    />
  );
}

/**
 * When the last automatic open-mode sign-in went through. It outlives the component on purpose: a browser that does
 * not keep the session cookie answers the next request with a 401, which brings the signed-out screen (and a new
 * OpenSignIn) back, and a per-mount guard would then sign in again for ever.
 */
let autoSignedInAt = 0;
/** How soon after a sign-in that "signed out again" means the cookie was not kept, rather than the session ending. */
export const COOKIE_LOOP_MS = 60_000;
/** For tests. */
export function resetOpenSignInGuard(): void {
  autoSignedInAt = 0;
}

/** Open mode: nothing to type. Signs in by itself and says why if the server refuses this address. */
export function OpenSignIn() {
  const qc = useQueryClient();
  const [refused, setRefused] = useState<OpenReason | null | undefined>(undefined);
  const [failed, setFailed] = useState(false);
  // Read once, at mount: a sign-in that just happened and is already gone means the cookie was not kept.
  const [noCookie, setNoCookie] = useState(() => Date.now() - autoSignedInAt < COOKIE_LOOP_MS);
  const [busy, setBusy] = useState(!noCookie);
  const started = useRef(false);

  const attempt = async () => {
    // Noted before the call: its answer turns the app to signed in, and a browser that drops the cookie can be back on this
    // screen before the line after the await runs.
    autoSignedInAt = Date.now();
    try {
      await signInOpen();
      await qc.invalidateQueries();
    } catch (e) {
      autoSignedInAt = 0;
      if (isOpenRefused(e)) setRefused(openRefusedReason(e));
      else {
        // 404: this Kipple no longer runs without a password. Ask again what it is, so the app moves to the form.
        if (e instanceof ApiError && e.status === 404) void qc.invalidateQueries({ queryKey: INSTANCE_KEY });
        setFailed(true);
      }
    } finally {
      setBusy(false);
    }
  };
  const run = () => {
    setBusy(true);
    setFailed(false);
    setNoCookie(false);
    return attempt();
  };
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    // The state already says "signing in" (busy, nothing failed), so the first try only makes the call.
    // The setState calls are all after the request has answered.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (!noCookie) void attempt();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (refused !== undefined) {
    return (
      <OpenRefusedScreen
        reason={refused}
        busy={busy}
        onRetry={() => {
          setRefused(undefined);
          void run();
        }}
      />
    );
  }
  if (noCookie) {
    return (
      <main className="flex h-full items-center justify-center px-4">
        <div className="flex max-w-sm flex-col gap-3" role="alert">
          <h1 className="text-xl font-bold">Kipple needs cookies to keep you signed in</h1>
          <p className="text-fg2">Kipple signed you in, but this browser didn't keep the sign-in. Allow cookies for this address (some private windows and privacy settings block them), then try again.</p>
          <Button variant="solid" className="self-start" disabled={busy} onClick={() => void run()}>
            {busy ? "Trying" : "Try again"}
          </Button>
        </div>
      </main>
    );
  }
  if (failed) {
    return (
      <main className="flex h-full items-center justify-center px-4">
        <div className="flex max-w-sm flex-col gap-3" role="alert">
          <h1 className="text-xl font-bold">Kipple couldn't sign you in</h1>
          <p className="text-fg2">Kipple couldn't reach the server, or it answered with something unexpected. Try again, and check the Kipple logs if it keeps happening.</p>
          <Button
            variant="solid"
            className="self-start"
            onClick={() => {
              // The mode may have changed since this screen was drawn: ask again, then try.
              void qc.invalidateQueries({ queryKey: INSTANCE_KEY });
              void run();
            }}
          >
            Try again
          </Button>
        </div>
      </main>
    );
  }
  return (
    <div className="flex h-full items-center justify-center" role="status">
      <span className="text-fg2">Signing you in</span>
    </div>
  );
}
