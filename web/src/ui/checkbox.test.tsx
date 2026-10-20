import { readFileSync } from "node:fs";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Checkbox, Switch } from "./kit";

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

  it("a disabled box cannot be changed, and an error is announced and described", async () => {
    const onChange = vi.fn();
    const { rerender } = render(<Checkbox label="Apply" checked={false} onChange={onChange} disabled />);
    await userEvent.setup().click(screen.getByText("Apply"));
    expect(onChange).not.toHaveBeenCalled();
    rerender(<Checkbox label="Apply" checked={false} onChange={onChange} error="Not now." />);
    expect(screen.getByRole("alert")).toHaveTextContent("Not now.");
    expect(screen.getByRole("checkbox", { name: "Apply" })).toHaveAccessibleDescription(expect.stringContaining("Not now."));
  });

  it("is a 44 px row on a phone, so it is as easy to hit as the Switch beside it", () => {
    render(
      <>
        <Checkbox label="Tick" checked={false} onChange={() => {}} />
        <Switch label="Flip" checked={false} onChange={() => {}} />
      </>,
    );
    expect(screen.getByText("Tick").closest("label")?.className).toContain("min-h-11");
    expect(screen.getByText("Flip").closest("label")?.className).toContain("min-h-11");
  });
});

// A switch is for a setting that takes effect the moment it is flipped. These forms and dialogs apply their options with
// a button, so they offer checkboxes (issue #286). The check reads the source so a switch cannot creep back in unnoticed.
describe("forms use checkboxes, not switches", () => {
  const FORMS = [
    "screens/feeds/AddFeedDialog.tsx",
    "screens/feeds/BulkActions.tsx",
    "screens/feeds/FeedEditor.tsx",
    "screens/feeds/OpmlDialog.tsx",
    "screens/filters/FilterEditor.tsx",
    "screens/StatsDataDialogs.tsx",
    "screens/WrappedScreen.tsx",
    "setup/FeedsStep.tsx",
  ];
  it.each(FORMS)("%s has no Switch", (file) => {
    const src = readFileSync(`src/${file}`, "utf8");
    expect(src).not.toMatch(/<Switch\b/);
  });
});
