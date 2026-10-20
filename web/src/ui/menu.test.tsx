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

function Demo({ onPick = () => {} }: { onPick?: () => void }) {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button type="button" aria-label="List options">
          open
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <MenuContent align="start" className="w-72">
          <DropdownMenu.Item onSelect={onPick}>First</DropdownMenu.Item>
          <DropdownMenu.Item>Second</DropdownMenu.Item>
        </MenuContent>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
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
    // The decorative grabber is hidden from assistive technology; the items are the menu's only content.
    expect(menu.querySelectorAll("[aria-hidden='true']").length).toBe(isTouch ? 1 : 0);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });

  it("closes on a tap on the dimmed page and picks with a tap", async () => {
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

  it("drops the click left over from the press that opened the sheet, and takes every click that starts inside it", async () => {
    touch(true);
    const user = userEvent.setup();
    const pick = vi.fn();
    render(<Demo onPick={pick} />);
    await user.click(screen.getByRole("button", { name: "List options" }));
    const first = await screen.findByRole("menuitem", { name: "First" });
    // A finger lifting after the sheet appeared under it sends a click with no press of its own inside the sheet.
    fireEvent.click(first);
    expect(pick).not.toHaveBeenCalled();
    expect(screen.queryByRole("menu")).not.toBeNull();
    // A press inside the sheet, then its click: a real tap.
    fireEvent.pointerDown(first);
    fireEvent.click(first);
    expect(pick).toHaveBeenCalledTimes(1);
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
