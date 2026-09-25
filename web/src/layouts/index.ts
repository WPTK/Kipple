import type { LayoutId } from "@/lib/prefs";
import { magazine } from "./magazine";
import type { ListLayout } from "./types";

// Register new layouts here (cards, compact, inbox, headlines).
const layouts: Record<LayoutId, ListLayout> = { magazine };

export function getLayout(id: LayoutId): ListLayout {
  return layouts[id] ?? magazine;
}

export type { ListLayout, RowProps } from "./types";
