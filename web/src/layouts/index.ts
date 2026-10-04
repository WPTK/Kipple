import { useMemo } from "react";
import { useBootstrap, useFolderTree } from "@/api/queries";
import { chainOf, type FolderTree } from "@/lib/folderTree";
import type { Scope } from "@/api/types";
import { resolveLayout, sessionLayoutStore, useDevicePrefs, type LayoutContext, type LayoutId } from "@/lib/devicePrefs";
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

/** The layout in effect for a list: `c` toggle, then feed override, folder overrides up the tree, device default. */
export function useResolvedLayout(scope: Scope): { layout: ListLayout; ctx: LayoutContext } {
  const dp = useDevicePrefs();
  const session = useStore(sessionLayoutStore);
  const boot = useBootstrap();
  const tree = useFolderTree();
  const feeds = boot.data?.feeds;
  const ctx = useMemo(() => layoutContext(scope, feeds ?? [], tree), [scope, feeds, tree]);
  return { layout: getLayout(resolveLayout(dp, ctx, session)), ctx };
}

export type { ListLayout, RowProps, RowMenuActions } from "./types";
