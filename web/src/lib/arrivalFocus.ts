import { useEffect } from "react";

/**
 * Where focus was in a history entry, so Back and Forward can give it back. The router keeps `{ usr, key, idx }` in
 * `history.state`; the row last focused is stored beside them (`kpRow`), in the entry the reader was on, and read when
 * that entry is arrived at again. A reload keeps it too, which a module variable would not.
 */
const KEY = "kpRow";

/** Records the list row that takes focus, in the current history entry. Mounted once by the app shell. */
export function useRememberFocusedRow(): void {
  useEffect(() => {
    const remember = (e: Event) => {
      const id = e.target instanceof Element ? e.target.closest<HTMLElement>("[data-item-id]")?.dataset.itemId : undefined;
      if (!id) return;
      try {
        window.history.replaceState({ ...(window.history.state as object | null), [KEY]: id }, "");
      } catch {
        /* a sandboxed frame may refuse; the heading takes focus instead */
      }
    };
    // focusin covers the keyboard; a click covers a browser that does not focus a link when it is pressed.
    document.addEventListener("focusin", remember);
    document.addEventListener("click", remember);
    return () => {
      document.removeEventListener("focusin", remember);
      document.removeEventListener("click", remember);
    };
  }, []);
}

/** Gives focus back to the row the reader left this history entry on. False when there is none or it is not on the page. */
export function restoreFocusedRow(within: HTMLElement | null): boolean {
  const id = (window.history.state as Record<string, unknown> | null)?.[KEY];
  if (typeof id !== "string") return false;
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
