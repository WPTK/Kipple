import { useSyncExternalStore } from "react";

/** The window's width in "text-size-1 pixels": the Text size setting scales every rem, so a wider text leaves less room. */
function scaledWidth(): number {
  const px = parseFloat(getComputedStyle(document.documentElement).fontSize);
  const scale = Number.isFinite(px) && px > 0 ? px / 16 : 1;
  return window.innerWidth / scale;
}

function subscribe(cb: () => void): () => void {
  window.addEventListener("resize", cb);
  // The text size is set on the root element's style, which no media query can see.
  const mo = new MutationObserver(cb);
  mo.observe(document.documentElement, { attributes: true, attributeFilter: ["style", "data-targets", "class"] });
  return () => {
    window.removeEventListener("resize", cb);
    mo.disconnect();
  };
}

/**
 * Whether the window is under `limit` px wide once the text size is counted. A media query cannot ask this: at 1.5x
 * text a 375 px phone has the room of a 250 px one, and a layout that wraps there wants the compact form.
 */
export function useNarrowText(limit: number): boolean {
  return useSyncExternalStore(subscribe, () => scaledWidth() < limit, () => false);
}
