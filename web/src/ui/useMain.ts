import { useSyncExternalStore } from "react";

const subscribe = () => () => {};
const mainNow = () => document.getElementById("main");
const noMain = () => null;

/**
 * The page's main landmark, where a menu or popover is drawn so it is inside a landmark (axe "region") and not loose in
 * the body. A component inside the landmark first renders before the landmark is in the document; React reads the
 * snapshot again after the commit and renders with the element. Null in a bare test render: Radix falls back to the body.
 */
export function useMain(): HTMLElement | null {
  return useSyncExternalStore(subscribe, mainNow, noMain);
}
