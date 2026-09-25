import { useEffect, useRef, type RefObject } from "react";
import { BackSwipe, prefersReducedMotion } from "./tracking";

const CONTROL = "input,textarea,select,button,video,audio,iframe,[contenteditable='true'],[data-no-swipe]";

/** Horizontally scrollable ancestor between the target and the container (tables, pre, code, wide figures). */
function inHorizontalScroller(target: Element, root: Element): boolean {
  for (let el: Element | null = target; el && el !== root; el = el.parentElement) {
    if (!(el instanceof HTMLElement)) continue;
    const ox = getComputedStyle(el).overflowX;
    if ((ox === "auto" || ox === "scroll") && (el.scrollWidth > el.clientWidth || el.scrollLeft > 0)) return true;
  }
  return false;
}

/**
 * True when a right-swipe starting here must be left alone: scrollable content, controls,
 * media, zoomable images, or an active text selection.
 */
export function swipeBackBlocked(target: EventTarget | null, root: Element): boolean {
  if (!(target instanceof Element)) return true;
  if (target.closest(CONTROL) || target.closest("[data-zoomable]")) return true;
  if (inHorizontalScroller(target, root)) return true;
  try {
    const sel = window.getSelection();
    if (sel && !sel.isCollapsed && sel.toString() !== "") return true;
  } catch {
    /* no selection API */
  }
  return false;
}

interface Opts {
  enabled: boolean;
  /** Pop to the previous screen (history back). */
  onBack: () => void;
}

/**
 * Article swipe-back: a right-swipe starting at least 24 px from the left edge, mostly
 * horizontal, pops to the screen you came from. The article follows the finger and springs
 * back on cancel; with reduced motion there is no drag animation, only the commit.
 */
export function useSwipeBack(ref: RefObject<HTMLElement | null>, { enabled, onBack }: Opts): void {
  const cb = useRef(onBack);
  useEffect(() => {
    cb.current = onBack;
  });

  useEffect(() => {
    const el = ref.current;
    if (!enabled || !el) return;
    const tracker = new BackSwipe();
    const touches = new Set<number>();
    let active: number | null = null;
    const reduced = () => prefersReducedMotion();

    const paint = (dx: number, animate: boolean) => {
      if (reduced()) return;
      el.style.transition = animate ? "transform 200ms cubic-bezier(0.2, 0, 0, 1)" : "none";
      el.style.transform = dx ? `translateX(${dx}px)` : "";
    };

    const down = (e: PointerEvent) => {
      if (e.pointerType !== "touch") return;
      touches.add(e.pointerId);
      if (touches.size > 1) {
        tracker.cancel();
        paint(0, true);
        return;
      }
      active = e.pointerId;
      tracker.start(e.clientX, e.clientY, performance.now(), el.clientWidth || window.innerWidth || 375, swipeBackBlocked(e.target, el));
    };
    const move = (e: PointerEvent) => {
      if (e.pointerId !== active) return;
      const before = tracker.phase;
      if (tracker.move(e.clientX, e.clientY, performance.now()) === "horizontal") {
        if (before !== "horizontal") {
          try {
            el.setPointerCapture(e.pointerId);
          } catch {
            /* synthetic pointer */
          }
        }
        paint(tracker.dx, false);
      }
    };
    const up = (e: PointerEvent) => {
      touches.delete(e.pointerId);
      if (e.pointerId !== active) return;
      active = null;
      const commit = tracker.end();
      paint(0, true);
      if (commit) cb.current();
    };
    const cancel = (e: PointerEvent) => {
      touches.delete(e.pointerId);
      if (e.pointerId !== active) return;
      active = null;
      tracker.cancel();
      paint(0, true);
    };

    el.addEventListener("pointerdown", down);
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", cancel);
    return () => {
      el.removeEventListener("pointerdown", down);
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", cancel);
      el.style.transform = "";
      el.style.transition = "";
    };
  }, [ref, enabled]);
}
