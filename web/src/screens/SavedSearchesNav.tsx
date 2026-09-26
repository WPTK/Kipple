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

/**
 * "Saved searches" under Favorites: name and unread count, tap runs the search in its scope and order. Collapsible
 * (remembered on this device). Counts are lazy: the names paint first, the numbers follow. Hidden when there are none.
 */
export function SavedSearchesNav({ onNavigate }: { onNavigate?: () => void }) {
  const { searches, countsError, refetchCounts } = useSavedSearches({ counts: true });
  const listId = useId();
  const open = useStore(savedOpenStore);
  const loc = useLocation();
  if (searches.length === 0) return null;
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
          {countsError ? (
            <li className="px-3 text-xs text-fg2">
              Couldn't count unread articles.{" "}
              <button type="button" onClick={refetchCounts} className="min-h-11 text-link underline underline-offset-2">
                Try again
              </button>
            </li>
          ) : null}
        </ul>
      ) : null}
    </section>
  );
}
