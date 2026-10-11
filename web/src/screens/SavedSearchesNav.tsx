import { useId } from "react";
import { Link, useLocation } from "react-router";
import { ChevronDown, ChevronRight, Search } from "lucide-react";
import { searchRoute, unreadLabel, useSavedSearches } from "@/api/savedSearches";
import type { SavedSearch } from "@/api/types";
import { cn } from "@/lib/cn";
import { savedOpenStore, setSavedOpen } from "@/lib/searchPrefs";
import { useStore } from "@/lib/store";

const row = "flex min-h-11 items-center gap-2 rounded-lg px-3 hover:bg-selection";

/** The unread count of a saved search: "999+" at the server's cap, a dash when it could not be counted in time, nothing until it loads. */
export function SavedCount({ s }: { s: SavedSearch }) {
  const label = unreadLabel(s);
  if (s.unread === undefined) return null;
  if (label === null) {
    return (
      <span data-testid="saved-count" className="shrink-0 rounded-full bg-surface px-2 py-0.5 text-xs font-semibold text-fg2">
        <span aria-hidden="true">–</span>
        <span className="sr-only-live">Unread count unavailable</span>
      </span>
    );
  }
  if (s.unread === 0) return null;
  return (
    <span data-testid="saved-count" className="shrink-0 rounded-full bg-surface px-2 py-0.5 text-xs font-semibold text-fg2 tabular-nums">
      <span className="sr-only-live">Unread </span>
      {label}
    </span>
  );
}

function RetryNote({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <li className="px-3 text-xs text-fg2">
      {message}{" "}
      <button type="button" onClick={onRetry} className="min-h-11 text-link underline underline-offset-2">
        Try again
      </button>
    </li>
  );
}

/**
 * "Saved searches" under Favorites: name and unread count, tap runs the search in its scope and order. Collapsible
 * (remembered on this device). Counts are lazy: the names paint first, the numbers follow. Hidden when there are none;
 * says so, with a retry, when the list could not be loaded.
 */
export function SavedSearchesNav({ onNavigate }: { onNavigate?: () => void }) {
  const { searches, error, refetch, countsError, refetchCounts } = useSavedSearches({ counts: true });
  const listId = useId();
  const open = useStore(savedOpenStore);
  const loc = useLocation();
  // Hidden only when the list loaded and is empty; a list that failed to load says so, so it is not mistaken for none.
  const listFailed = error && searches.length === 0;
  if (searches.length === 0 && !listFailed) return null;
  const current = loc.pathname === "/search" ? new URLSearchParams(loc.search).get("ss") : null;
  return (
    <section aria-label="Saved searches" className="mb-2">
      <h2 className="mt-3">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={listId}
          onClick={() => setSavedOpen(!open)}
          className="flex min-h-11 w-full items-center gap-1 rounded-lg px-3 text-left text-xs font-semibold tracking-wide text-fg2 uppercase hover:bg-selection"
        >
          {open ? <ChevronDown className="size-4" aria-hidden="true" /> : <ChevronRight className="size-4" aria-hidden="true" />}
          Saved searches
        </button>
      </h2>
      {open ? (
        <ul id={listId} className="flex flex-col gap-1">
          {searches.map((s) => (
            <li key={s.id}>
              <Link
                to={searchRoute(s)}
                onClick={onNavigate}
                aria-current={current === s.id ? "page" : undefined}
                className={cn(row, "text-sm", current === s.id && "bg-selection")}
              >
                <Search aria-hidden="true" className="size-4 shrink-0 text-fg2" />
                <span className="min-w-0 truncate">{s.name}</span>
                <span className="ml-auto" />
                <SavedCount s={s} />
              </Link>
            </li>
          ))}
          {listFailed ? <RetryNote message="Couldn't load saved searches." onRetry={refetch} /> : null}
          {countsError ? <RetryNote message="Couldn't count unread articles." onRetry={refetchCounts} /> : null}
        </ul>
      ) : null}
    </section>
  );
}
