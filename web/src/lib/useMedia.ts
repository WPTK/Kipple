import { useSyncExternalStore } from "react";

/** Subscribe to a media query. Falls back to `false` when matchMedia is unavailable. */
export function useMedia(query: string): boolean {
  return useSyncExternalStore(
    (cb) => {
      let m: MediaQueryList | null = null;
      try {
        m = window.matchMedia(query);
        m.addEventListener("change", cb);
      } catch {
        /* no matchMedia */
      }
      return () => m?.removeEventListener("change", cb);
    },
    () => {
      try {
        return window.matchMedia(query).matches;
      } catch {
        return false;
      }
    },
    () => false,
  );
}

/** Wide layout: sidebar plus list plus reader pane (design: container >= 900 px). */
export const WIDE_QUERY = "(min-width: 900px)";
export function useWide(): boolean {
  return useMedia(WIDE_QUERY);
}
