import { useCallback, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { flattenItems, keys, useItems } from "@/api/queries";
import type { Card, Scope } from "@/api/types";

/**
 * Where a list's articles come from. The list screen drives everything it does with them (restoring the place,
 * loading more, mark as read while scrolling, the arrow keys, mark all, mark above and below) through this alone, so a
 * layout that brings its own data (`ListLayout.data`) only needs a source of its own.
 *
 * A source is a fresh object on every render, like the query it wraps: effects that list it in their dependencies run
 * whenever the list's state changes. `startOver` is the one member that keeps its identity (while the scope does).
 */
export interface ListSource {
  /** Every loaded article, in the server's order. */
  cards: Card[];
  /** The server has more articles than are loaded. */
  more: boolean;
  /** Load the next page. */
  loadMore: () => Promise<unknown>;
  /** The next page is being loaded. */
  loadingMore: boolean;
  /** The last attempt at the next page failed (the loaded articles stay). */
  loadMoreFailed: boolean;
  /** Nothing is loaded yet and nothing has failed. */
  pending: boolean;
  /** Any request for the list is out (its first page, a refetch, the next page). */
  fetching: boolean;
  /** Why the last request failed, null when it did not. */
  error: unknown;
  /** Some articles are loaded (an error then leaves them in place). */
  hasData: boolean;
  /** The server's `as_of` for the list (the newest article it knew of when the first page was sent). */
  asOf: string | undefined;
  /** A search that had no exact match and shows partial matches (docs/design.md 2.4). */
  fallback: boolean;
  /** Ask again for a list whose first load failed. */
  retry: () => void;
  /** Drop what is loaded and load the list from its first page. */
  startOver: () => Promise<void>;
  /**
   * The articles mark above and below may reach from `item`, in the date order the server marks a side by; null when
   * the list's reading order is not date order there.
   */
  rangeOf: (item: Card) => readonly Card[] | null;
}

/**
 * The list as the server sends it, a page at a time (useItems). `paged`: a page layout (the Gazette) draws it, whose
 * reading order is not date order, so mark above and below reach nothing.
 */
export function useItemsSource(scope: Scope, enabled: boolean, paged: boolean): ListSource {
  const qc = useQueryClient();
  const q = useItems(scope, enabled);
  const cards = useMemo(() => flattenItems(q.data), [q.data]);
  const startOver = useCallback(() => qc.resetQueries({ queryKey: keys.items(scope) }), [qc, scope]);
  const first = q.data?.pages[0];
  return {
    cards,
    more: !!q.hasNextPage,
    loadMore: () => q.fetchNextPage(),
    loadingMore: q.isFetchingNextPage,
    loadMoreFailed: q.isFetchNextPageError,
    pending: q.isPending,
    fetching: q.isFetching,
    error: q.error,
    hasData: !!q.data,
    asOf: first?.as_of,
    fallback: first?.fallback === true,
    retry: () => void q.refetch(),
    startOver,
    rangeOf: () => (paged ? null : cards),
  };
}
