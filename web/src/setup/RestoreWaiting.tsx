import { useEffect, useState } from "react";
import { ApiError, SESSION_EXPIRED } from "@/api/client";
import { Button } from "@/ui/button";
import { Notice } from "@/ui/kit";
import { fetchInstance } from "./api";
import { WizardFrame } from "./Frame";
import { estimateMinutes } from "./restoreApi";

/** Said before and after a reset: until setup is finished, a new instance has no owner. */
export const UNCLAIMED_NOTICE = "After the reset, Kipple stays open to first-time setup for anyone who can reach it until setup is finished. Finish setup right away.";

/** How often the page asks whether Kipple is back. */
export const POLL_MS = 3000;

/** How long a restart may take before the page says Kipple stopped: three times the estimate, at least 5 minutes. */
export const silenceLimitSeconds = (estimateSeconds: number): number => Math.max(300, estimateSeconds * 3);

const clock = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;

/** What each restart is waiting for, and what the page says meanwhile and after. */
const KINDS = {
  restore: {
    working: "Restoring your library",
    done: "Restored",
    // A restore is over when Kipple answers that setup is over.
    finished: (setup: boolean) => !setup,
    stopped: "Kipple stopped. Start it again and the restore will finish.",
    notApplied: "Kipple is running, but the restore was not applied. Open Kipple again and restore from the setup steps.",
    action: "Go to sign in",
  },
  reset: {
    working: "Resetting Kipple",
    done: "Kipple is ready for setup",
    // A reset is over when Kipple answers that it is waiting for setup.
    finished: (setup: boolean) => setup,
    stopped: "Kipple stopped. Start it again and the reset will finish.",
    notApplied: "Kipple is running, but the reset was not applied. Open Kipple, sign in and try the reset again.",
    action: "Set up Kipple",
  },
} as const;

/**
 * After "Everything" was confirmed in the setup wizard (or on a reload while one is being applied), or after a reset
 * was confirmed in Settings: waits for Kipple to restart. The server is away for the whole restart, so failed requests
 * and 502s from a proxy are normal and ignored. The change is applied once the instance answers as the kind says.
 */
export function RestoreWaiting({
  kind = "restore",
  estimateSeconds,
  username,
  onSignIn,
}: {
  kind?: keyof typeof KINDS;
  estimateSeconds: number;
  username: string | null;
  onSignIn: () => void;
}) {
  const k = KINDS[kind];
  const [elapsed, setElapsed] = useState(0);
  const [done, setDone] = useState(false);
  // What the last poll got: nothing (the network failed: Kipple is away, so "stopped"), an HTTP answer that says
  // neither "setup" nor "not setup" (a refused host name, a proxy error), or an instance answer that is not the one
  // this restart waits for (Kipple is up, but the change was not applied).
  const [answered, setAnswered] = useState<"no" | "kipple" | "redirect">("no");
  const [running, setRunning] = useState(false);
  const [started] = useState(() => Date.now());

  useEffect(() => {
    if (done) return;
    let stop = false;
    const tick = window.setInterval(() => setElapsed(Math.floor((Date.now() - started) / 1000)), 1000);
    const poll = async () => {
      try {
        const i = await fetchInstance();
        if (stop) return;
        setAnswered("no");
        setRunning(!!i && !k.finished(i.setup));
        if (i && k.finished(i.setup)) setDone(true);
      } catch (e) {
        /* Kipple is away while it restarts: keep waiting */
        if (stop) return;
        setRunning(false);
        // A redirect is the access proxy in front of Kipple sending an expired session to its sign-in page.
        if (e instanceof ApiError && e.code === SESSION_EXPIRED) setAnswered("redirect");
        else setAnswered(e instanceof ApiError && e.fromKipple ? "kipple" : "no");
      }
    };
    const t = window.setInterval(() => void poll(), POLL_MS);
    return () => {
      stop = true;
      window.clearInterval(tick);
      window.clearInterval(t);
    };
  }, [done, started, k]);

  if (done) {
    return (
      <WizardFrame step={{ id: "restarted", n: 0, title: k.done }}>
        <div className="flex flex-1 flex-col gap-4">
          <Notice role="status">
            {kind === "reset" ? (
              <>
                Your library was erased. A safety copy is kept in <code>backup/pre-restore-*</code> in your data folder, and <code>kipple restore</code> can bring it back.
              </>
            ) : username
              ? `Restored. Sign in as ${username}.`
              : "Restored. Sign in with the account from your backup."}
          </Notice>
          {kind === "reset" ? <Notice tone="warn">{UNCLAIMED_NOTICE}</Notice> : null}
          <div className="mt-auto flex justify-end border-t border-line pt-4">
            <Button variant="solid" onClick={onSignIn}>
              {k.action}
            </Button>
          </div>
        </div>
      </WizardFrame>
    );
  }

  const stopped = elapsed > silenceLimitSeconds(estimateSeconds);
  const minutes = estimateMinutes(estimateSeconds);
  return (
    <WizardFrame step={{ id: "restarting", n: 0, title: k.working }}>
      <div className="flex flex-col gap-4" role="status">
        <p>
          {kind === "reset"
            ? "Resetting Kipple. It is working, you can leave this page open."
            : `Restoring your library. This usually takes about ${minutes} ${minutes === 1 ? "minute" : "minutes"} for a backup this size. It is working, you can leave this page open.`}
        </p>
        {kind === "reset" ? <Notice tone="warn">{UNCLAIMED_NOTICE}</Notice> : null}
        <p className="text-sm text-fg2" data-testid="elapsed">
          Elapsed {clock(elapsed)}
        </p>
        {stopped ? (
          <Notice tone="warn" role="alert">
            {running
              ? k.notApplied
              : answered === "redirect"
                ? "The page was redirected, probably to a sign-in for the proxy in front of Kipple. Reload this page."
                : answered === "kipple"
                  ? "Kipple answered, but not as expected. Open it by its local address."
                  : k.stopped}
          </Notice>
        ) : null}
      </div>
    </WizardFrame>
  );
}
