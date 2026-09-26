import { useEffect, useState, useSyncExternalStore } from "react";

/** The content width of an element, kept current by a ResizeObserver (0 until measured). */
export function useWidth(ref: React.RefObject<HTMLElement | null>): number {
  const [w, setW] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const e = entries[0];
      if (e) setW(Math.round(e.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return w;
}

/** Room to leave for the article beside the list, and for the list and article beside the sidebar. */
export const ARTICLE_MIN = 320;
export const LIST_MIN_FOR_SIDEBAR = 260;

/**
 * The widest a resizable column may be inside `total` px when `rest` px must stay for what is beside it. The
 * fixed limit wins when the space is unknown (0, before the first measure) and the minimum wins when it is tight,
 * so the value a handle reports is always one the layout can actually show.
 */
export function maxFor(total: number, rest: number, min: number, max: number): number {
  if (total <= 0) return max;
  return Math.max(min, Math.min(max, total - rest));
}

/** The window's inner width, following resizes (1024 where there is no window). */
export function useViewportWidth(): number {
  return useSyncExternalStore(
    (fn) => {
      window.addEventListener("resize", fn);
      return () => window.removeEventListener("resize", fn);
    },
    () => window.innerWidth,
    () => 1024,
  );
}
