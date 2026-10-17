import { useLayoutEffect, useRef, type ReactNode } from "react";
import { useWide } from "@/lib/useMedia";

/** The CSS variable the toast region reads: the height of the screen's own bar above the tab bar (0 when none). */
export const BOTTOM_BAR_VAR = "--bottom-bar-h";

/**
 * A bar at the bottom of a screen's content (the selection bars of Feeds and Feed Health). It publishes its height,
 * so the toasts, which sit in a fixed region of the shell, stand above it instead of covering its buttons.
 * A screen with a bottom bar uses this component and nothing else has to know about it.
 *
 * The device's bottom inset (the home indicator) is counted once, by the toast region. The tab bar below a phone
 * screen pays it, so the bar there does not; with no tab bar (wide) the bar is the bottom-most thing and pays it in a
 * spacer outside the measured region, so the published height never includes it.
 */
export function BottomBar({ ariaLabel, children }: { ariaLabel: string; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const wide = useWide();
  // Layout effect: the height is published before the first frame, so a toast never paints behind the bar.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const root = document.documentElement;
    const publish = () => root.style.setProperty(BOTTOM_BAR_VAR, `${el.getBoundingClientRect().height}px`);
    publish();
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(publish);
    ro?.observe(el, { box: "border-box" });
    return () => {
      ro?.disconnect();
      root.style.removeProperty(BOTTOM_BAR_VAR);
    };
  }, []);
  return (
    <>
      <div ref={ref} role="region" aria-label={ariaLabel} className="flex shrink-0 flex-wrap items-center gap-2 border-t border-line bg-surface px-4 py-2">
        {children}
      </div>
      {wide ? <div aria-hidden="true" className="pb-safe shrink-0 bg-surface" /> : null}
    </>
  );
}
