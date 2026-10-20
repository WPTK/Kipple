import { type ComponentProps, useRef } from "react";
import { DropdownMenu } from "radix-ui";
import { cn } from "@/lib/cn";
import { useMedia } from "@/lib/useMedia";
import { menuScrollClass, useTabBarClearance } from "@/ui/menuClearance";

/** A finger is the pointer: menus open as a bottom sheet there, as a popover beside their button everywhere else. */
export const TOUCH_QUERY = "(pointer: coarse)";

const shell = "z-50 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl";
const sheet = "menu-sheet mx-auto max-h-[85dvh] w-full max-w-lg overflow-y-auto overscroll-contain rounded-b-none rounded-t-2xl border-x-0 border-b-0 px-2 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]";

/**
 * The content of every dropdown menu. With a mouse it is a popover that keeps clear of the screen edges and the tab bar
 * and scrolls when it is taller than the room left. With a touch screen it is a bottom sheet instead: full width along
 * the bottom edge (capped at the width of a Modal), over a dimmed page, so a tall menu is reached with a thumb and never
 * covers the button that opened it. Radix keeps the menu semantics, focus trap, Escape and the return of focus in both.
 * `className` sizes the popover (a width); the sheet ignores it. The sheet's placement is the `.menu-sheet` rule in index.css, which overrides Radix's popper wrapper.
 */
export function MenuContent({ className, children, collisionPadding, ...rest }: ComponentProps<typeof DropdownMenu.Content>) {
  const touch = useMedia(TOUCH_QUERY);
  // MenuContent mounts only while its menu is open, so this measures exactly then.
  const bottomPad = useTabBarClearance(true);
  // The press that opened the menu has not ended when the sheet appears, often right under the finger, and its click then
  // lands on whatever the sheet put there ("Mute similar", say). Until a press or a key starts inside the sheet, a click
  // is that leftover one and is dropped.
  const settled = useRef(false);
  return (
    <>
      {touch ? <div aria-hidden="true" data-menu-backdrop="" className="pointer-events-auto fixed inset-0 z-40 bg-black/50" /> : null}
      <DropdownMenu.Content
        {...rest}
        collisionPadding={collisionPadding ?? { top: 8, left: 8, right: 8, bottom: bottomPad }}
        data-menu-sheet={touch ? "" : undefined}
        onPointerDownCapture={() => {
          settled.current = true;
        }}
        onKeyDownCapture={() => {
          settled.current = true;
        }}
        onClickCapture={(e) => {
          if (!touch || settled.current) return;
          settled.current = true;
          e.stopPropagation();
          e.preventDefault();
        }}
        className={touch ? cn(shell, sheet) : cn(shell, menuScrollClass, "max-w-[calc(100vw-1rem)]", className)}
      >
        {touch ? <div aria-hidden="true" className="mx-auto mb-1 h-1 w-9 rounded-full bg-line" /> : null}
        {children}
      </DropdownMenu.Content>
    </>
  );
}
