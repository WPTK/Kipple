import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render } from "@testing-library/react";
import { useRef } from "react";
import { DND_ROW_CLASS, useRowDnd, type DragSource } from "./dnd";

const src: DragSource = { kind: "feed", id: "f1", group: "g" };

function List({ onDrop = () => {} }: { onDrop?: () => void }) {
  const scroller = useRef<HTMLDivElement>(null);
  const dnd = useRowDnd({ enabled: true, onDrop, scroller: () => scroller.current });
  return (
    <div ref={scroller} data-testid="list">
      <div data-testid="row" className={DND_ROW_CLASS} {...dnd.rowProps(src)}>
        row
      </div>
      <button data-testid="grip" {...dnd.gripProps(src)}>
        grip
      </button>
    </div>
  );
}

const touch = (el: Element, type: "pointerDown" | "pointerMove", x = 5, y = 5) =>
  fireEvent[type](el, { pointerType: "touch", pointerId: 3, clientX: x, clientY: y, isPrimary: true, button: 0 });
const move = () => {
  const ev = new Event("touchmove", { bubbles: true, cancelable: true });
  document.querySelector('[data-testid="row"]')!.dispatchEvent(ev);
  return ev.defaultPrevented;
};

afterEach(() => vi.useRealTimers());

describe("touch drag on the row itself", () => {
  it("installs the non-passive touchmove guard on the list at mount, not after the hold", () => {
    const add = vi.spyOn(HTMLElement.prototype, "addEventListener");
    render(<List />);
    const call = add.mock.calls.find((c) => c[0] === "touchmove" && (c[2] as AddEventListenerOptions | undefined)?.passive === false);
    expect(call).toBeDefined();
    add.mockRestore();
  });

  it("cancels the scroll only while a drag is live: not before the hold, yes after it, not after the drop", () => {
    vi.useFakeTimers();
    const { getByTestId } = render(<List />);
    const row = getByTestId("row");
    touch(row, "pointerDown");
    expect(move()).toBe(false);
    act(() => void vi.advanceTimersByTime(300));
    expect(move()).toBe(true);
    fireEvent.pointerUp(row, { pointerType: "touch", pointerId: 3 });
    expect(move()).toBe(false);
  });

  it("a touch that moves before the hold ends is a scroll and never starts a drag", () => {
    vi.useFakeTimers();
    const { getByTestId } = render(<List />);
    const row = getByTestId("row");
    touch(row, "pointerDown");
    touch(row, "pointerMove", 5, 40);
    act(() => void vi.advanceTimersByTime(400));
    expect(move()).toBe(false);
  });

  it("the grip still starts at once", () => {
    const { getByTestId } = render(<List />);
    touch(getByTestId("grip"), "pointerDown");
    expect(move()).toBe(true);
  });

  it("rows scroll vertically, suppress the iOS link callout and text selection, and swallow a held-touch context menu", () => {
    vi.useFakeTimers();
    const { getByTestId } = render(<List />);
    const row = getByTestId("row");
    expect(row.className).toContain("touch-pan-y");
    expect(row.className).toContain("[-webkit-touch-callout:none]");
    expect(row.className).toContain("select-none");
    touch(row, "pointerDown");
    const menu = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    row.dispatchEvent(menu);
    expect(menu.defaultPrevented).toBe(true);
  });
});
