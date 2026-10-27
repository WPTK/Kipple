import { useEffect } from "react";

/**
 * Where focus was in each history entry, so Back and Forward can give it back. The router stamps every entry with a
 * `key` (in `history.state`); the list row last focused or activated is remembered in memory against the key of the entry
 * it happened in, and read when that entry is arrived at again. Nothing is written to the history, which a browser
 * limits and a reload clears anyway (a reload starts at the page load, which has its own focus rule).
 */
const rows = new Map<string, string>();
const LIMIT = 200;

/** The key of the history entry being shown. */
function entryKey(): string {
  return (window.history.state as { key?: string } | null)?.key ?? "default";
}

function remember(e: Event): void {
  const id = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-item-id]")?.dataset.itemId : undefined;
  if (!id) return;
  const key = entryKey();
  rows.delete(key);
  rows.set(key, id);
  if (rows.size > LIMIT) rows.delete(rows.keys().next().value as string);
}

/**
 * Records the list row that takes focus or is activated (a click, a tap or Enter). A press that only starts a scroll on a
 * phone is not an activation, so Back does not focus a row the reader merely touched. Listening in the capture phase
 * means the entry is still the one being left when the click starts a navigation. Mounted once by the app shell.
 */
export function useRememberFocusedRow(): void {
  useEffect(() => {
    document.addEventListener("focusin", remember, true);
    document.addEventListener("click", remember, true);
    return () => {
      document.removeEventListener("focusin", remember, true);
      document.removeEventListener("click", remember, true);
    };
  }, []);
}

/** Gives focus back to the row the reader left this history entry on. False when there is none or it is not on the page. */
export function restoreFocusedRow(within: HTMLElement | null): boolean {
  const id = rows.get(entryKey());
  if (id === undefined) return false;
  const link = within?.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"] a`);
  if (!link) return false;
  link.focus({ preventScroll: true });
  return true;
}

/** True when focus is nowhere useful: the page body, the main region itself, or an element that has left the page. */
export function focusIsLost(main: HTMLElement | null): boolean {
  const at = document.activeElement;
  return at === null || at === document.body || at === main || !at.isConnected;
}

/** Forgets everything remembered (tests). */
export function forgetFocusedRows(): void {
  rows.clear();
}
