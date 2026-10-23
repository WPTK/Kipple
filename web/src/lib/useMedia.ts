import { useCallback, useSyncExternalStore } from "react";
import { DEFAULT_DEVICE_PREFS } from "./devicePrefs";
import { ARTICLE_MIN, LIST_MIN_FOR_SIDEBAR } from "./useWidth";

/** Whether a media query matches right now; `false` when matchMedia is unavailable. */
export function mediaMatches(query: string): boolean {
  try {
    return window.matchMedia(query).matches;
  } catch {
    return false;
  }
}

/** Subscribe to a media query. Falls back to `false` when matchMedia is unavailable. */
export function useMedia(query: string): boolean {
  // Stable functions per query: an inline subscribe would unsubscribe and resubscribe every render.
  const subscribe = useCallback(
    (cb: () => void) => {
      let m: MediaQueryList | null = null;
      try {
        m = window.matchMedia(query);
        m.addEventListener("change", cb);
      } catch {
        /* no matchMedia */
      }
      return () => m?.removeEventListener("change", cb);
    },
    [query],
  );
  const getSnapshot = useCallback(() => mediaMatches(query), [query]);
  return useSyncExternalStore(subscribe, getSnapshot, () => false);
}

/**
 * A short screen: a phone held sideways (about 375 px or less of height). Its chrome has to take less of it. Landscape
 * is part of it, so an upright phone whose keyboard has taken most of the height keeps its layout while you type.
 * Any window that is landscape and 500 px or less tall counts, a short desktop window too: the limit is the height,
 * not the device (the sidebar layout, from the wide breakpoint, has no tab bar and ignores it). This is the one place the
 * query is written: the components read it with `useShort()`, and the app shell mirrors it as `data-short` on the
 * root element for the style sheet.
 */
export const SHORT_QUERY = "(orientation: landscape) and (max-height: 500px)";
export function useShort(): boolean {
  return useMedia(SHORT_QUERY);
}

/** Where the Show pills no longer fit the title row beside the controls and the feed switch (a short, narrow phone). */
export const NARROW_QUERY = "(max-width: 700px)";

/**
 * Wide layout: sidebar plus list plus reader pane. It starts where a sidebar at its default width still leaves the
 * list and the article their minimums (820 px), so the threshold follows the pane minimums and not a number of its own.
 * Below it the phone layout stays, with the tab bar (a tablet portrait).
 */
export const WIDE_MIN_PX = DEFAULT_DEVICE_PREFS.sidebarWidth + LIST_MIN_FOR_SIDEBAR + ARTICLE_MIN;
export const WIDE_QUERY = `(min-width: ${WIDE_MIN_PX}px)`;
export function useWide(): boolean {
  return useMedia(WIDE_QUERY);
}
