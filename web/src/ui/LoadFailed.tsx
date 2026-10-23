import { useEffect, useState } from "react";
import { CAUSE_PHRASE, type FailureCause } from "@/api/client";
import { useLatestRef } from "@/lib/useLatestRef";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";

/** How often a screen that could not load asks again by itself (at least; a Retry-After is honoured). */
export const AUTO_RETRY_MS = 5_000;

/**
 * The failure a first load ended with, kept while it is asked again. TanStack Query puts a query that has no data back to
 * "pending" for each new attempt, which would unmount the failed screen (and its alert and focus) every few seconds; a
 * screen shows LoadFailed for as long as this is set, and it is cleared when data arrives. `error` is the query's.
 */
export function useKeptFailure(error: unknown, hasData: boolean): unknown {
  const [kept, setKept] = useState<unknown>(null);
  // Data wins: a refetch that fails over data already shown (TanStack keeps both) is not a failed launch.
  if (hasData) {
    if (kept !== null) setKept(null);
  } else if (error != null && kept !== error) setKept(error);
  return kept;
}

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
  retryAfterMs = 0,
}: {
  cause: FailureCause | "other";
  onRetry: () => void;
  busy: boolean;
  /** Replaces the sentence for the cause when the screen knows more (what is saved on this device, say). */
  why?: string;
  /** The server's Retry-After, in milliseconds: the next automatic try waits at least this long. */
  retryAfterMs?: number;
}) {
  const retry = useLatestRef(onRetry);
  useEffect(() => {
    // Only a cause that can pass by itself is asked again on its own: not a refusal, a rate limit or an answer Kipple
    // did not expect (a sign-in attempt that is wrong stays wrong).
    if (busy || cause === "other") return;
    const h = setTimeout(() => retry.current(), Math.max(AUTO_RETRY_MS, retryAfterMs));
    return () => clearTimeout(h);
  }, [busy, cause, retryAfterMs, retry]);
  return (
    <main className="flex h-full items-center justify-center">
      <EmptyState
        role="alert"
        asH1
        title={cause === "other" ? "Something went wrong" : CAUSE_PHRASE[cause]}
        why={why ?? WHY[cause]}
        action={
          <>
            <Button variant="solid" aria-disabled={busy} onClick={onRetry}>
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
