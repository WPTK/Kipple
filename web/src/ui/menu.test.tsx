import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Toasts } from "@/shell/toasts";
import { DeviceSaveStatus } from "@/shell/SaveStatus";
import { DropdownMenu } from "./menu";

// #423: a tall menu is a popover beside its button with a mouse and a bottom sheet with a finger. One primitive does both
// for every dropdown menu, with Radix's menu semantics, focus trap, Escape and return of focus either way.

function touch(on: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: on && q === "(pointer: coarse)", media: q, addEventListener() {}, removeEventListener() {} }));
}

afterEach(() => vi.unstubAllGlobals());

function pointer(type: string, el: Element, pointerType: "touch" | "mouse", { id = 1, x = 100, y = 700 } = {}) {
  // jsdom has no PointerEvent: a MouseEvent of the same type carries the button and coordinates Radix reads.
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y });
  Object.defineProperty(e, "pointerType", { value: pointerType });
  Object.defineProperty(e, "pointerId", { value: id });
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
        <DropdownMenu.Content align="start" popoverWidth="w-72" onClickCapture={onClickCapture}>
          <DropdownMenu.Item onSelect={onPick}>First</DropdownMenu.Item>
          <DropdownMenu.Item onSelect={onPickSecond}>Second</DropdownMenu.Item>
        </DropdownMenu.Content>
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
  it("is not modal: the page stays visible to assistive technology and the menu sits in the main landmark", async () => {
    touch(false);
    render(
      <>
        <header>
          <button type="button">Skip</button>
        </header>
        <main id="main">
          <Demo />
        </main>
      </>,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "List options" }));
    const menu = await screen.findByRole("menu");
    expect(menu.closest("#main")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Skip" }).closest("[aria-hidden='true']")).toBeNull();
    expect(screen.getByRole("main").closest("[aria-hidden='true']")).toBeNull();
  });

  it("is drawn above the toasts and the save notice, so neither covers a sheet or takes its taps", async () => {
    touch(true);
    const z = (el: Element) => Number(/(?:^|\s)z-(\d+)(?:\s|$)/.exec(el.className)![1]);
    const { container } = render(
      <>
        <main id="main">
          <Demo />
        </main>
        <Toasts />
      </>,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "List options" }));
    const menu = await screen.findByRole("menu");
    const toasts = container.querySelector("[data-testid=toast-region]")!;
    expect(z(menu)).toBeGreaterThan(z(toasts));
    expect(z(document.querySelector("[data-menu-backdrop]")!)).toBeGreaterThan(z(toasts));
    // The save notice shares the toasts' layer.
    expect(DeviceSaveStatus.toString()).toContain("z-50");
  });

  it("stays open when focus moves to the page under a sheet (the mouse events after a long press's lift focus it)", async () => {
    touch(true);
    render(
      <>
        <button type="button">Under</button>
        <Demo />
      </>,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "List options" }));
    await screen.findByRole("menu");
    act(() => screen.getByRole("button", { name: "Under" }).focus());
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("menu")).not.toBeNull();
  });

  it("closes when focus leaves it after the opening press is over (a screen reader moving to the page)", async () => {
    touch(true);
    render(
      <>
        <button type="button">Under</button>
        <Demo />
      </>,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "List options" }));
    await screen.findByRole("menu");
    await new Promise((r) => setTimeout(r, 450));
    act(() => screen.getByRole("button", { name: "Under" }).focus());
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
  });

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
    expect(menu.className).toContain("max-h-[70dvh]");
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

  it("picks with a tap and closes on a tap on the dimmed page", async () => {
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
    // jsdom has no stylesheet, so user-event would see the body's pointer-events: none
    const backdrop = document.querySelector("[data-menu-backdrop]")!;
    pointer("pointerdown", backdrop, "touch", { id: 2 });
    fireEvent.click(backdrop);
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
    at(first, { left: 50, right: 150, top: 600, bottom: 640 }); // dragged up from the press at 700 to the item
    pointer("pointerup", first, "mouse", { y: 620 }); // not captured: the release lands on the item and Radix clicks it
    first.click();
    expect(pick).toHaveBeenCalledTimes(1);
  });

  it("drops the click a mouse makes when its button comes up where it went down, over the sheet", async () => {
    touch(true);
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    const { first } = await openWith("mouse");
    pointer("pointerup", first, "mouse"); // measured in a touch-screen Chromium: Radix clicks the item on the release
    first.click();
    expect(pick).not.toHaveBeenCalled();
    expect(screen.queryByRole("menu")).not.toBeNull();
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

  it("closes on a tap on the dimmed page, which stays until the tap's click so nothing under it gets the tap", async () => {
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
    pointer("pointerdown", backdrop, "touch", { id: 2 });
    pointer("pointerup", backdrop, "touch", { id: 2 });
    // Radix closes a menu on the press outside it; the dimmed page would then be gone before the lift and its click,
    // which the browser would hit-test against the page.
    expect(screen.queryByRole("menu")).not.toBeNull();
    expect(document.querySelector("[data-menu-backdrop]")).toBe(backdrop);
    fireEvent.click(backdrop);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(under).not.toHaveBeenCalled();
  });

  it("gives focus back to the button after a tap on the dimmed page", async () => {
    touch(true);
    render(<Demo />);
    const { trigger } = await openWith("touch");
    const backdrop = document.querySelector("[data-menu-backdrop]")!;
    pointer("pointerdown", backdrop, "touch", { id: 2 });
    pointer("pointerup", backdrop, "touch", { id: 2 });
    fireEvent.click(backdrop);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("has a dimmed page that takes no touch scroll", async () => {
    touch(true);
    render(<Demo />);
    await openWith("touch");
    const cls = document.querySelector("[data-menu-backdrop]")!.className;
    expect(cls).toContain("touch-none");
    expect(cls).toContain("overscroll-none");
  });

  it("stays open when the click of the tap that opened it lands on the dimmed page", async () => {
    touch(true);
    render(<Demo />);
    const { trigger } = await openWith("touch");
    pointer("pointerup", trigger, "touch");
    const backdrop = document.querySelector("[data-menu-backdrop]")!;
    at(backdrop, { left: 0, right: 375, top: 0, bottom: 812 });
    fireEvent.click(backdrop);
    expect(screen.queryByRole("menu")).not.toBeNull();
  });

  it("stays open when a mouse opens it and its button comes up over the dimmed page", async () => {
    touch(true);
    render(<Demo />);
    const { trigger } = await openWith("mouse");
    pointer("pointerup", trigger, "mouse");
    fireEvent.click(document.querySelector("[data-menu-backdrop]")!);
    expect(screen.queryByRole("menu")).not.toBeNull();
  });

  it("closes a menu whose open state the caller holds through the caller's onOpenChange", async () => {
    touch(true);
    const changed = vi.fn();
    function Held() {
      const [open, setOpen] = useState(false);
      return (
        <DropdownMenu.Root
          open={open}
          onOpenChange={(o) => {
            changed(o);
            setOpen(o);
          }}
        >
          <DropdownMenu.Trigger asChild>
            <button type="button">More</button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content>
              <DropdownMenu.Item>First</DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
      );
    }
    const user = userEvent.setup();
    render(<Held />);
    await user.click(screen.getByRole("button", { name: "More" }));
    await screen.findByRole("menu");
    const backdrop = document.querySelector("[data-menu-backdrop]")!;
    pointer("pointerdown", backdrop, "touch", { id: 2 });
    fireEvent.click(backdrop);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(changed).toHaveBeenLastCalledWith(false);
  });

  it("still drops the leftover click when a second finger lands while the first is down", async () => {
    touch(true);
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    const { trigger, first } = await openWith("touch");
    const backdrop = document.querySelector("[data-menu-backdrop]")!;
    pointer("pointerdown", backdrop, "touch", { id: 2, x: 300, y: 20 });
    pointer("pointerup", backdrop, "touch", { id: 2, x: 300, y: 20 });
    pointer("pointerup", trigger, "touch");
    fireEvent.click(first);
    expect(pick).not.toHaveBeenCalled();
    expect(screen.queryByRole("menu")).not.toBeNull();
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
