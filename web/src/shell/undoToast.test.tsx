import { afterEach, describe, expect, it } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { UndoToast } from "./toasts";
import { beginUndo, pushUndo, resetUndo, type UndoTicket } from "@/lib/undo";

afterEach(() => resetUndo());

describe("UndoToast while an undo waits", () => {
  it("keeps the focused Undo button, marked disabled, and says Undoing…", async () => {
    render(<UndoToast />);
    act(() => pushUndo({ kind: "read", ids: ["1"], undo: () => {} }));
    let t!: UndoTicket;
    act(() => {
      t = beginUndo("read"); // joins the toast's group, still in flight
    });
    const button = screen.getByRole("button", { name: "Undo" });
    button.focus();
    act(() => button.click());
    expect(screen.getByText("Undoing…")).toBeTruthy();
    const same = screen.getByRole("button", { name: "Undo" });
    expect(same).toBe(button);
    expect(same.getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(button);
    await act(async () => t.settle(null));
  });
});

describe("UndoToast dismiss", () => {
  it("closes the toast with a Dismiss button, leaving the group on the undo stack", () => {
    render(<UndoToast />);
    act(() => pushUndo({ kind: "read", ids: ["1"], undo: () => {} }));
    expect(screen.getByRole("button", { name: "Undo" })).toBeTruthy();
    act(() => screen.getByRole("button", { name: "Dismiss" }).click());
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull();
  });
});

describe("a toast's action", () => {
  it("runs, then closes the toast", async () => {
    const { Toasts, toast, clearToasts } = await import("./toasts");
    render(<Toasts />);
    let ran = 0;
    act(() => void toast("Feed paused", "info", { label: "Resume", run: () => void ran++ }));
    act(() => screen.getByRole("button", { name: "Resume" }).click());
    expect(ran).toBe(1);
    expect(screen.queryByText("Feed paused")).toBeNull();
    act(() => clearToasts());
  });
});
