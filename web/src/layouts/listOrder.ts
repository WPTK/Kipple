import { useCallback, useMemo } from "react";
import { flattenItems, useBootstrap, useItems } from "@/api/queries";
import type { Card, Scope } from "@/api/types";
import { useFavorites } from "@/lib/favorites";
import { pageOf, useResolvedLayout } from "@/layouts";
import { useDigestSource } from "@/screens/digestSource";
import { planGazette, readingOrder } from "./gazettePlan";

/** An open article's list in reading order, for its previous and next. */
export interface ListOrder {
  /** The loaded articles in reading order; empty when the list is not asked for (`enabled` false). */
  ids: string[];
  /** The loaded articles in the server's order, whatever is cached, asked for or not. */
  cards: Card[];
  /** The server has more articles than are loaded. */
  more: boolean;
  /** Load the next page: the reading order after it, and whether there is still more to load. Rejects when the page could not be loaded. */
  fetchMore: () => Promise<{ ids: string[]; more: boolean }>;
}

/**
 * The list an open article belongs to, in the order the reader reads it, taken from the list's cache so it works with
 * no list mounted. In the Gazette (#315) that is the order the page prints the stories, from the same plan the list
 * draws over the loaded list; the plan is the same on a phone and on a wide screen up to its columns, so the order does
 * not depend on the screen. Until the paper has a front page (it waits for the lead window), and for an article
 * outside the plan (not laid out yet), the loaded order stands. In the Digest it is the sections in order and the
 * articles each shows (a section opened out with "N more" counts the articles it shows then); paging past the last one
 * asks for the next sections. Every other layout, and a search, reads the loaded order (newest first, as the server
 * sends it, or the order the list was asked for).
 */
export function useListOrder(scope: Scope, id: string, enabled: boolean): ListOrder {
  const { layout } = useResolvedLayout(scope);
  const cut = layout.cut;
  const digested = !!cut && !!pageOf(layout, scope);
  const list = useItems(scope, enabled && !digested);
  const digest = useDigestSource(scope, digested, enabled, cut);
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
  const listed = useMemo(() => flattenItems(list.data), [list.data]);
  const cards = digested ? digest.loadedCards : listed;
  const more = digested ? digest.more : !!list.hasNextPage;
  const ids = useMemo(() => {
    if (!enabled) return [];
    return digested ? digest.cards.map((c) => c.id) : orderOf(listed, more);
  }, [listed, digest.cards, digested, more, enabled, orderOf]);
  const fetchMore = async () => {
    if (digested) {
      const r = await digest.loadMore();
      return { ids: r.cards.map((c) => c.id), more: r.more };
    }
    const r = await list.fetchNextPage();
    if (r.isFetchNextPageError) throw r.error; // a page that failed to load is not the end of the list
    return { ids: orderOf(flattenItems(r.data), !!r.hasNextPage), more: !!r.hasNextPage };
  };
  return { ids, cards, more, fetchMore };
}
