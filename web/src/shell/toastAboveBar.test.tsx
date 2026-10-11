import { afterEach, describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { BOTTOM_BAR_VAR, BottomBar } from "@/ui/BottomBar";
import { Toasts } from "./toasts";

afterEach(() => document.documentElement.style.removeProperty(BOTTOM_BAR_VAR));

describe("toasts above a screen's bottom bar", () => {
  it("stand above the bar's published height, on top of the tab bar", () => {
    const { getByTestId } = render(<Toasts inset="tabbar" />);
    expect(getByTestId("toast-region").style.bottom).toContain("var(--bottom-bar-h, 0px)");
    expect(getByTestId("toast-region").style.bottom).toContain("var(--tabbar-h)");
  });

  it("a bar publishes its height while it is on screen and clears it when it goes", () => {
    const { unmount } = render(<BottomBar label="Selected feeds">x</BottomBar>);
    expect(document.documentElement.style.getPropertyValue(BOTTOM_BAR_VAR)).toMatch(/^\d+(\.\d+)?px$/);
    unmount();
    expect(document.documentElement.style.getPropertyValue(BOTTOM_BAR_VAR)).toBe("");
  });
});
