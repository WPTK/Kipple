import type { Dispatch, FocusEvent, KeyboardEvent, RefObject, SetStateAction } from "react";
import type { Virtualizer } from "@tanstack/react-virtual";
import type { Card } from "@/api/types";
import { focusRow, pageOrder, rowKeyMove, storyIntoView } from "@/layouts/rowKeys";
import { STOP_PREFIX } from "@/layouts/rowTabStop";

interface Args {
  paged: boolean;
  parentRef: RefObject<HTMLDivElement | null>;
  items: readonly Card[];
  cols: number;
  setFocusId: Dispatch<SetStateAction<string | undefined>>;
  /** The reader has moved the page, so a pending restore gives up. */
  pageRestored: { current: boolean };
  virtualizer: Virtualizer<HTMLDivElement, Element>;
  rowIndexOf: (id: string) => number;
}

/**
 * The arrow keys between rows (layouts/rowKeys.ts). They only move focus: they select nothing and open nothing, so
 * looking through the list marks nothing read. The list is one Tab stop (and Safari's Tab skips links), so these keys
 * are how the keyboard reaches the other rows.
 */
export function useListKeyboard({ paged, parentRef, items, cols, setFocusId, pageRestored, virtualizer, rowIndexOf }: Args) {
  const onRowKey = (e: KeyboardEvent) => {
    // A page's keys visit its stories and its buttons (the Digest's "N more") in the order they are drawn.
    const moved = rowKeyMove(
      e,
      () => (paged ? pageOrder(parentRef.current) : items.map((i) => i.id)),
      (row) => Math.max(1, Math.floor((parentRef.current?.clientHeight ?? 0) / Math.max(1, row.offsetHeight))) * cols,
    );
    const next = moved?.to;
    if (!next) return;
    if (!next.startsWith(STOP_PREFIX)) setFocusId(next);
    pageRestored.current = true; // moving by key is the reader moving the page
    if (paged) storyIntoView(parentRef.current, next);
    else virtualizer.scrollToIndex(rowIndexOf(next), { align: "auto" });
    requestAnimationFrame(() => requestAnimationFrame(() => focusRow(parentRef.current, next)));
  };

  /** The row that takes focus in the list (its own focus, or one of its controls) becomes the list's Tab stop. */
  const onListFocus = (e: FocusEvent) => {
    const id = e.target instanceof HTMLElement ? e.target.closest<HTMLElement>("[data-item-id],[data-muted-for]") : null;
    const rowId = id?.dataset.itemId ?? id?.dataset.mutedFor;
    if (rowId) setFocusId(rowId);
  };

  return { onRowKey, onListFocus };
}
