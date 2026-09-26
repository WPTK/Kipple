import { Suspense, lazy } from "react";
import { liveStore } from "@/api/events";
import { useFilters } from "@/api/filters";
import { filterEditorStore } from "@/lib/similar";
import { useStore, useStoreSelector } from "@/lib/store";

// The editor is loaded the first time it is opened, so the main chunk does not carry it.
const FilterEditor = lazy(() => import("@/screens/filters/FilterEditor").then((m) => ({ default: m.FilterEditor })));

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

/**
 * Progress of "Apply to existing articles": a slim bar under the top of the screen while the run is going. The bar
 * and its counts are for the eyes (aria-hidden text); assistive technology gets a progressbar with its value and a
 * polite status that changes only when a quarter more is done, so it is not read out twice a second. The finish is
 * announced by the event handler.
 */
export function ApplyProgress() {
  const runs = useStoreSelector(liveStore, (s) => s.runs);
  const filters = useFilters();
  const run = Object.values(runs).find((r) => r.kind === "filter_apply");
  if (!run) return null;
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
