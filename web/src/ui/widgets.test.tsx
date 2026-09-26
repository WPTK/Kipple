import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DEFAULT_DEVICE_PREFS, parseDevicePrefs, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { Segmented } from "./segmented";
import { ResizeHandle, dragWidth, keyWidth } from "./ResizeHandle";
import { UnreadCount, badgeKind, badgeText } from "./UnreadCount";

beforeEach(() => {
  localStorage.clear();
  resetDevicePrefs();
});
afterEach(() => vi.restoreAllMocks());

describe("Segmented", () => {
  it("draws the chosen option as pressed and never lets a focused radio show", async () => {
    const onChange = vi.fn();
    render(<Segmented legend="Text size" value={1} options={[{ value: 0.875, label: "Smaller" }, { value: 1, label: "Default" }]} onChange={onChange} />);
    const on = screen.getByRole("radio", { name: "Default" });
    expect(on.closest("label")).toHaveAttribute("data-pressed", "true");
    expect(screen.getByRole("radio", { name: "Smaller" }).closest("label")).toHaveAttribute("data-pressed", "false");
    // The input stays the visually hidden kind (sr-only-live); only an anchor may appear on focus (index.css).
    expect(on.className).toContain("sr-only-live");
    await userEvent.setup().click(screen.getByText("Smaller"));
    expect(onChange).toHaveBeenCalledWith(0.875);
  });

  it("holds the control where it was on screen when choosing it reflows the page above (text size)", async () => {
    let top = 300;
    function Host() {
      const [v, setV] = useState(1);
      return (
        <div data-testid="scroller" style={{ overflowY: "auto" }}>
          <Segmented
            legend="Text size"
            value={v}
            options={[{ value: 1, label: "Default" }, { value: 1.25, label: "Larger" }]}
            onChange={(n) => {
              top = 340; // everything above grew, so the row is now 40 px lower
              setV(n);
            }}
          />
        </div>
      );
    }
    render(<Host />);
    const scroller = screen.getByTestId("scroller");
    Object.defineProperty(scroller, "scrollHeight", { value: 2000, configurable: true });
    Object.defineProperty(scroller, "clientHeight", { value: 500, configurable: true });
    scroller.scrollTop = 100;
    const row = screen.getByRole("radio", { name: "Default" }).closest("div") as HTMLElement;
    vi.spyOn(row, "getBoundingClientRect").mockImplementation(() => ({ top }) as DOMRect);
    await userEvent.setup().click(screen.getByText("Larger"));
    // The option row moved 40 px down (top 300 -> 340) and the scroll parent made up for it.
    expect(scroller.scrollTop).toBe(140);
  });
});

describe("ResizeHandle", () => {
  it("clamps a drag and maps the arrow keys, Shift, Home and End", () => {
    expect(dragWidth(300, 50, 200, 400)).toBe(350);
    expect(dragWidth(300, 500, 200, 400)).toBe(400);
    expect(dragWidth(300, -500, 200, 400)).toBe(200);
    expect(keyWidth("ArrowRight", false, 300, 200, 400)).toBe(316);
    expect(keyWidth("ArrowLeft", true, 300, 200, 400)).toBe(252);
    expect(keyWidth("ArrowRight", true, 390, 200, 400)).toBe(400);
    expect(keyWidth("Home", false, 300, 200, 400)).toBe(200);
    expect(keyWidth("End", false, 300, 200, 400)).toBe(400);
    expect(keyWidth("a", false, 300, 200, 400)).toBeNull();
  });

  it("is a separator with a value, resized by keys, and reset by double-click", () => {
    const onChange = vi.fn();
    const onReset = vi.fn();
    render(<ResizeHandle label="Resize sidebar" value={240} min={200} max={420} onChange={onChange} onReset={onReset} />);
    const s = screen.getByRole("separator", { name: "Resize sidebar" });
    expect(s).toHaveAttribute("aria-valuenow", "240");
    expect(s).toHaveAttribute("aria-valuemin", "200");
    fireEvent.keyDown(s, { key: "ArrowRight" });
    expect(onChange).toHaveBeenCalledWith(256);
    fireEvent.keyDown(s, { key: "Home" });
    expect(onChange).toHaveBeenCalledWith(200);
    fireEvent.doubleClick(s);
    expect(onReset).toHaveBeenCalled();
  });
});

describe("Unread badge", () => {
  it("caps at 99+ and follows Count, Dot only and Off", () => {
    expect(badgeText(7)).toBe("7");
    expect(badgeText(99)).toBe("99");
    expect(badgeText(100)).toBe("99+");
    expect(badgeText(4321)).toBe("99+");
    expect(badgeKind(0, "count")).toBeNull();
    expect(badgeKind(5, "off")).toBeNull();
    expect(badgeKind(5, "dot")).toBe("dot");
    expect(badgeKind(5, "count")).toBe("count");
  });

  it("renders the setting", () => {
    const { rerender } = render(<UnreadCount n={999} />);
    expect(screen.getByTestId("unread-count")).toHaveTextContent("99+");
    act(() => updateDevicePrefs({ unreadBadge: "dot" }));
    rerender(<UnreadCount n={999} />);
    expect(screen.queryByTestId("unread-count")).toBeNull();
    expect(screen.getByTestId("unread-dot")).toHaveTextContent("Unread"); // labelled for a screen reader
    act(() => updateDevicePrefs({ unreadBadge: "off" }));
    rerender(<UnreadCount n={999} />);
    expect(screen.queryByTestId("unread-dot")).toBeNull();
    expect(screen.queryByTestId("unread-count")).toBeNull();
  });
});

describe("device prefs added for this round", () => {
  it("keeps the layout ids stable (Magazine and Headlines were only renamed) and parses defensively", () => {
    const p = parseDevicePrefs(JSON.stringify({ layout: "magazine", overrides: { feed: { "3": "headlines" }, folder: {} } }));
    expect(p.layout).toBe("magazine");
    expect(p.overrides.feed).toEqual({ "3": "headlines" });
    expect(p.articleWidth).toBe("medium");
    expect(p.linkTarget).toBeNull();
    expect(p.unreadBadge).toBe("count");
    const bad = parseDevicePrefs(
      JSON.stringify({ articleWidth: "huge", listWidth: 99999, sidebarWidth: 5, linkTarget: "popup", unreadBadge: "loud", collapsedFolders: ["1", 2], favoritesLocal: [{ t: "feed", id: "7" }, { t: "x", id: "1" }, { t: "feed", id: "7" }, { t: "folder", id: "a" }] }),
    );
    expect(bad).toMatchObject({ articleWidth: "medium", listWidth: 720, sidebarWidth: 200, linkTarget: null, unreadBadge: "count", collapsedFolders: ["1"] });
    expect(bad.favoritesLocal).toEqual([{ t: "feed", id: "7" }]);
    expect(DEFAULT_DEVICE_PREFS.sidebarWidth).toBe(240);
  });
});
