import { useEffect } from "react";

export const APP_TITLE = "Kipple";

/** The tab title for a screen: "<heading> - Kipple". Without a heading it is just "Kipple". */
export function documentTitle(heading?: string): string {
  const h = heading?.trim();
  return h ? `${h} - ${APP_TITLE}` : APP_TITLE;
}

/**
 * Names the page for the window switcher, the tab strip, history and screen readers (WCAG 2.4.2). Every screen calls
 * it with its own h1 text; a screen that has nothing to say yet (still loading) passes undefined and the title stays
 * "Kipple". The title goes back to "Kipple" when the screen leaves, so a screen that forgets the call never shows the
 * previous one's name.
 */
export function useDocumentTitle(heading?: string): void {
  useEffect(() => {
    if (heading === undefined) return;
    document.title = documentTitle(heading);
    return () => {
      document.title = APP_TITLE;
    };
  }, [heading]);
}
