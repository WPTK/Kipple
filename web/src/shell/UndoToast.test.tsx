import { afterEach, describe, expect, it } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { UndoToast } from "./UndoToast";
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
