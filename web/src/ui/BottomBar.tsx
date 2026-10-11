import { useEffect, useRef, type ReactNode } from "react";

/** The CSS variable the toast region reads: the height of the screen's own bar above the tab bar (0 when none). */
export const BOTTOM_BAR_VAR = "--bottom-bar-h";

/**
 * A bar at the bottom of a screen's content (the selection bars of Feeds and Feed Health). It publishes its height,
 * so the toasts, which sit in a fixed region of the shell, stand above it instead of covering its buttons.
 * A screen with a bottom bar uses this component and nothing else has to know about it.
 */
export function BottomBar({ label, children }: { label: string; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const root = document.documentElement;
    const publish = () => root.style.setProperty(BOTTOM_BAR_VAR, `${el.getBoundingClientRect().height}px`);
    publish();
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(publish);
    ro?.observe(el);
    return () => {
      ro?.disconnect();
      root.style.removeProperty(BOTTOM_BAR_VAR);
    };
  }, []);
  return (
    <div ref={ref} role="region" aria-label={label} className="pb-safe flex shrink-0 flex-wrap items-center gap-2 border-t border-line bg-surface px-4 py-2">
      {children}
    </div>
  );
}
