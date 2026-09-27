import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { themeStore } from "@/theme/theme";
import { ThemeSelect } from "./AppearanceControls";

vi.mock("@/api/admin", () => ({ useSettings: () => ({ data: undefined }) }));

afterEach(() => act(() => themeStore.set({ ...DEFAULT_THEME_SETTINGS })));

// The server metadata only supplies the label and narrows the schemes; without it every local scheme is offered.
const show = () => render(<ThemeSelect />);

describe("the reading menu's theme select", () => {
  it("lists Match my device, then On a schedule, before the schemes", () => {
    show();
    const opts = Array.from((screen.getByLabelText("Theme") as HTMLSelectElement).options).map((o) => o.textContent);
    expect(opts.slice(0, 2)).toEqual(["Match my device", "On a schedule"]);
  });

  it("shows and sets the schedule without touching its times or the day and night picks", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "fixed", fixed: "graphite", nightStart: "22:00", night: "carbon" });
    show();
    const sel = screen.getByLabelText("Theme") as HTMLSelectElement;
    expect(sel.value).toBe("graphite");
    fireEvent.change(sel, { target: { value: "__schedule" } });
    expect(themeStore.get()).toMatchObject({ mode: "schedule", nightStart: "22:00", night: "carbon", fixed: "graphite" });
    expect(sel.value).toBe("__schedule");
    fireEvent.change(sel, { target: { value: "__follow" } });
    expect(themeStore.get().mode).toBe("follow");
    fireEvent.change(sel, { target: { value: "linen" } });
    expect(themeStore.get()).toMatchObject({ mode: "fixed", fixed: "linen" });
  });
});
