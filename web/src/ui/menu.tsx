import { type ComponentProps, createContext, type MouseEvent, useContext, useRef, useState } from "react";
import { DropdownMenu as Radix } from "radix-ui";
import { cn } from "@/lib/cn";
import { mediaMatches } from "@/lib/useMedia";
import { useMain } from "@/ui/useMain";
import { menuScrollClass, useTabBarClearance } from "@/ui/menuClearance";

/** A finger is the pointer: menus open as a bottom sheet there, as a popover beside their button everywhere else. */
export const TOUCH_QUERY = "(pointer: coarse)";

// z-60 and z-55 sit above the toasts, the save notice and dialogs (z-50): an open menu is never covered by one, so a toast
// cannot take the taps meant for the bottom of a sheet.
const shell = "z-60 rounded-xl border border-line bg-bg p-1 text-fg shadow-sheet";
const sheet =
  "menu-sheet mx-auto max-h-[70dvh] w-full max-w-lg overflow-y-auto overscroll-contain rounded-b-none rounded-t-2xl border-x-0 border-b-0 pt-2 pr-[max(0.5rem,env(safe-area-inset-right))] pb-[max(0.5rem,env(safe-area-inset-bottom))] pl-[max(0.5rem,env(safe-area-inset-left))]";

/** The row of a menu item (a command, a choice): 44 px tall, with the highlight the keyboard and pointer move. Every item below wears it. */
const menuItemClass = "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[disabled]:opacity-50 data-[highlighted]:bg-selection";

/** Closes the menu of the Root around it. */
const CloseMenu = createContext<() => void>(() => {});

/**
 * Radix's Root, with the open state where the sheet's dimmed page can reach it (a tap on it closes the menu). Menus are
 * not modal: a modal menu marks the rest of the page aria-hidden while its Skip link and list pane can still be
 * focused, and leaves the page with no main landmark. Escape, a press outside and the arrow keys work the same; the
 * keyboard cannot leave the menu either way, since Radix keeps Tab inside it.
 */
function Root({ open, defaultOpen = false, onOpenChange, ...rest }: ComponentProps<typeof Radix.Root>) {
  const [own, setOwn] = useState(defaultOpen);
  const change = (o: boolean) => {
    setOwn(o);
    onOpenChange?.(o);
  };
  return (
    <CloseMenu.Provider value={() => change(false)}>
      <Radix.Root {...rest} modal={false} open={open ?? own} onOpenChange={change} />
    </CloseMenu.Provider>
  );
}

type ContentProps = Omit<ComponentProps<typeof Radix.Content>, "className"> & {
  /** The popover's width (a Tailwind width class); the sheet is always the screen's width. */
  popoverWidth?: string;
};

/**
 * The content of every dropdown menu. With a mouse it is a popover that keeps clear of the screen edges and the tab bar
 * and scrolls when it is taller than the room left. With a touch screen it is a bottom sheet instead: full width along
 * the bottom edge (capped at the width of a Modal), over a dimmed page, so a tall menu is reached with a thumb and never
 * covers the button that opened it. Radix keeps the menu semantics, focus trap, Escape and the return of focus in both.
 *
 * The mode is read once, when the menu opens, and kept until it closes: a pointer that appears or goes while the menu is
 * open (a trackpad on a tablet, a keyboard dock) does not swap the layout under the user.
 */
function MenuContent(props: ContentProps) {
  // MenuContent mounts only while its menu is open, so this reads the pointer exactly then.
  const [touch] = useState(() => mediaMatches(TOUCH_QUERY));
  return touch ? <SheetContent {...props} /> : <PopoverContent {...props} />;
}

function PopoverContent({ popoverWidth, collisionPadding, ...rest }: ContentProps) {
  const bottomPad = useTabBarClearance(true);
  return <Radix.Content {...rest} collisionPadding={collisionPadding ?? { top: 8, left: 8, right: 8, bottom: bottomPad }} className={cn(shell, menuScrollClass, "max-w-[calc(100vw-1rem)]", popoverWidth)} />;
}

/**
 * The sheet's placement is the `.menu-sheet` rule in index.css: Radix places every menu with an inline transform on a
 * wrapper element that no prop reaches, so the wrapper is pinned to the bottom edge there. menu.test.tsx fails when Radix
 * stops wrapping the content, and the touch-menus UAT measures where the sheet really lands in every engine it runs in.
 *
 * The dimmed page closes the menu on its click, not on the press: Radix closes a menu on the press outside it, which
 * took the dimmed page away (and the menu's block on the page's pointer events) while the finger was still down, so the
 * rest of that tap (the lift and its click) landed on whatever lay under it, an article row that then opened. Measured
 * in Chromium and WebKit with real touch input. Kept until its own click, the dimmed page takes the whole tap.
 */
function SheetContent({ popoverWidth: _width, onClickCapture, onPointerDownOutside, onFocusOutside, children, ...rest }: ContentProps) {
  const close = useContext(CloseMenu);
  const [opener] = useState(() => ({ at: performance.now(), id: lastPress }));
  const pressed = useRef(false);
  return (
    <>
      <div
        aria-hidden="true"
        data-menu-backdrop=""
        // touch-none and overscroll-none: a drag that starts on the dimmed page must not scroll the page behind it (the
        // row's menu is not modal and locks no scroll of its own).
        className="pointer-events-auto fixed inset-0 z-55 touch-none overscroll-none bg-scrim"
        // Only a press that began on it: the click of the press that opened the menu (a tap, or a mouse whose button
        // comes up over the dimmed page) also lands here. A click handler is also what makes iOS deliver a tap's click
        // to an element that is not a link or a button.
        onPointerDown={() => (pressed.current = true)}
        onClick={() => {
          if (pressed.current) close();
        }}
      />
      <Radix.Content
        {...rest}
        data-menu-sheet=""
        // A non-modal menu closes when focus leaves it, and a long press opens a row menu with no focus anywhere: the
        // mouse events a browser sends after the lift (mousedown on the story's link) move focus to the page under the
        // sheet and closed the menu the moment the finger came up. That holds only while the opening press is down or
        // just lifted; after it, focus leaving the sheet (a screen reader moving on to the page) closes it like any
        // other non-modal menu, so the sheet and its dimmed page never stay over a page the user has moved to.
        onFocusOutside={(e) => {
          onFocusOutside?.(e);
          if (isOpeningPress(opener)) e.preventDefault();
        }}
        onPointerDownOutside={(e) => {
          onPointerDownOutside?.(e);
          if ((e.detail.originalEvent.target as Element | null)?.closest?.("[data-menu-backdrop]")) e.preventDefault();
        }}
        onClickCapture={(e) => {
          if (isLeftoverClick(e, opener)) {
            e.stopPropagation();
            e.preventDefault();
            return;
          }
          onClickCapture?.(e);
        }}
        className={cn(shell, sheet)}
      >
        {children}
      </Radix.Content>
    </>
  );
}

/**
 * Where menus are drawn: inside the page's main landmark, not loose in the body, so every menu is in a landmark (axe
 * "region"). Its position is fixed, so it is placed the same wherever it sits in the page.
 */
function Portal(props: ComponentProps<typeof Radix.Portal>) {
  return <Radix.Portal container={useMain()} {...props} />;
}

/** The one menu row: a command and a radio choice share the look; `className` adds to it (an indent for the indicator). */
function Item({ className, ...rest }: ComponentProps<typeof Radix.Item>) {
  return <Radix.Item {...rest} className={cn(menuItemClass, className)} />;
}
function RadioItem({ className, ...rest }: ComponentProps<typeof Radix.RadioItem>) {
  return <Radix.RadioItem {...rest} className={cn(menuItemClass, className)} />;
}

/**
 * The menu parts. Nested menus (Sub) are left out on purpose: neither the popover nor the bottom sheet is built for one,
 * and ESLint keeps every other module from importing Radix's DropdownMenu directly.
 */
export const DropdownMenu = {
  Root,
  Trigger: Radix.Trigger,
  Portal,
  Content: MenuContent,
  Item,
  CheckboxItem: Radix.CheckboxItem,
  RadioGroup: Radix.RadioGroup,
  RadioItem,
  ItemIndicator: Radix.ItemIndicator,
  Label: Radix.Label,
  Separator: Radix.Separator,
};

/**
 * The press that opens a sheet. A menu opens on the press, so the sheet is already under the finger (or the mouse) when
 * it lifts, and the browser then delivers that press's click to whatever the sheet put at that spot: Chromium sends a
 * tap's click to the element under the lifted finger, WebKit sends the lift to it, and Radix's item turns a lift or a
 * mouse button's release it never saw start into a click. Either way "Mute similar" (say) is chosen by the press that
 * only opened the menu. Measured in Chromium and WebKit with real touch events and with a mouse in a touch-screen
 * browser (web/uat/touch-menus.mjs).
 *
 * The leftover click is told apart by what only it has: it follows the release of the press that opened the sheet,
 * within LEFTOVER_MS, and lands on the spot where that press began. A click from assistive technology (TalkBack, Voice
 * Control, Switch Access, el.click()) has no press before it, or lands elsewhere, or comes later; a trackpad or mouse
 * press-drag-release (Radix clicks the item on release) lands on the item it was dragged to, away from where it began.
 * A press and tap inside the sheet start after it opened, so they are never leftovers. Each pointer's press is kept
 * apart, so a second finger cannot stand in for the one that opened the menu.
 */
const LEFTOVER_MS = 400;
const SLOP_PX = 2;
type Press = { down: number; up: number; x: number; y: number };
const presses = new Map<number, Press>();
/** The pointer of the latest press, or -1 after a key press. */
let lastPress = -1;
if (typeof window !== "undefined") {
  window.addEventListener(
    "pointerdown",
    (e) => {
      const now = performance.now();
      for (const [id, p] of presses) if (p.up >= 0 && now - p.up > LEFTOVER_MS) presses.delete(id);
      presses.set(e.pointerId, { down: now, up: -1, x: e.clientX, y: e.clientY });
      lastPress = e.pointerId;
    },
    true,
  );
  // A key press means the user is no longer pressing: whatever opens or picks next is not the leftover of a press.
  window.addEventListener(
    "keydown",
    () => {
      presses.clear();
      lastPress = -1;
    },
    true,
  );
  for (const type of ["pointerup", "pointercancel"]) {
    window.addEventListener(
      type,
      (e) => {
        const p = presses.get((e as PointerEvent).pointerId);
        if (p) p.up = performance.now();
      },
      true,
    );
  }
}

/** The press that opened the sheet is still down, or lifted less than LEFTOVER_MS ago (never for a key press). */
function isOpeningPress(opener: { at: number; id: number }): boolean {
  const p = presses.get(opener.id);
  return !!p && p.down <= opener.at && (p.up < 0 || performance.now() - p.up <= LEFTOVER_MS);
}

function isLeftoverClick(e: MouseEvent<HTMLElement>, opener: { at: number; id: number }): boolean {
  const p = presses.get(opener.id);
  if (!p || p.up < 0 || p.down > opener.at || performance.now() - p.up > LEFTOVER_MS) return false;
  const box = (e.target as Element).getBoundingClientRect();
  // The browser hit-tests whole pixels, so a click can land on an item whose edge is a fraction of a pixel off the press.
  return p.x >= box.left - SLOP_PX && p.x <= box.right + SLOP_PX && p.y >= box.top - SLOP_PX && p.y <= box.bottom + SLOP_PX;
}
