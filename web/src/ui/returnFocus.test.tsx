import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Modal } from "./kit";

// A Modal is opened by state, not by a Dialog.Trigger, so Radix has no trigger to give focus back to on close.
function Host() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>Open it</button>
      <Modal open={open} onOpenChange={setOpen} title="A dialog">
        <input aria-label="Name" />
      </Modal>
    </>
  );
}

describe("Modal", () => {
  it("gives focus back to the control that opened it when Escape closes it", async () => {
    const user = userEvent.setup();
    render(<Host />);
    const opener = screen.getByRole("button", { name: "Open it" });
    opener.focus();
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(opener).not.toHaveFocus();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(opener).toHaveFocus());
  });
});
