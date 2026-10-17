import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { BOTTOM_BAR_VAR, BottomBar } from "@/ui/BottomBar";
import { Toasts } from "./toasts";

const root = document.documentElement;
const published = () => root.style.getPropertyValue(BOTTOM_BAR_VAR);

let observers: { cb: () => void; target: Element | null; opts: ResizeObserverOptions | undefined; disconnected: boolean }[] = [];
let barHeight = 52;
let wide = false;

beforeEach(() => {
  observers = [];
  barHeight = 52;
  vi.stubGlobal(
    "ResizeObserver",
    class {
      rec = { cb: () => {}, target: null as Element | null, opts: undefined as ResizeObserverOptions | undefined, disconnected: false };
      constructor(cb: () => void) {
        this.rec.cb = cb;
        observers.push(this.rec);
      }
      observe(el: Element, opts?: ResizeObserverOptions) {
        this.rec.target = el;
        this.rec.opts = opts;
      }
      disconnect() {
        this.rec.disconnected = true;
      }
    },
  );
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(() => ({ height: barHeight }) as DOMRect);
  vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({ matches: wide, media: query, addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  root.style.removeProperty(BOTTOM_BAR_VAR);
  wide = false;
});

describe("toasts above a screen's bottom bar", () => {
  it("stand above the bar's published height and the tab bar, with the device inset counted once", () => {
    const { getByTestId } = render(<Toasts inset="tabbar" />);
    const bottom = getByTestId("toast-region").style.bottom;
    expect(bottom).toContain("var(--bottom-bar-h, 0px)");
    expect(bottom).toContain("var(--tabbar-h)");
    expect(bottom.match(/safe-area-inset-bottom/g)).toHaveLength(1);
  });

  it("publishes the bar's height before the first frame and clears it when the bar goes", () => {
    const { unmount } = render(<BottomBar ariaLabel="Selected feeds">x</BottomBar>);
    expect(published()).toBe("52px");
    unmount();
    expect(published()).toBe("");
  });

  it("follows the bar when it changes height, and measures the border box", () => {
    render(<BottomBar ariaLabel="Selected feeds">x</BottomBar>);
    expect(observers).toHaveLength(1);
    expect(observers[0]!.opts).toEqual({ box: "border-box" });
    barHeight = 96; // the buttons wrap onto a second row
    act(() => observers[0]!.cb());
    expect(published()).toBe("96px");
  });

  it("on a phone the tab bar pays the device inset, so the bar does not", () => {
    const { container, getByRole } = render(<BottomBar ariaLabel="Selected feeds">x</BottomBar>);
    expect(getByRole("region").className).not.toContain("pb-safe");
    expect(container.querySelector(".pb-safe")).toBeNull();
  });

  it("with no tab bar the bar pays the inset in a spacer outside the measured region, so the toasts count it once", () => {
    wide = true;
    const { container, getByRole } = render(<BottomBar ariaLabel="Selected feeds">x</BottomBar>);
    const region = getByRole("region");
    expect(region.className).not.toContain("pb-safe");
    expect(region.querySelector(".pb-safe")).toBeNull();
    expect(container.querySelectorAll(".pb-safe")).toHaveLength(1);
    expect(observers[0]!.target).toBe(region);
  });
});
