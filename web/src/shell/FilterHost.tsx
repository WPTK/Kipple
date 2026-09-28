import { Suspense } from "react";
import { lazyScreen } from "@/lib/lazyScreen";
import { liveStore } from "@/api/events";
import { useFilters } from "@/api/filters";
import { useBootstrap } from "@/api/queries";
import { filterEditorStore } from "@/lib/similar";
import { closeFeedEditor, feedEditorStore } from "@/lib/feedEditor";
import { useStore, useStoreSelector } from "@/lib/store";
import type { RunStatus } from "@/api/types";

// The editor is loaded the first time it is opened, so the main chunk does not carry it.
const FilterEditor = lazyScreen(() => import("@/screens/filters/FilterEditor").then((m) => ({ default: m.FilterEditor })));
const FeedEditor = lazyScreen(() => import("@/screens/feeds/FeedEditor").then((m) => ({ default: m.FeedEditor })));

/** The one place the filter editor is mounted: Settings, the article menu and a muted article all open it through `openFilterEditor`. */
export function FilterEditorHost() {
  const request = useStore(filterEditorStore);
  if (!request) return null;
  return (
    <Suspense fallback={null}>
      <FilterEditor request={request} />
    </Suspense>
  );
}

/** The feed editor opened from an article's "Manage this feed" menu action, through `openFeedEditor`. */
export function FeedEditorHost() {
  const feedId = useStore(feedEditorStore);
  const boot = useBootstrap();
  const feed = feedId ? boot.data?.feeds.find((f) => f.id === feedId) : undefined;
  if (!feed) return null;
  return (
    <Suspense fallback={null}>
      <FeedEditor feed={feed} onClose={closeFeedEditor} />
    </Suspense>
  );
}

/**
 * Progress of "Apply to existing articles": a slim bar under the top of the screen while the run is going. The bar
 * and its counts are for the eyes (aria-hidden text); assistive technology gets a progressbar with its value and a
 * polite status that changes only when a quarter more is done, so it is not read out twice a second. The finish is
 * announced by the event handler.
 */
export function ApplyProgress() {
  const runs = useStoreSelector(liveStore, (s) => s.runs);
  const run = Object.values(runs).find((r) => r.kind === "filter_apply");
  const autoRead = Object.values(runs).some((r) => r.kind === "auto_read");
  if (run) return <ApplyBar run={run} />;
  // The server is marking old articles read (Settings, Reading): a quiet line, no count or progress bar.
  if (autoRead) {
    return (
      <div data-testid="auto-read-status" role="status" className="pt-safe shrink-0 border-b border-line bg-surface px-4 py-2 text-sm text-fg2">
        Marking old articles as read
      </div>
    );
  }
  return null;
}

/** Mounted only while a filter apply runs, so the rules are fetched only then (for the rule's name). */
function ApplyBar({ run }: { run: RunStatus }) {
  const filters = useFilters();
  const name = filters.data?.find((f) => f.id === run.filter_id)?.name;
  const total = Math.max(run.total, 0);
  const done = Math.min(run.done, total);
  const quarter = total > 0 ? Math.floor((done / total) * 4) : 0;
  return (
    <div data-testid="apply-progress" className="pt-safe shrink-0 border-b border-line bg-surface px-4 py-2 text-sm">
      <div className="flex items-center gap-3">
        <span aria-hidden="true" className="min-w-0 flex-1 truncate">
          Applying {name ? `“${name}”` : "a filter"}: {done.toLocaleString()} of {total.toLocaleString()} articles checked
          {run.changed ? `, ${run.changed.toLocaleString()} changed` : ""}
        </span>
        <div
          role="progressbar"
          aria-label={name ? `Applying ${name}` : "Applying a filter"}
          aria-valuemin={0}
          aria-valuemax={total}
          aria-valuenow={done}
          aria-valuetext={`${done} of ${total} articles checked`}
          className="h-2 w-32 shrink-0 overflow-hidden rounded-full bg-line"
        >
          <div className="h-full bg-accent transition-[width] duration-300" style={{ width: total ? `${(done / total) * 100}%` : "0%" }} />
        </div>
      </div>
      <p role="status" className="sr-only-live">
        Applying a filter: {quarter * 25} percent
      </p>
    </div>
  );
}
