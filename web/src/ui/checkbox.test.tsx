import { readdirSync, readFileSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Checkbox, RadioCard, RadioRow } from "./kit";

describe("Checkbox", () => {
  it("is a native checkbox named by its label, described by its help, and reports the new value", async () => {
    const onChange = vi.fn();
    render(<Checkbox label="Check this feed" help="Untick to stop checking." checked={false} onChange={onChange} />);
    const box = screen.getByRole("checkbox", { name: "Check this feed" });
    expect(box).not.toBeChecked();
    expect(box).toHaveAccessibleDescription("Untick to stop checking.");
    expect(screen.queryByRole("switch")).toBeNull();
    await userEvent.setup().click(screen.getByText("Check this feed"));
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

// A checkbox or radio is one shared component (`Checkbox`, `RadioCard`, `RadioRow` in ui/kit.tsx), so a form's options
// look, size and align the same everywhere. A raw `<input type="checkbox|radio">` may appear only where the control is
// not a labelled option in a form: the rows of a list that are picked for a bulk action (exempt in
// docs/maintainers/ui-decisions.md), and the two controls drawn as something else. Every source file is scanned.
// The number of raw inputs each allowed file may hold, so a new one in an allowed file fails too.
const RAW_INPUT_ALLOWED: Record<string, number> = {
  "ui/kit.tsx": 4, // the shared components: Switch, Checkbox, RadioCard, RadioRow
  "ui/segmented.tsx": 1, // the segmented control (native radios drawn as buttons)
  "theme/ThemePicker.tsx": 2, // swatches: native radios drawn as colour tiles
  "setup/FeedsStep.tsx": 1, // list rows picked for a bulk action
  "screens/FeedsScreen.tsx": 2, // list rows picked for a bulk action
  "screens/HealthScreen.tsx": 3, // list rows picked for a bulk action
};

/** How many `<input type="checkbox">` or `type="radio"` a source text holds. */
function rawInputs(text: string): number {
  return (text.match(/type=(["'{]+)(checkbox|radio)\1?/g) ?? []).length;
}

describe("options use the shared components", () => {
  it('<input type="checkbox"> and <input type="radio"> appear only in the allowed places, as often as allowed', () => {
    const root = resolve("src");
    const found: Record<string, number> = {};
    for (const f of sources(root)) {
      const n = rawInputs(readFileSync(f, "utf8"));
      if (n > 0) found[relative(root, f).split(sep).join("/")] = n;
    }
    expect(found).toEqual(RAW_INPUT_ALLOWED);
  });

  it("the matcher counts a hand-made checkbox or radio, written either way, and nothing else", () => {
    expect(rawInputs('<input type="checkbox" checked /><input\n type="radio" name="x" />')).toBe(2);
    expect(rawInputs("<input type={'radio'} />")).toBe(1);
    expect(rawInputs('<input type="text" /><Checkbox label="a" checked onChange={f} />')).toBe(0);
  });
});

describe("RadioCard", () => {
  it("is a native radio group by name: one checked, arrow keys move it, and the value is the radio's value", async () => {
    function Host() {
      const [v, setV] = useState("a");
      return (
        <fieldset>
          <legend>Pick</legend>
          {["a", "b"].map((x) => (
            <RadioCard key={x} name="pick" value={x} checked={v === x} onSelect={() => setV(x)} title={`Option ${x}`} help={`About ${x}.`} />
          ))}
        </fieldset>
      );
    }
    render(<Host />);
    expect(screen.getByRole("group", { name: "Pick" })).toBeInTheDocument();
    const a = screen.getByRole("radio", { name: /^Option a/ });
    const b = screen.getByRole("radio", { name: /^Option b/ });
    expect(a).toBeChecked();
    expect(b).toHaveAttribute("value", "b");
    expect(a).toHaveAttribute("name", "pick");
    a.focus();
    await userEvent.setup().keyboard("{ArrowDown}");
    expect(b).toBeChecked();
    expect(a).not.toBeChecked();
  });

  it("with children: they sit outside the name and describe the radio; the title or the description picks the card, a field in it does not", async () => {
    const onSelect = vi.fn();
    render(
      <RadioCard name="r" checked={false} onSelect={onSelect} title="With a password">
        <p>The usual choice.</p>
        <input aria-label="Password" />
        <span role="button" tabIndex={0}>
          Show tips
        </span>
        <span contentEditable suppressContentEditableWarning>
          Note
        </span>
        <details>
          <summary>More</summary>
        </details>
      </RadioCard>,
    );
    const radio = screen.getByRole("radio", { name: "With a password" });
    expect(radio).toHaveAccessibleDescription(expect.stringContaining("The usual choice."));
    const user = userEvent.setup();
    await user.click(screen.getByText("With a password"));
    expect(onSelect).toHaveBeenCalledTimes(1);
    await user.click(screen.getByText("The usual choice."));
    expect(onSelect).toHaveBeenCalledTimes(2);
    await user.click(screen.getByLabelText("Password"));
    await user.click(screen.getByRole("button", { name: "Show tips" }));
    await user.click(screen.getByText("Note"));
    await user.click(screen.getByText("More"));
    expect(onSelect).toHaveBeenCalledTimes(2);
  });

  it("a disabled card with children ignores a click on its description", async () => {
    const onSelect = vi.fn();
    render(
      <RadioCard name="r" checked={false} onSelect={onSelect} disabled title="Not offered">
        <p>Why not.</p>
      </RadioCard>,
    );
    await userEvent.setup().click(screen.getByText("Why not."));
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("a card without children is one label: the whole card picks it", async () => {
    const onSelect = vi.fn();
    render(<RadioCard name="r" checked={false} onSelect={onSelect} title="Everywhere" help="Every feed" />);
    await userEvent.setup().click(screen.getByText("Every feed"));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("a disabled RadioRow is disabled and dimmed", () => {
    render(
      <RadioRow name="r" checked={false} onSelect={() => {}} disabled>
        Keep the feeds
      </RadioRow>,
    );
    expect(screen.getByRole("radio", { name: "Keep the feeds" })).toBeDisabled();
    expect(screen.getByText("Keep the feeds").closest("label")).toHaveClass("cursor-not-allowed", "opacity-60");
  });

  it("a disabled card cannot be chosen", async () => {
    const onSelect = vi.fn();
    render(<RadioCard name="r" checked={false} onSelect={onSelect} disabled title="Not now" />);
    await userEvent.setup().click(screen.getByText("Not now"));
    expect(onSelect).not.toHaveBeenCalled();
    expect(screen.getByRole("radio", { name: "Not now" })).toBeDisabled();
  });
});
