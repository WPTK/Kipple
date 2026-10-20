import { useLayoutEffect } from "react";

/**
 * The pathname of each history entry this page has shown, by the entry's position (the router keeps it as
 * `history.state.idx`). Visiting an entry again, by Back, Forward or a replace, rewrites its slot, so the slot before
 * the current one is always the entry Back would return to. A reload empties it; the entry before is then unknown.
 */
const trail = new Map<number, string>();

const position = () => (window.history.state as { idx?: number } | null)?.idx ?? 0;

/** Records the current screen. Called by the app shell on every change of location. */
export function useRecordTrail(pathname: string): void {
  useLayoutEffect(() => {
    trail.set(position(), pathname);
  }, [pathname]);
}

/** True when the entry Back would return to is the screen at `pathname`. */
export function previousEntryIs(pathname: string): boolean {
  const idx = position();
  return idx > 0 && trail.get(idx - 1) === pathname;
}
