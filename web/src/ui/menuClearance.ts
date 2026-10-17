import { useEffect, useState } from "react";

/** How far a menu keeps off the bottom edge: 8 px, or past the primary tab bar (its safe-area padding included) when it shows. */
export function tabBarClearance(): number {
  const top = document.querySelector('nav[aria-label="Primary"]')?.getBoundingClientRect().top;
  // The wide layout's sidebar is the same landmark but runs down the side, so only a bar in the lower half counts.
  return top !== undefined && top > window.innerHeight / 2 ? window.innerHeight - top + 8 : 8;
}

/**
 * Room a dropdown keeps clear at the bottom (the tab bar and the home-indicator zone), measured while `open` and again
 * when the window or the visual viewport resizes (rotation, the address bar). Feed it to Radix `collisionPadding.bottom`,
 * next to `menuScrollClass` so the menu caps its height to what is left and scrolls.
 */
export function useTabBarClearance(open: boolean): number {
  const [pad, setPad] = useState(8);
  useEffect(() => {
    if (!open) return;
    const measure = () => setPad(tabBarClearance());
    measure();
    window.addEventListener("resize", measure);
    window.visualViewport?.addEventListener("resize", measure);
    return () => {
      window.removeEventListener("resize", measure);
      window.visualViewport?.removeEventListener("resize", measure);
    };
  }, [open]);
  return pad;
}

/** Caps a Radix dropdown's height to the space Radix says is available and scrolls the rest. */
export const menuScrollClass =
  "max-h-[var(--radix-dropdown-menu-content-available-height)] overflow-y-auto overscroll-contain [&::-webkit-scrollbar]:w-1.5 [&::-webkit-scrollbar-thumb]:rounded-full [&::-webkit-scrollbar-thumb]:bg-fg2";
