import { useMemo } from "react";
import { useBootstrap } from "@/api/queries";
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

export function getLayout(id: LayoutId): ListLayout {
  return layouts[id] ?? magazine;
}

/** Feed and folder a list belongs to, for override resolution. A feed list also inherits its folder's override. */
export function layoutContext(scope: Scope, feeds: { id: string; folder_id: string }[]): LayoutContext {
  if (scope.feed) return { feedId: scope.feed, folderId: feeds.find((f) => f.id === scope.feed)?.folder_id };
  if (scope.folder) return { folderId: scope.folder };
  return {};
}

/** The layout in effect for a list: `c` toggle, then feed override, folder override, device default. */
export function useResolvedLayout(scope: Scope): { layout: ListLayout; ctx: LayoutContext } {
  const dp = useDevicePrefs();
  const session = useStore(sessionLayoutStore);
  const boot = useBootstrap();
  const feeds = boot.data?.feeds;
  const ctx = useMemo(() => layoutContext(scope, feeds ?? []), [scope, feeds]);
  return { layout: getLayout(resolveLayout(dp, ctx, session)), ctx };
}

export type { ListLayout, RowProps, RowMenuActions } from "./types";
