import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { Modal, Skeleton } from "./kit";
import { Segmented } from "./segmented";
import { Button } from "./button";

describe("a sheet's first focus (#493)", () => {
  it("goes to the sheet itself while its fields are loading, so Tab reaches the first field before the footer", async () => {
    function Sheet() {
      const [loaded, setLoaded] = useState(false);
      return (
        <Modal open onOpenChange={() => {}} title="Edit feed" footer={<button type="button">Cancel</button>}>
          {loaded ? <input aria-label="Title" /> : <Skeleton label="Loading feed settings" />}
          <button type="button" onClick={() => setLoaded(true)} hidden>
            load
          </button>
        </Modal>
      );
    }
    render(<Sheet />);
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveFocus();
    (screen.getByText("load", { selector: "button" }) as HTMLButtonElement).click();
    await screen.findByRole("textbox", { name: "Title" });
    await userEvent.setup().tab();
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveFocus();
  });

  it("a busy status that is not the fields loading leaves focus on the first field", () => {
    render(
      <Modal open onOpenChange={() => {}} title="New filter" footer={<button type="button">Cancel</button>}>
        <input aria-label="Name" />
        <p role="status" aria-busy="true">
          Checking
        </p>
      </Modal>,
    );
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveFocus();
  });

  it("goes to the first field when the fields are there", () => {
    render(
      <Modal open onOpenChange={() => {}} title="Add feed" footer={<button type="button">Cancel</button>}>
        <input aria-label="Address" />
      </Modal>,
    );
    expect(screen.getByRole("textbox", { name: "Address" })).toHaveFocus();
  });
});

describe("segmented control Home and End (#495)", () => {
  const options = [
    { value: "s", label: "Small" },
    { value: "m", label: "Medium" },
    { value: "l", label: "Large" },
  ] as const;

  it("Home and End pick the first and last choice", async () => {
    const onChange = vi.fn();
    render(<Segmented legend="Size" value="m" options={options} onChange={onChange} />);
    screen.getByRole("radio", { name: "Medium" }).focus();
    const user = userEvent.setup();
    await user.keyboard("{End}");
    expect(screen.getByRole("radio", { name: "Large" })).toHaveFocus();
    expect(onChange).toHaveBeenLastCalledWith("l");
    await user.keyboard("{Home}");
    expect(screen.getByRole("radio", { name: "Small" })).toHaveFocus();
    expect(onChange).toHaveBeenLastCalledWith("s");
  });

  it("explicit: Home and End only move focus", async () => {
    const onChange = vi.fn();
    render(<Segmented legend="Size" value="m" options={options} onChange={onChange} explicit />);
    screen.getByRole("radio", { name: "Medium" }).focus();
    await userEvent.setup().keyboard("{End}");
    expect(screen.getByRole("radio", { name: "Large" })).toHaveFocus();
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe("Button aria-disabled (#492)", () => {
  it("stays focusable and does nothing when activated", async () => {
    const onClick = vi.fn();
    render(
      <Button aria-disabled onClick={onClick}>
        Refresh
      </Button>,
    );
    const b = screen.getByRole("button", { name: "Refresh" });
    b.focus();
    await userEvent.setup().keyboard("{Enter}");
    expect(b).toHaveFocus();
    expect(onClick).not.toHaveBeenCalled();
  });
});
