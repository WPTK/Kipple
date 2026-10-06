import { useEffect, useState } from "react";
import { Button } from "@/ui/button";
import { Notice } from "@/ui/kit";
import { fetchInstance } from "./api";
import { WizardFrame } from "./Frame";
import { estimateMinutes } from "./restoreApi";

const WAITING_STEP = { id: "restoring", n: 0, title: "Restoring your library" } as const;
/** How often the page asks whether Kipple is back. */
export const POLL_MS = 3000;

/** How long a restore may take before the page says Kipple stopped: three times the estimate, at least 5 minutes. */
export const silenceLimitSeconds = (estimateSeconds: number): number => Math.max(300, estimateSeconds * 3);

const clock = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;

/**
 * After "Everything" was confirmed (or on a reload while one is being applied): waits for Kipple to restart. The server
 * is away for the whole restore, so failed requests and 502s from a proxy are normal and ignored. The restore is
 * applied once the instance answers that setup is over.
 */
export function RestoreWaiting({ estimateSeconds, username, onSignIn }: { estimateSeconds: number; username: string | null; onSignIn: () => void }) {
  const [elapsed, setElapsed] = useState(0);
  const [done, setDone] = useState(false);
  const [started] = useState(() => Date.now());

  useEffect(() => {
    if (done) return;
    let stop = false;
    const tick = window.setInterval(() => setElapsed(Math.floor((Date.now() - started) / 1000)), 1000);
    const poll = async () => {
      try {
        const i = await fetchInstance();
        if (!stop && i && !i.setup) setDone(true);
      } catch {
        /* Kipple is away while it restores: keep waiting */
      }
    };
    const t = window.setInterval(() => void poll(), POLL_MS);
    return () => {
      stop = true;
      window.clearInterval(tick);
      window.clearInterval(t);
    };
  }, [done, started]);

  if (done) {
    return (
      <WizardFrame step={{ ...WAITING_STEP, title: "Restored" }}>
        <div className="flex flex-1 flex-col gap-4">
          <Notice role="status">{username ? `Restored. Sign in as ${username}.` : "Restored. Sign in with the account from your backup."}</Notice>
          <div className="mt-auto flex justify-end border-t border-line pt-4">
            <Button variant="solid" onClick={onSignIn}>
              Go to sign in
            </Button>
          </div>
        </div>
      </WizardFrame>
    );
  }

  const stopped = elapsed > silenceLimitSeconds(estimateSeconds);
  const minutes = estimateMinutes(estimateSeconds);
  return (
    <WizardFrame step={WAITING_STEP}>
      <div className="flex flex-col gap-4" role="status">
        <p>
          Restoring your library. This usually takes about {minutes} {minutes === 1 ? "minute" : "minutes"} for a backup this size. It is working, you can leave this page open.
        </p>
        <p className="text-sm text-fg2" data-testid="elapsed">
          Elapsed {clock(elapsed)}
        </p>
        {stopped ? (
          <Notice tone="warn" role="alert">
            Kipple stopped. Start it again and the restore will finish.
          </Notice>
        ) : null}
      </div>
    </WizardFrame>
  );
}
