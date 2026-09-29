import { useEffect, useRef } from "react";
import { Button } from "@/ui/button";
import { openReasonText, type OpenReason } from "./api";

/**
 * Shown when Kipple has no password (open mode) and refuses this request: from the sign-in step, or, later, from any
 * screen once the address or network this page came from stopped being an allowed one. It says why in plain words and
 * how to get in, because the server's short answer only makes sense to someone who has read the design.
 */
export function OpenRefusedScreen({ reason, onRetry, busy }: { reason: OpenReason | null; onRetry?: () => void; busy?: boolean }) {
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    heading.current?.focus({ preventScroll: true });
  }, []);
  return (
    <main className="pt-safe pb-safe flex h-full items-center justify-center overflow-y-auto px-4" data-testid="open-refused">
      <div className="flex w-full max-w-md flex-col gap-4 py-6" role="alert">
        <h1 ref={heading} tabIndex={-1} className="text-2xl font-bold outline-none">
          Kipple can't let you in from here
        </h1>
        <p className="text-base">{openReasonText(reason)}</p>
        <div className="rounded-xl border border-line bg-surface px-4 py-3 text-sm">
          <p className="font-semibold">Ways in</p>
          <ul className="mt-1 flex list-disc flex-col gap-1 pl-5 text-fg2">
            <li>Open Kipple on the computer that runs it, at localhost or its IP address.</li>
            <li>Connect over Tailscale and open Kipple by its Tailscale address.</li>
            <li>Or, from a place where you can get in, set a password in Settings so it works from anywhere you allow.</li>
          </ul>
        </div>
        {onRetry ? (
          <Button variant="solid" className="self-start" disabled={busy} onClick={onRetry}>
            {busy ? "Trying" : "Try again"}
          </Button>
        ) : null}
      </div>
    </main>
  );
}
