import { CAUSE_PHRASE, errorMessage, failureCause } from "@/api/client";
import { offlineStore } from "@/lib/offlineState";
import { useStore } from "@/lib/store";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { WHY } from "@/ui/LoadFailed";

/**
 * A part of a screen that could not load (the screen around it keeps its own header): the cause in the three phrases every
 * screen uses, a sentence on what to check, and the way to ask again. While the offline notice is up it already says
 * the cause (offline, or Kipple not answering), so the block names only what is missing (`what`) and does not say the
 * cause a second time.
 */
export function FailedBlock({ error, onRetry, busy, what }: { error: unknown; onRetry: () => void; busy: boolean; what: string }) {
  const cause = failureCause(error);
  const said = errorMessage(error);
  const noticeUp = !useStore(offlineStore).online && (cause === "offline" || cause === "unreachable");
  if (noticeUp)
    return (
      <EmptyState
        role="alert"
        title={`${what} didn't load`}
        why="Try again once you're back."
        action={
          <Button variant="solid" disabled={busy} onClick={onRetry}>
            Try again
          </Button>
        }
      />
    );
  return (
    <EmptyState
      role="alert"
      title={cause ? CAUSE_PHRASE[cause] : "Something went wrong"}
      why={cause ? WHY[cause] : said === "Something went wrong." ? WHY.other : said}
      action={
        <Button variant="solid" disabled={busy} onClick={onRetry}>
          Try again
        </Button>
      }
    />
  );
}
