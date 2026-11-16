import type { InfiniteData } from "@tanstack/react-query";
import type { Card, ItemsPage } from "@/api/types";
import { arrivedAfter } from "./itemActions";

/**
 * The articles of a list read a page at a time, as the list stood when its first page was read. A later page is read
 * from the live store, so an article that arrived after the first page (dated back, it falls inside a later page)
 * would join the list in the middle while one dated at arrival waits above for a reload. Both wait: a later page
 * leaves out what is newer than the first page's `as_of`, and the list offers it as new articles. The first page is
 * kept whole. A list whose first page carries no `as_of` (a cache from an older server) is kept whole. So is a list in
 * relevance order (`ranked`): it has no arrival order to hold, and its pages are cut by a rank that the live index
 * moves, so leaving an article out would not make its pages stable.
 *
 * The bound is the `as_of` of the first page the data holds now: a list read again (a refetch reads every page, the
 * first one too) is a new snapshot with its own bound, whole from its first page to its last.
 */
export function listCards(data: InfiniteData<ItemsPage> | undefined, ranked = false): Card[] {
  if (!data) return [];
  const asOf = ranked ? undefined : data.pages[0]?.as_of;
  return data.pages.flatMap((p, i) => (i === 0 || !asOf ? p.items : p.items.filter((c) => !arrivedAfter(c.id, asOf))));
}
