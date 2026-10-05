import { useMemo } from "react";
import { useBootstrap, useFolderTree } from "@/api/queries";
import { chainOf, type FolderTree } from "@/lib/folderTree";
import type { Scope } from "@/api/types";
import {
  resolveLayout,
  sessionLayoutStore,
  setListOverride,
  useDevicePrefs,
  type KnownLists,
  type LayoutContext,
  type LayoutId,
  type ListField,
  type ListOverride,
} from "@/lib/devicePrefs";
import { useStore } from "@/lib/store";
import { cards } from "./cards";
import { compact } from "./compact";
import { headlines } from "./headlines";
import { inbox } from "./inbox";
import { magazine } from "./magazine";
import type { ListLayout } from "./types";

const layouts: Record<LayoutId, ListLayout> = { magazine, cards, compact, inbox, headlines };

function getLayout(id: LayoutId): ListLayout {
  return layouts[id] ?? magazine;
}

/**
 * Feed and folders a list belongs to, for override resolution. A folder list inherits the override of the nearest
 * folder above it that has one; a feed list also inherits its folder's (and so on up).
 */
export function layoutContext(scope: Scope, feeds: readonly { id: string; folder_id: string }[], tree: FolderTree<FolderLike>): LayoutContext {
  if (scope.feed) {
    const folder = feeds.find((f) => f.id === scope.feed)?.folder_id;
    return { feedId: scope.feed, folderIds: folder ? chainOf(tree, folder) : [] };
  }
  if (scope.folder) return { folderIds: chainOf(tree, scope.folder) };
  return {};
}
type FolderLike = { id: string; name: string; parent_id?: string | null };

/** The feed and folders a list's overrides resolve through (layoutContext over the cached bootstrap). */
export function useListContext(scope: Pick<Scope, "feed" | "folder">): LayoutContext {
  const boot = useBootstrap();
  const tree = useFolderTree();
  const feeds = boot.data?.feeds;
  const { feed, folder } = scope;
  return useMemo(() => layoutContext({ view: "unread", feed, folder }, feeds ?? [], tree), [feed, folder, feeds, tree]);
}

/**
 * setListOverride bound to the library the bootstrap lists, so a write also drops overrides of deleted feeds and
 * folders. A bootstrap the service worker answered from its stored copy may predate a new feed, so it prunes nothing.
 */
export function useSetListOverride(): <F extends ListField>(kind: "feed" | "folder", id: string, field: F, value: ListOverride[F] | null) => void {
  const data = useBootstrap().data;
  return useMemo(() => {
    const known: KnownLists | undefined =
      data && !data.fromCache ? { feeds: new Set(data.feeds.map((f) => f.id)), folders: new Set(data.folders.map((f) => f.id)) } : undefined;
    return (kind, id, field, value) => setListOverride(kind, id, field, value, known);
  }, [data]);
}

/** The layout in effect for a list: `c` toggle, then feed override, folder overrides up the tree, device default. */
export function useResolvedLayout(scope: Scope): { layout: ListLayout; ctx: LayoutContext } {
  const dp = useDevicePrefs();
  const session = useStore(sessionLayoutStore);
  const ctx = useListContext(scope);
  return { layout: getLayout(resolveLayout(dp, ctx, session)), ctx };
}

export type { ListLayout, RowProps, RowMenuActions } from "./types";
