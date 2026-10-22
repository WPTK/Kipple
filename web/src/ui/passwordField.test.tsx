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
