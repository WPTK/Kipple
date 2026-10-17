import { useCallback, useEffect, useRef } from "react";

/**
 * Put the focus on the element with this id, and keep trying for a moment: after a move or a delete the row re-mounts
 * or a dialog's own focus return lands on a button that is about to go. It never takes the focus from anything the
 * person moved it to since. Returns a function that stops the retries.
 */
export function focusActionsButton(id: string): () => void {
  let tries = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let stopped = false;
  const tick = () => {
    const el = document.getElementById(id);
    const now = document.activeElement;
    const adrift = !now || now === document.body || !now.isConnected || (now instanceof HTMLElement && now.dataset.folderActions !== undefined);
    if (el && now !== el && adrift) el.focus();
    if (!stopped && ++tries < 20) timer = setTimeout(tick, 50);
  };
  timer = setTimeout(tick, 0);
  return () => {
    stopped = true;
    clearTimeout(timer);
  };
}

/** `focusActionsButton` tied to a component: a newer call replaces the one still running, and unmounting stops it. */
export function useFocusActions(): (id: string) => void {
  const stop = useRef<(() => void) | null>(null);
  useEffect(() => () => stop.current?.(), []);
  return useCallback((id: string) => {
    stop.current?.();
    stop.current = focusActionsButton(id);
  }, []);
}
