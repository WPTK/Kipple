import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, renderHook, screen } from "@testing-library/react";
import { closeRowMenu, openRowMenu } from "@/gestures/rowMenu";
import { card } from "@/test/mockApi";
import { tabBarClearance, useTabBarClearance } from "@/ui/menuClearance";
import { RowMenu } from "./parts";
import type { RowMenuActions } from "./types";

// #431: on a landscape phone the row menu (the More button and the long press share it) was taller than the screen and its
// top ran off the edge. It caps its height to what Radix says is left and scrolls, like the layout menu.

afterEach(() => {
  act(() => closeRowMenu());
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function tabBarAt(top: number) {
  const nav = document.createElement("nav");
  nav.setAttribute("aria-label", "Primary");
  nav.getBoundingClientRect = () => ({ top }) as DOMRect;
  document.body.append(nav);
}

describe("row menu on a short screen", () => {
  it("scrolls inside the room Radix leaves instead of overflowing, for a long press as for the button", async () => {
    const actions = new Proxy({}, { get: () => vi.fn() }) as unknown as RowMenuActions;
    const item = card(1);
    render(<RowMenu item={item} actions={actions} />);
    act(() => openRowMenu(item.id));
    const menu = await screen.findByRole("menu");
    expect(menu.className).toContain("max-h-[var(--radix-dropdown-menu-content-available-height)]");
    expect(menu.className).toContain("overflow-y-auto");
  });

  it("keeps clear of the tab bar when it runs along the bottom, and of 8 px otherwise", () => {
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(402);
    expect(tabBarClearance()).toBe(8);
    tabBarAt(340);
    expect(tabBarClearance()).toBe(402 - 340 + 8);
    document.body.replaceChildren();
    tabBarAt(0); // the wide layout's sidebar
    expect(tabBarClearance()).toBe(8);
  });

  it("measures only while open", () => {
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(402);
    tabBarAt(340);
    const { result, rerender } = renderHook(({ open }) => useTabBarClearance(open), { initialProps: { open: false } });
    expect(result.current).toBe(8);
    rerender({ open: true });
    expect(result.current).toBe(70);
  });
});
