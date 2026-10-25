import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { prefsStore } from "./prefs";
import { useStoreSelector } from "./store";

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

// A control is 2.75rem square (Button size "icon", `size-11`); Large targets make every button at least 3.5rem
// (index.css, `data-targets="large"`). Everything below is in rem: it scales with the root font size (the text size).
const CONTROL_REM = 2.75;
const CONTROL_REM_LARGE = 3.5;
// The article toolbar (ArticlePane.tsx, `Toolbar`): seven controls and 0.25rem of padding on each side.
const TOOLBAR_CONTROLS = 7;
const TOOLBAR_PAD_REM = 0.5;
// The list header (ReaderRoute.tsx, `ScopeHeader`, wide): seven controls with six 0.125rem gaps, after 1rem of padding.
const HEADER_CONTROLS = 7;
const HEADER_PAD_REM = 6 * 0.125 + 1;
// The strip a resize handle lies over (ui/ResizeHandle.tsx): each wide column after the first starts this far to the
// right of its neighbour (`pl-6` on the main area and on the article column).
const GUTTER_REM = 1.5;

/** What the columns need, in px: the article toolbar, the list header and the strip before each column after the first. */
export interface ColumnNeeds {
  article: number;
  list: number;
  gutter: number;
}

/**
 * What the article toolbar and the list header need to show their controls whole, for a root font size of `remPx` (44 px
 * controls at 16 px: 316 and 336, with 24 px gutters). The sidebar yields to these first and then the list does, so at
 * the 900 px breakpoint the default text size shows all three columns whole. They are what the layout reserves, not a
 * promise about what is on screen: where the window cannot hold the sidebar at its 200 px floor, the list and the article
 * (Large targets or a larger text size on a phone held sideways), the article keeps its need and the list gives way to its
 * 260 px floor, and a list the user dragged narrower than its need keeps the width they chose. uat/layout-walk.mjs measures
 * the real toolbar and header at these sizes, so a control that changes size without these constants fails there.
 */
export function columnNeeds(remPx: number, largeTargets: boolean): ColumnNeeds {
  const control = largeTargets ? CONTROL_REM_LARGE : CONTROL_REM;
  return {
    article: Math.ceil((TOOLBAR_CONTROLS * control + TOOLBAR_PAD_REM) * remPx),
    list: Math.ceil((HEADER_CONTROLS * control + HEADER_PAD_REM) * remPx),
    gutter: Math.ceil(GUTTER_REM * remPx),
  };
}

/** What 1rem is in px: the root font size, which follows the text size (`--kp-scale`, index.css). It only changes with that setting, so it is read then. */
export function useRemPx(): number {
  const textSize = useStoreSelector(prefsStore, (p) => p.textSize);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => (typeof document === "undefined" ? 16 : parseFloat(getComputedStyle(document.documentElement).fontSize) || 16), [textSize]);
}

/** `columnNeeds` for the text size and the Large targets setting now in force. */
export function useColumnNeeds(): ColumnNeeds {
  const remPx = useRemPx();
  const large = useStoreSelector(prefsStore, (p) => p.largeTargets);
  return useMemo(() => columnNeeds(remPx, large), [remPx, large]);
}

/**
 * The widest a resizable column may be inside `total` px when `rest` px must stay for what is beside it. The
 * fixed limit wins when the space is unknown (0, before the first measure) and the minimum wins when it is tight,
 * so the value a handle reports is always one the layout can actually show.
 */
export function maxFor(total: number, rest: number, min: number, max: number): number {
  if (total <= 0) return max;
  return Math.max(min, Math.min(max, total - rest));
}

/** Calls `fn` when the window resizes or rotates; returns what stops it. */
function onResize(fn: () => void): () => void {
  window.addEventListener("resize", fn);
  return () => window.removeEventListener("resize", fn);
}

/** The window's inner width, following resizes (1024 where there is no window). */
export function useViewportWidth(): number {
  return useSyncExternalStore(onResize, () => window.innerWidth, () => 1024);
}

/**
 * The right safe-area inset in px (the notch of a phone held sideways), read from the main area that applies it
 * (`pr-safe`) and read again after each resize or rotation. The window is that much narrower for the columns than
 * `innerWidth`.
 */
export function useSafeInsetRight(): number {
  return useSyncExternalStore(
    onResize,
    () => {
      const main = document.getElementById("main");
      return main ? parseFloat(getComputedStyle(main).paddingRight) || 0 : 0;
    },
    () => 0,
  );
}
