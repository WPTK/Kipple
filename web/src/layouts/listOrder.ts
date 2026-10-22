import { useCallback, useMemo } from "react";
import { flattenItems, useBootstrap, useItems } from "@/api/queries";
import type { Card, Scope } from "@/api/types";
import { useFavorites } from "@/lib/favorites";
import { pageOf, useResolvedLayout } from "@/layouts";
import { planGazette, readingOrder } from "./gazettePlan";

/** An open article's list in reading order, for its previous and next. */
export interface ListOrder {
  /** The loaded articles in reading order; empty when the list is not asked for (`enabled` false). */
  ids: string[];
  /** The loaded articles in the server's order, whatever is cached, asked for or not. */
  cards: Card[];
  /** The server has more articles than are loaded. */
  more: boolean;
  /** Load the next page: the reading order after it, and whether there is still more to load. */
  fetchMore: () => Promise<{ ids: string[]; more: boolean }>;
}

/**
 * The list an open article belongs to, in the order the reader reads it, taken from the list's cache so it works with
 * no list mounted. In the Gazette (#315) that is the order the page prints the stories, from the same plan the list
 * draws over the loaded list; the plan is the same on a phone and on a wide screen up to its columns, so the order does
 * not depend on the screen. Until the paper has a front page (it waits for the lead window), and for an article
 * outside the plan (not laid out yet), the loaded order stands. Every other layout, and a search, reads the loaded
 * order (newest first, as the server sends it, or the order the list was asked for).
 */
export function useListOrder(scope: Scope, id: string, enabled: boolean): ListOrder {
  const list = useItems(scope, enabled);
  const { layout } = useResolvedLayout(scope);
  const boot = useBootstrap();
  const { favorites } = useFavorites();
  const feeds = boot.data?.feeds;
  const folders = boot.data?.folders;
  const { feed, folder } = scope;
  const paged = !!pageOf(layout, scope);
  const orderOf = useCallback(
    (cards: readonly Card[], more: boolean) => {
      const plain = cards.map((c) => c.id);
      if (!paged) return plain;
      const plan = planGazette({ articles: cards, feeds: feeds ?? [], folders: folders ?? [], favorites, scope: { feed, folder }, screen: "wide", more });
      const printed = plan.pages.length ? readingOrder(plan) : plain;
      return printed.includes(id) ? printed : plain;
    },
    [paged, feeds, folders, favorites, feed, folder, id],
  );
  const cards = useMemo(() => flattenItems(list.data), [list.data]);
  const more = !!list.hasNextPage;
  const ids = useMemo(() => (enabled ? orderOf(cards, more) : []), [cards, more, enabled, orderOf]);
  const fetchMore = async () => {
    const r = await list.fetchNextPage();
    return { ids: orderOf(flattenItems(r.data), !!r.hasNextPage), more: !!r.hasNextPage && !r.isFetchNextPageError };
  };
  return { ids, cards, more, fetchMore };
}
