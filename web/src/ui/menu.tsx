import { type ComponentProps, type MouseEvent, useState } from "react";
import { DropdownMenu } from "radix-ui";
import { cn } from "@/lib/cn";
import { mediaMatches } from "@/lib/useMedia";
import { menuScrollClass, useTabBarClearance } from "@/ui/menuClearance";

/** A finger is the pointer: menus open as a bottom sheet there, as a popover beside their button everywhere else. */
export const TOUCH_QUERY = "(pointer: coarse)";

const shell = "z-50 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl";
const sheet =
  "menu-sheet mx-auto max-h-[85dvh] w-full max-w-lg overflow-y-auto overscroll-contain rounded-b-none rounded-t-2xl border-x-0 border-b-0 pt-2 pr-[max(0.5rem,env(safe-area-inset-right))] pb-[max(0.5rem,env(safe-area-inset-bottom))] pl-[max(0.5rem,env(safe-area-inset-left))]";

type ContentProps = ComponentProps<typeof DropdownMenu.Content>;

/**
 * The content of every dropdown menu. With a mouse it is a popover that keeps clear of the screen edges and the tab bar
 * and scrolls when it is taller than the room left. With a touch screen it is a bottom sheet instead: full width along
 * the bottom edge (capped at the width of a Modal), over a dimmed page, so a tall menu is reached with a thumb and never
 * covers the button that opened it. Radix keeps the menu semantics, focus trap, Escape and the return of focus in both.
 * `className` sizes the popover (a width); the sheet ignores it.
 *
 * The mode is read once, when the menu opens, and kept until it closes: a pointer that appears or goes while the menu is
 * open (a trackpad on a tablet, a keyboard dock) does not swap the layout under the user. Nested menus (DropdownMenu.Sub)
 * are not supported by either mode and ESLint refuses them.
 */
export function MenuContent(props: ContentProps) {
  // MenuContent mounts only while its menu is open, so this reads the pointer exactly then.
  const [touch] = useState(() => mediaMatches(TOUCH_QUERY));
  return touch ? <SheetContent {...props} /> : <PopoverContent {...props} />;
}

function PopoverContent({ className, collisionPadding, ...rest }: ContentProps) {
  const bottomPad = useTabBarClearance(true);
  return <DropdownMenu.Content {...rest} collisionPadding={collisionPadding ?? { top: 8, left: 8, right: 8, bottom: bottomPad }} className={cn(shell, menuScrollClass, "max-w-[calc(100vw-1rem)]", className)} />;
}

/**
 * The sheet's placement is the `.menu-sheet` rule in index.css: Radix places every menu with an inline transform on a
 * wrapper element that no prop reaches, so the wrapper is pinned to the bottom edge there. menu.test.tsx fails when Radix
 * stops wrapping the content, and the touch-menus UAT measures where the sheet really lands.
 */
function SheetContent({ className: _className, onClickCapture, children, ...rest }: ContentProps) {
  const [openedAt] = useState(() => performance.now());
  return (
    <>
      <div aria-hidden="true" data-menu-backdrop="" className="pointer-events-auto fixed inset-0 z-40 bg-black/50" />
      <DropdownMenu.Content
        {...rest}
        data-menu-sheet=""
        onClickCapture={(e) => {
          if (isLeftoverClick(e, openedAt)) {
            e.stopPropagation();
            e.preventDefault();
            return;
          }
          onClickCapture?.(e);
        }}
        className={cn(shell, sheet)}
      >
        {children}
      </DropdownMenu.Content>
    </>
  );
}

/**
 * The tap that opens a sheet. A menu opens on the press, so the sheet is already under the finger when it lifts, and the
 * browser then delivers that tap's click to whatever the sheet put at that spot: Chromium sends the click to the element
 * under the lifted finger, WebKit sends the lift to it and Radix's item turns a lift it never saw start into a click. Either
 * way "Mute similar" (say) is chosen by the tap that only opened the menu. Measured in Chromium and WebKit with real touch
 * events (web/uat/touch-menus.mjs).
 *
 * The leftover click is told apart by what only it has: it follows the lift of a touch whose press began before the sheet
 * existed, within LEFTOVER_MS, and lands on the spot where that press began. A click from assistive technology (TalkBack,
 * Voice Control, Switch Access, el.click()) has no touch before it, or lands elsewhere, or comes later; a trackpad or mouse
 * press-drag-release (Radix clicks the item on release) is not a touch. A press and tap inside the sheet start after it
 * opened, so they are never leftovers.
 */
const LEFTOVER_MS = 400;
const SLOP_PX = 2;
const touchPress = { down: -1, up: -1, x: 0, y: 0 };
if (typeof window !== "undefined") {
  const isTouch = (e: PointerEvent) => e.pointerType === "touch" || e.pointerType === "pen";
  window.addEventListener(
    "pointerdown",
    (e) => {
      touchPress.down = isTouch(e) ? performance.now() : -1;
      touchPress.up = -1;
      touchPress.x = e.clientX;
      touchPress.y = e.clientY;
    },
    true,
  );
  // A key press means the user is no longer tapping: whatever opens or picks next is not the leftover of a tap.
  window.addEventListener(
    "keydown",
    () => {
      touchPress.down = -1;
    },
    true,
  );
  for (const type of ["pointerup", "pointercancel"]) {
    window.addEventListener(type, () => (touchPress.up = touchPress.down >= 0 ? performance.now() : -1), true);
  }
}

function isLeftoverClick(e: MouseEvent<HTMLDivElement>, openedAt: number): boolean {
  const { down, up, x, y } = touchPress;
  if (down < 0 || up < 0 || down > openedAt || performance.now() - up > LEFTOVER_MS) return false;
  const box = (e.target as Element).getBoundingClientRect();
  // The browser hit-tests whole pixels, so a click can land on an item whose edge is a fraction of a pixel off the touch.
  return x >= box.left - SLOP_PX && x <= box.right + SLOP_PX && y >= box.top - SLOP_PX && y <= box.bottom + SLOP_PX;
}
