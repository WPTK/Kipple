import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DEFAULT_DEVICE_PREFS, parseDevicePrefs, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { Segmented } from "./segmented";
import { Switch } from "./kit";
import { maxFor } from "@/lib/useWidth";
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

describe("ResizeHandle drag", () => {
  const p = (type: "pointerDown" | "pointerMove" | "pointerUp" | "pointerCancel", el: Element, x: number) =>
    fireEvent[type](el, { pointerId: 1, clientX: x, button: 0 });

  it("previews while dragging, shows the live value, and reports once on release", () => {
    const onChange = vi.fn();
    const onPreview = vi.fn();
    render(<ResizeHandle label="Resize x" value={300} min={200} max={400} onChange={onChange} onPreview={onPreview} />);
    const s = screen.getByRole("separator", { name: "Resize x" });
    p("pointerDown", s, 100);
    p("pointerMove", s, 130);
    p("pointerMove", s, 150);
    expect(onPreview.mock.calls).toEqual([[330], [350]]);
    expect(s).toHaveAttribute("aria-valuenow", "350");
    expect(onChange).not.toHaveBeenCalled();
    p("pointerUp", s, 150);
    expect(onChange.mock.calls).toEqual([[350]]);
  });

  it("a cancelled drag puts the saved width back and saves nothing", () => {
    const onChange = vi.fn();
    const onPreview = vi.fn();
    render(<ResizeHandle label="Resize x" value={300} min={200} max={400} onChange={onChange} onPreview={onPreview} />);
    const s = screen.getByRole("separator", { name: "Resize x" });
    p("pointerDown", s, 100);
    p("pointerMove", s, 160);
    p("pointerCancel", s, 160);
    expect(onChange).not.toHaveBeenCalled();
    expect(onPreview).toHaveBeenLastCalledWith(null);
    expect(s).toHaveAttribute("aria-valuenow", "300");
  });

  it("dragging the sidebar writes the device preference once, on release, and never past what the window allows", () => {
    const onChange = vi.fn();
    render(<ResizeHandle label="Resize x" value={300} min={200} max={320} onChange={onChange} />);
    const s = screen.getByRole("separator", { name: "Resize x" });
    p("pointerDown", s, 0);
    p("pointerMove", s, 500);
    expect(s).toHaveAttribute("aria-valuenow", "320");
    p("pointerUp", s, 500);
    expect(onChange).toHaveBeenCalledOnce();
    expect(onChange).toHaveBeenCalledWith(320);
  });
});

describe("column limits", () => {
  it("leaves the rest of the row its minimum, keeps the fixed limits when nothing is measured, and floors at min", () => {
    expect(maxFor(0, 320, 260, 720)).toBe(720);
    expect(maxFor(1000, 320, 260, 720)).toBe(680);
    expect(maxFor(2000, 320, 260, 720)).toBe(720);
    expect(maxFor(400, 320, 260, 720)).toBe(260);
    // At the 900 px breakpoint the sidebar leaves 260 + 320 for the list and the article.
    expect(maxFor(900, 580, 200, 420)).toBe(320);
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

describe("visually hidden inputs stay where their control is", () => {
  it("every sr-only-live input sits in a positioned label, so focusing it scrolls its control into view (WCAG 2.4.11)", () => {
    // .sr-only-live is absolute at top/left 0: without a positioned ancestor a focused input is pinned to the
    // viewport corner, scroll-into-view has nothing to scroll to and the focus ring is drawn off screen.
    const { container } = render(
      <div>
        <Switch label="Shortcuts" checked={false} onChange={() => {}} />
        <Segmented legend="Size" value={1} options={[{ value: 1, label: "One" }, { value: 2, label: "Two" }]} onChange={() => {}} />
      </div>,
    );
    const inputs = container.querySelectorAll("input.sr-only-live");
    expect(inputs.length).toBe(3);
    for (const i of inputs) expect(i.closest("label")?.className).toMatch(/(^| )relative( |$)/);
  });
});
