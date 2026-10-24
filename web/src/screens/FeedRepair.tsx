import { Suspense, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { invalidateFeeds, refreshFeed, useHealth } from "@/api/admin";
import { errorMessage } from "@/api/client";
import { lazyScreen } from "@/lib/lazyScreen";
import type { Feed } from "@/api/types";
import { failureReason, retryLabel } from "@/lib/feedStatus";
import { whenLabel } from "@/lib/format";
import { Button } from "@/ui/button";
import { announce, toast } from "@/shell/toasts";

const FeedEditor = lazyScreen(() => import("./feeds/FeedEditor").then((m) => ({ default: m.FeedEditor })));

/** When a feed last worked and when Kipple tries again, from Feed health's own figures (the feed list does not carry them). */
export function useFeedTimes(id: string) {
  const h = useHealth();
  const f = h.data?.feeds.find((x) => x.id === id);
  return { lastSuccess: f?.last_success_at, retry: f?.enabled === false ? "" : retryLabel(f?.next_fetch_at), failures: f?.consecutive_failures ?? 0 };
}

/** "Last worked 3 hours ago. Trying again in 1 hour." The part that is not known yet is left out. */
export function FeedFacts({ id }: { id: string }) {
  const t = useFeedTimes(id);
  const parts = [t.lastSuccess ? `Last worked ${whenLabel(t.lastSuccess).toLowerCase()}.` : "", t.retry ? `${t.retry}.` : ""].filter(Boolean);
  return parts.length ? <p className="text-sm text-fg2">{parts.join(" ")}</p> : null;
}

/** The reason a feed fails, with its facts: the block at the top of Edit feed. */
export function FeedReason({ feed }: { feed: Pick<Feed, "id" | "last_error" | "last_error_class"> }) {
  if (!feed.last_error) return null;
  return (
    <div data-testid="feed-reason" className="flex flex-col gap-0.5 rounded-xl border border-danger bg-surface px-3 py-2">
      <p className="text-sm font-semibold">{failureReason(feed)}.</p>
      <FeedFacts id={feed.id} />
    </div>
  );
}

/**
 * Edit address and Try now, the two ways to repair a failing feed. Try now fetches the feed at once and says what came
 * of it. `onEdit` opens Edit feed with its address field focused.
 */
export function RepairButtons({ feedId, title, onEdit }: { feedId: string; title: string; onEdit: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const tryNow = async () => {
    setBusy(true);
    try {
      const r = await refreshFeed(feedId);
      invalidateFeeds(qc);
      void qc.invalidateQueries({ queryKey: ["items"] });
      if (r.pending) announce("Still fetching. Check again in a moment.");
      else if (r.error || r.outcome === "error") toast(`${title} still can't be fetched: ${failureReason({ last_error: r.error, last_error_class: r.error_class }).toLowerCase()}.`, "error");
      else toast(r.new_items ? `${title} works again: ${r.new_items} new article${r.new_items === 1 ? "" : "s"}.` : `${title} works again.`);
    } catch (e) {
      toast(errorMessage(e), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-wrap gap-2">
      <Button onClick={onEdit} aria-label={`Edit address of ${title}`}>
        Edit address
      </Button>
      <Button disabled={busy} onClick={() => void tryNow()} aria-label={`Try ${title} now`}>
        Try now
      </Button>
    </div>
  );
}

/** The repair surface in a failing feed's empty list: the reason's facts and the two buttons. */
export function FeedRepair({ feed }: { feed: Feed }) {
  const [editing, setEditing] = useState(false);
  return (
    <div data-testid="feed-repair" className="flex flex-col items-center gap-3">
      <FeedFacts id={feed.id} />
      <RepairButtons feedId={feed.id} title={feed.title} onEdit={() => setEditing(true)} />
      {editing ? (
        <Suspense fallback={null}>
          <FeedEditor feed={feed} focusAddress onClose={() => setEditing(false)} />
        </Suspense>
      ) : null}
    </div>
  );
}
