import { CAUSE_PHRASE, errorMessage, failureCause } from "@/api/client";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { WHY } from "@/ui/LoadFailed";

/**
 * A part of a screen that could not load (the screen around it keeps its own header): the cause in the three phrases every
 * screen uses, a sentence on what to check, and the way to ask again.
 */
export function FailedBlock({ error, onRetry, busy }: { error: unknown; onRetry: () => void; busy: boolean }) {
  const cause = failureCause(error);
  const said = errorMessage(error);
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
