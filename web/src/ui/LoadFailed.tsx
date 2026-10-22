import { useEffect } from "react";
import { CAUSE_PHRASE, type FailureCause } from "@/api/client";
import { useLatestRef } from "@/lib/useLatestRef";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";

/** How often a screen that could not load asks again by itself. */
export const AUTO_RETRY_MS = 5_000;

const WHY: Record<FailureCause | "other", string> = {
  offline: "Check your connection.",
  unreachable: "Kipple isn't answering. Check that it is running and that you can reach it.",
  server: "Kipple answered with an error.",
  other: "Kipple got an answer it didn't expect.",
};

/**
 * The first load failed and there is nothing to show yet. It names the cause (Q14), asks again every few seconds on its
 * own with a "Trying again" line, and keeps the button for asking now. `onRetry` must be quiet: it changes nothing on
 * screen except `busy`.
 */
export function LoadFailed({
  cause,
  onRetry,
  busy,
  why,
}: {
  cause: FailureCause | "other";
  onRetry: () => void;
  busy: boolean;
  /** Replaces the sentence for the cause when the screen knows more (what is saved on this device, say). */
  why?: string;
}) {
  const retry = useLatestRef(onRetry);
  useEffect(() => {
    if (busy) return;
    const h = setTimeout(() => retry.current(), AUTO_RETRY_MS);
    return () => clearTimeout(h);
  }, [busy]);
  return (
    <main className="flex h-full items-center justify-center">
      <EmptyState
        role="alert"
        asH1
        title={cause === "other" ? "Something went wrong" : CAUSE_PHRASE[cause]}
        why={why ?? WHY[cause]}
        action={
          <>
            <Button variant="solid" disabled={busy} onClick={onRetry}>
              Try again
            </Button>
            <p role="status" className="min-h-5 text-sm text-fg2">
              {busy ? "Trying again" : ""}
            </p>
          </>
        }
      />
    </main>
  );
}
