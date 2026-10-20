import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DropdownMenu } from "radix-ui";
import { MenuContent } from "./menu";

// #423: a tall menu is a popover beside its button with a mouse and a bottom sheet with a finger. One primitive does both
// for every dropdown menu, with Radix's menu semantics, focus trap, Escape and return of focus either way.

function touch(on: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: on && q === "(pointer: coarse)", media: q, addEventListener() {}, removeEventListener() {} }));
}

afterEach(() => vi.unstubAllGlobals());

function pointer(type: string, el: Element, pointerType: "touch" | "mouse") {
  // jsdom has no PointerEvent: a MouseEvent of the same type carries the button and coordinates Radix reads.
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: 100, clientY: 700 });
  Object.defineProperty(e, "pointerType", { value: pointerType });
  fireEvent(el, e);
}

function Demo({ onPick = () => {}, onPickSecond = () => {}, onClickCapture }: { onPick?: () => void; onPickSecond?: () => void; onClickCapture?: () => void }) {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button type="button" aria-label="List options">
          open
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <MenuContent align="start" className="w-72" onClickCapture={onClickCapture}>
          <DropdownMenu.Item onSelect={onPick}>First</DropdownMenu.Item>
          <DropdownMenu.Item onSelect={onPickSecond}>Second</DropdownMenu.Item>
        </MenuContent>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

// The event sequences below are the ones measured in Chromium and WebKit with real touch input (web/uat/touch-menus.mjs):
// the menu opens on the press, so the sheet is already under the finger when it lifts.
function at(el: Element, box = { left: 50, right: 150, top: 680, bottom: 720 }) {
  el.getBoundingClientRect = () => ({ ...box, x: box.left, y: box.top, width: box.right - box.left, height: box.bottom - box.top, toJSON() {} });
}

async function openWith(type: "touch" | "mouse") {
  const trigger = screen.getByRole("button", { name: "List options" });
  pointer("pointerdown", trigger, type);
  const first = await screen.findByRole("menuitem", { name: "First" });
  at(first);
  return { trigger, first };
}

describe("MenuContent", () => {
  it("is a popover with a mouse: sized by the caller, scrolling, no dimmed page", async () => {
    touch(false);
    const user = userEvent.setup();
    render(<Demo />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    const menu = await screen.findByRole("menu");
    expect(menu.className).toContain("w-72");
    expect(menu.className).toContain("max-h-[var(--radix-dropdown-menu-content-available-height)]");
    expect(menu.hasAttribute("data-menu-sheet")).toBe(false);
    expect(document.querySelector("[data-menu-backdrop]")).toBeNull();
  });

  it("is a bottom sheet with a finger: full width, capped height, dimmed page, the caller's width ignored", async () => {
    touch(true);
    const user = userEvent.setup();
    render(<Demo />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    const menu = await screen.findByRole("menu");
    expect(menu.hasAttribute("data-menu-sheet")).toBe(true);
    expect(menu.className).toContain("w-full");
    expect(menu.className).toContain("max-h-[85dvh]");
    expect(menu.className).toContain("overflow-y-auto");
    expect(menu.className).toContain("rounded-t-2xl");
    expect(menu.className).toContain("pl-[max(0.5rem,env(safe-area-inset-left))]");
    expect(menu.className).toContain("pr-[max(0.5rem,env(safe-area-inset-right))]");
    expect(menu.className).not.toContain("w-72");
    expect(document.querySelector("[data-menu-backdrop]")?.getAttribute("aria-hidden")).toBe("true");
  });

  it.each([false, true])("keeps the menu role, its name, Escape and the return of focus (touch %s)", async (isTouch) => {
    touch(isTouch);
    const user = userEvent.setup();
    render(<Demo />);
    const trigger = screen.getByRole("button", { name: "List options" });
    await user.click(trigger);
    const menu = await screen.findByRole("menu", { name: "List options" });
    expect(screen.getAllByRole("menuitem")).toHaveLength(2);
    expect(menu.querySelectorAll("[aria-hidden='true']").length).toBe(0); // the items are the menu's only content
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });

  it("picks with a tap and closes on a press on the dimmed page", async () => {
    touch(true);
    const user = userEvent.setup();
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    await user.click(await screen.findByRole("menuitem", { name: "First" }));
    expect(pick).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    await user.click(screen.getByRole("button", { name: "List options" }));
    await screen.findByRole("menu");
    fireEvent.pointerDown(document.querySelector("[data-menu-backdrop]")!); // jsdom has no stylesheet, so user-event would see the body's pointer-events: none
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
  });

  it("drops the click Chromium sends to the sheet after the tap that opened it", async () => {
    touch(true);
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    const { trigger, first } = await openWith("touch");
    pointer("pointerup", trigger, "touch"); // the touch stays captured by the button it began on
    fireEvent.click(first); // the click goes to the element under the lifted finger
    expect(pick).not.toHaveBeenCalled();
    expect(screen.queryByRole("menu")).not.toBeNull();
  });

  it("drops the click WebKit makes when the lift lands on the sheet", async () => {
    touch(true);
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    const { first } = await openWith("touch");
    pointer("pointerup", first, "touch");
    first.click(); // Radix clicks an item whose press it never saw
    expect(pick).not.toHaveBeenCalled();
    expect(screen.queryByRole("menu")).not.toBeNull();
  });

  it("takes a tap inside the sheet right after, however soon: its press starts inside", async () => {
    touch(true);
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    const { trigger, first } = await openWith("touch");
    pointer("pointerup", trigger, "touch");
    fireEvent.click(first); // the leftover, dropped
    pointer("pointerdown", first, "touch");
    pointer("pointerup", first, "touch");
    fireEvent.click(first);
    expect(pick).toHaveBeenCalledTimes(1);
  });

  it("takes a bare click from assistive technology (TalkBack, Voice Control, Switch Access, el.click())", async () => {
    touch(true);
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    const user = userEvent.setup();
    await user.keyboard("{Tab}{Enter}"); // opened without a press
    const first = await screen.findByRole("menuitem", { name: "First" });
    first.click();
    expect(pick).toHaveBeenCalledTimes(1);
  });

  it("takes a bare click on another item just after a touch opened the menu", async () => {
    touch(true);
    const pick = vi.fn();
    const second = vi.fn();
    render(<Demo onPick={pick} onPickSecond={second} />);
    const { trigger } = await openWith("touch");
    pointer("pointerup", trigger, "touch");
    const other = screen.getByRole("menuitem", { name: "Second" });
    at(other, { left: 0, right: 40, top: 0, bottom: 20 }); // not under the spot the press began
    other.click();
    expect(second).toHaveBeenCalledTimes(1);
    expect(pick).not.toHaveBeenCalled();
  });

  it("takes a click on the spot the tap began once the window has passed", async () => {
    touch(true);
    const pick = vi.fn();
    const now = vi.spyOn(performance, "now");
    now.mockReturnValue(1000);
    render(<Demo onPick={pick} />);
    const { trigger, first } = await openWith("touch");
    pointer("pointerup", trigger, "touch");
    now.mockReturnValue(1000 + 401);
    first.click();
    expect(pick).toHaveBeenCalledTimes(1);
    now.mockRestore();
  });

  it("takes the release of a trackpad or mouse press-drag-release on a coarse-pointer tablet", async () => {
    touch(true);
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    const { first } = await openWith("mouse");
    pointer("pointerup", first, "mouse"); // not captured: the release lands on the item and Radix clicks it
    first.click();
    expect(pick).toHaveBeenCalledTimes(1);
  });

  it("keeps a caller's own capture handlers", async () => {
    touch(true);
    const user = userEvent.setup();
    const seen = vi.fn();
    render(<Demo onClickCapture={seen} />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    await user.click(await screen.findByRole("menuitem", { name: "Second" }));
    expect(seen).toHaveBeenCalled();
  });

  it("keeps the layout it opened with when the pointer changes while it is open", async () => {
    touch(true);
    const user = userEvent.setup();
    const { rerender } = render(<Demo />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    expect((await screen.findByRole("menu")).hasAttribute("data-menu-sheet")).toBe(true);
    touch(false);
    rerender(<Demo />);
    expect(screen.getByRole("menu").hasAttribute("data-menu-sheet")).toBe(true);
    expect(document.querySelector("[data-menu-backdrop]")).not.toBeNull();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    await user.click(screen.getByRole("button", { name: "List options" }));
    expect((await screen.findByRole("menu")).hasAttribute("data-menu-sheet")).toBe(false);
  });

  it("closes on a touch press on the dimmed page, and the click after it reaches nothing", async () => {
    touch(true);
    const under = vi.fn();
    render(
      <>
        <button type="button" onClick={under}>
          page
        </button>
        <Demo />
      </>,
    );
    await openWith("touch");
    const backdrop = document.querySelector("[data-menu-backdrop]")!;
    pointer("pointerdown", backdrop, "touch");
    pointer("pointerup", backdrop, "touch");
    fireEvent.click(backdrop);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(under).not.toHaveBeenCalled();
  });

  it("wraps the sheet in the Radix popper element that index.css pins to the bottom edge", async () => {
    touch(true);
    const user = userEvent.setup();
    render(<Demo />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    const menu = await screen.findByRole("menu");
    expect(menu.parentElement?.hasAttribute("data-radix-popper-content-wrapper")).toBe(true);
    expect(menu.hasAttribute("data-menu-sheet")).toBe(true);
  });

  it("takes a key press on an item at once: Enter has no press before its click", async () => {
    touch(true);
    const user = userEvent.setup();
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    await screen.findByRole("menu");
    await user.keyboard("{ArrowDown}{Enter}");
    expect(pick).toHaveBeenCalledTimes(1);
  });

  it("does not drop a click in the popover", async () => {
    touch(false);
    const user = userEvent.setup();
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "First" }));
    expect(pick).toHaveBeenCalledTimes(1);
  });
});
