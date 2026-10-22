import { useCallback } from "react";
import { useBootstrap } from "@/api/queries";
import type { Card, Scope } from "@/api/types";
import { useFavorites } from "@/lib/favorites";
import { pageOf, useResolvedLayout } from "@/layouts";
import { planGazette, readingOrder } from "./gazettePlan";

/**
 * An open article's previous and next when the list is the Gazette (#315): the stories in the order the page prints
 * them, from the same plan the list draws over the loaded list. Null where the list is not a page (every other layout,
 * and a search). Called with the loaded cards (newest first, as the server sends them) and whether the server has more.
 * The plan is the same on a phone and on a wide screen up to its columns, so the order does not depend on the screen.
 * Until the paper has a front page (it waits for the lead window) the loaded order stands.
 */
export function useStoryOrder(scope: Scope): ((cards: readonly Card[], more: boolean) => string[]) | null {
  const { layout } = useResolvedLayout(scope);
  const boot = useBootstrap();
  const { favorites } = useFavorites();
  const feeds = boot.data?.feeds;
  const folders = boot.data?.folders;
  const { feed, folder } = scope;
  const paged = !!pageOf(layout, scope);
  const order = useCallback(
    (cards: readonly Card[], more: boolean) => {
      const plan = planGazette({ articles: cards, feeds: feeds ?? [], folders: folders ?? [], favorites, scope: { feed, folder }, screen: "wide", more });
      return plan.pages.length ? readingOrder(plan) : cards.map((c) => c.id);
    },
    [feeds, folders, favorites, feed, folder],
  );
  return paged ? order : null;
}
