import { useCallback, useMemo, useState } from "react";
import { defaultRangeExtractor, type Range } from "@tanstack/react-virtual";
import type { Card } from "@/api/types";
import type { Row } from "@/lib/format";

export type VRow = Row<Card> | { kind: "group"; key: string; items: Card[] };

/** The rows to render: the visible window and its overscan, plus the list's Tab stop row wherever it is. */
export function keepMounted(range: Range, stopIndex: number): number[] {
  const indexes = defaultRangeExtractor(range);
  if (stopIndex < 0 || indexes.includes(stopIndex)) return indexes;
  return stopIndex < (indexes[0] ?? 0) ? [stopIndex, ...indexes] : [...indexes, stopIndex];
}

/** The selected row, the row that last had focus, and the one Tab stop they decide. */
export function useListSelection(activeId: string | undefined, savedSelectedId: string | undefined, items: readonly Card[], rows: readonly VRow[]) {
  const [selectedId, setSelectedId] = useState<string | undefined>(savedSelectedId);
  const selected = activeId ?? selectedId;
  // The list is one Tab stop (rowTabStop.ts): the row that last had focus, else the selected row, else the first. Its
  // row is always rendered, so Tab into the list finds it wherever the list is scrolled. Opening another article (Next
  // in the reader, say) makes it the selected row again: Shift+Tab from the reader comes back to the open article.
  const [focusId, setFocusId] = useState<string | undefined>(undefined);
  const [stopFor, setStopFor] = useState(activeId);
  if (stopFor !== activeId) {
    setStopFor(activeId);
    setFocusId(undefined);
  }
  const shown = (id: string | undefined, of: readonly Card[]) => (id && of.some((i) => i.id === id) ? id : undefined);
  const tabStopId = shown(focusId, items) ?? shown(selected, items) ?? items[0]?.id;
  const stopIndex = useMemo(
    () => (tabStopId ? rows.findIndex((r) => (r.kind === "item" ? r.item.id === tabStopId : r.kind === "group" && r.items.some((i) => i.id === tabStopId))) : -1),
    [rows, tabStopId],
  );
  const rangeExtractor = useCallback((range: Range) => keepMounted(range, stopIndex), [stopIndex]);
  return { selectedId, setSelectedId, selected, focusId, setFocusId, shown, tabStopId, rangeExtractor };
}
