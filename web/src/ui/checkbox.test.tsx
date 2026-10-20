import { readdirSync, readFileSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Checkbox } from "./kit";

describe("Checkbox", () => {
  it("is a native checkbox named by its label, described by its help, and reports the new value", async () => {
    const onChange = vi.fn();
    render(<Checkbox label="Enabled" help="Untick to stop checking." checked={false} onChange={onChange} />);
    const box = screen.getByRole("checkbox", { name: "Enabled" });
    expect(box).not.toBeChecked();
    expect(box).toHaveAccessibleDescription("Untick to stop checking.");
    expect(screen.queryByRole("switch")).toBeNull();
    await userEvent.setup().click(screen.getByText("Enabled"));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("follows its checked prop and toggles from the label", async () => {
    function Host() {
      const [on, setOn] = useState(true);
      return <Checkbox label="Ignore accents" checked={on} onChange={setOn} />;
    }
    render(<Host />);
    const box = screen.getByRole("checkbox", { name: "Ignore accents" });
    expect(box).toBeChecked();
    await userEvent.setup().click(box);
    expect(box).not.toBeChecked();
  });

  it("a disabled box cannot be changed, and an error is announced, described and marks the box invalid", async () => {
    const onChange = vi.fn();
    const { rerender } = render(<Checkbox label="Apply" checked={false} onChange={onChange} disabled />);
    await userEvent.setup().click(screen.getByText("Apply"));
    expect(onChange).not.toHaveBeenCalled();
    rerender(<Checkbox label="Apply" checked={false} onChange={onChange} error="Not now." />);
    expect(screen.getByRole("alert")).toHaveTextContent("Not now.");
    const box = screen.getByRole("checkbox", { name: "Apply" });
    expect(box).toHaveAccessibleDescription(expect.stringContaining("Not now."));
    expect(box).toBeInvalid();
  });

  it("the box and the text are inside one label, so a tap in the box column toggles it; the help is outside the label", async () => {
    const onChange = vi.fn();
    render(<Checkbox label="Tick" help="Some help." checked={false} onChange={onChange} />);
    const box = screen.getByRole("checkbox", { name: "Tick" });
    const label = box.closest("label");
    expect(label).not.toBeNull();
    expect(label).toContainElement(screen.getByText("Tick"));
    expect(label).not.toContainElement(screen.getByText("Some help."));
    await userEvent.setup().click(box.parentElement as HTMLElement); // the box column, not the box itself
    expect(onChange).toHaveBeenCalledWith(true);
    // The real 44 px and 56 px sizes and the alignment are measured in a browser: web/uat/controls.mjs (K6).
  });
});

// A switch is for a setting that takes effect the moment it is flipped. Forms and dialogs apply their options with a
// button, so they offer checkboxes (issue #286). Every source file is scanned, so a new file is covered: a <Switch> may
// appear only in the places below, each of which applies the change at once.
const INSTANT_APPLY = [
  "screens/AppearanceControls.tsx", // Settings > Appearance & Reading: each flip is saved
  "screens/SettingField.tsx", // Settings: each flip is saved
  "screens/SettingsScreen.tsx", // Settings: each flip is saved
  "screens/filters/FiltersSection.tsx", // the Filters list: a rule's on/off applies at once
];

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) return sources(p);
    return /\.tsx$/.test(e.name) && !/\.test\.tsx$/.test(e.name) ? [p] : [];
  });
}

describe("forms use checkboxes, not switches", () => {
  it("<Switch> appears only in the instant-apply places", () => {
    const root = resolve("src");
    const used = sources(root)
      .filter((f) => /<Switch\b/.test(readFileSync(f, "utf8")))
      .map((f) => relative(root, f).split(sep).join("/"))
      .filter((f) => f !== "ui/kit.tsx");
    expect(used.filter((f) => !INSTANT_APPLY.includes(f))).toEqual([]);
  });
});
