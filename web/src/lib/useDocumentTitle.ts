import { useEffect } from "react";

export const APP_TITLE = "Kipple";

/**
 * The tab title for a screen: "<heading> - Kipple". Without a heading it is just "Kipple". A screen whose own heading
 * already starts with the app name ("Sign in to Kipple") passes `standalone` so the name is not said twice; article
 * titles, feed names and the like never do, so they always carry the suffix.
 */
export function documentTitle(heading?: string, standalone = false): string {
  const h = heading?.trim();
  if (!h) return APP_TITLE;
  return standalone ? h : `${h} - ${APP_TITLE}`;
}

/**
 * Names the page for the window switcher, the tab strip, history and screen readers (WCAG 2.4.2). Every screen calls
 * it with its own h1 text; a screen that has nothing to say yet (still loading) passes undefined and the title stays
 * "Kipple". The title goes back to "Kipple" when the screen leaves, so a screen that forgets the call never shows the
 * previous one's name.
 */
export function useDocumentTitle(heading?: string, standalone = false): void {
  useEffect(() => {
    if (heading === undefined) return;
    document.title = documentTitle(heading, standalone);
    return () => {
      document.title = APP_TITLE;
    };
  }, [heading, standalone]);
}
