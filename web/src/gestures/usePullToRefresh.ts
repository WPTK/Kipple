import { useEffect, useRef, useState, type RefObject } from "react";
import { gestureLock } from "./lock";
import { PULL, PullTracker } from "./tracking";

export interface PullState {
  /** Displayed pull distance in px (after resistance). */
  distance: number;
  armed: boolean;
  /** Finger is down and pulling (drives "no transition while dragging"). */
  dragging: boolean;
}

const IDLE: PullState = { distance: 0, armed: false, dragging: false };

/**
 * Pull to refresh on a scroller. Uses touch events, not pointer events: at scrollTop 0 the
 * browser would otherwise start its own overscroll and cancel the pointer stream. A
 * non-passive touchmove blocks the Safari-tab rubber band once the pull passes 12 px, and the
 * scroller's overscroll-behavior keeps an installed PWA from bouncing on top of it.
 */
export function usePullToRefresh(
  ref: RefObject<HTMLElement | null>,
  { enabled, onRefresh }: { enabled: boolean; onRefresh: () => void },
): PullState {
  const [state, setState] = useState<PullState>(IDLE);
  const cb = useRef(onRefresh);
  useEffect(() => {
    cb.current = onRefresh;
  });

  useEffect(() => {
    const el = ref.current;
    if (!enabled || !el) return;
    const t = new PullTracker();
    const push = () => setState({ distance: t.distance, armed: t.armed(), dragging: t.phase === "pulling" });

    const start = (e: TouchEvent) => {
      if (e.touches.length !== 1) {
        t.phase = "dead";
        return;
      }
      const p = e.touches[0] as Touch;
      t.start(p.clientX, p.clientY, el.scrollTop);
    };
    const move = (e: TouchEvent) => {
      if (e.touches.length !== 1) {
        if (t.phase !== "idle") {
          t.phase = "dead";
          t.distance = 0;
          push();
        }
        return;
      }
      const p = e.touches[0] as Touch;
      const phase = t.move(p.clientX, p.clientY, el.scrollTop, gestureLock.rowSwipe);
      if (phase === "pulling") {
        if (t.shouldPreventDefault() && e.cancelable) e.preventDefault();
        push();
      } else if (phase === "dead") {
        setState((s) => (s.distance === 0 && !s.dragging ? s : IDLE));
      }
    };
    const end = () => {
      if (t.phase === "idle") return;
      const go = t.end();
      setState(IDLE);
      if (go) cb.current();
    };

    el.addEventListener("touchstart", start, { passive: true });
    el.addEventListener("touchmove", move, { passive: false });
    el.addEventListener("touchend", end);
    el.addEventListener("touchcancel", end);
    return () => {
      el.removeEventListener("touchstart", start);
      el.removeEventListener("touchmove", move);
      el.removeEventListener("touchend", end);
      el.removeEventListener("touchcancel", end);
    };
  }, [ref, enabled]);

  return state;
}

export { PULL };
