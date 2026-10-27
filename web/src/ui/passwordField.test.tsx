import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { PasswordField } from "./kit";

function Harness() {
  const [v, setV] = useState("hunter22");
  return <PasswordField label="Password" value={v} onChange={setV} autoComplete="off" error="Too short." />;
}

describe("PasswordField", () => {
  it("hides the value until Show is pressed, and Hide puts it back", async () => {
    render(<Harness />);
    const input = screen.getByLabelText("Password");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAccessibleDescription("Too short.");
    await userEvent.setup().click(screen.getByRole("button", { name: "Show password" }));
    expect(input).toHaveAttribute("type", "text");
    await userEvent.setup().click(screen.getByRole("button", { name: "Hide password" }));
    expect(input).toHaveAttribute("type", "password");
  });
});

describe("PasswordField names", () => {
  it("gives two fields in one dialog two different Show buttons, each controlling its own input", () => {
    render(
      <>
        <PasswordField label="Current password" value="" onChange={() => {}} autoComplete="current-password" />
        <PasswordField label="New password" value="" onChange={() => {}} autoComplete="new-password" />
      </>,
    );
    const cur = screen.getByRole("button", { name: "Show current password" });
    const next = screen.getByRole("button", { name: "Show new password" });
    expect(cur.getAttribute("aria-controls")).toBe(screen.getByLabelText("Current password").id);
    expect(next.getAttribute("aria-controls")).toBe(screen.getByLabelText("New password").id);
  });
});

describe("PasswordField acronym", () => {
  it("keeps the capitals of an acronym in the Show button's name", () => {
    render(<PasswordField label="API password" value="" onChange={() => {}} autoComplete="off" />);
    expect(screen.getByRole("button", { name: "Show API password" })).toBeInTheDocument();
  });
});

describe("PasswordField names by case", () => {
  it.each([
    ["Current password", "Show current password"],
    ["API password", "Show API password"],
    ["A password", "Show a password"],
    ["Passphrase", "Show password"],
  ])("%s gives the button %s", (label, name) => {
    render(<PasswordField label={label} value="" onChange={() => {}} autoComplete="off" />);
    expect(screen.getByRole("button", { name })).toBeInTheDocument();
  });
});

describe("PasswordField disabled", () => {
  it("disables the Show button with its input", () => {
    render(<PasswordField label="Login" value="" onChange={() => {}} autoComplete="off" disabled />);
    expect(screen.getByLabelText("Login")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Show password" })).toBeDisabled();
  });
});
