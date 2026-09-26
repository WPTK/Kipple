import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { SCHEMES } from "./schemes";
import { DEFAULT_THEME_SETTINGS } from "./settings";
import { themeStore } from "./theme";
import { ThemePicker } from "./ThemePicker";

const narrow = vi.hoisted(() => ({ ids: null as string[] | null }));
vi.mock("./serverThemes", async (orig) => {
  const m = await orig<typeof import("./serverThemes")>();
  return { ...m, useAllowedSchemes: () => m.pickSchemes(narrow.ids) };
});

afterEach(() => {
  narrow.ids = null;
  act(() => themeStore.set(DEFAULT_THEME_SETTINGS));
});

describe("ThemePicker when the server narrows the schemes", () => {
  it("a stored fixed theme that is no longer offered shows the default as selected", () => {
    narrow.ids = ["paper", "midnight", "linen"];
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "fixed", fixed: "inkwell" });
    render(<ThemePicker />);
    const checked = screen.getAllByRole("radio").filter((r) => (r as HTMLInputElement).checked);
    expect(checked).toHaveLength(1);
    expect(screen.getByText(/no longer offered/)).toBeInTheDocument();
  });

  it("Day and Night selects show an offered scheme when the stored pair is not offered", () => {
    narrow.ids = ["linen", "carbon"];
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", day: "paper", night: "midnight" });
    render(<ThemePicker />);
    const day = screen.getByLabelText("Day theme") as HTMLSelectElement;
    const night = screen.getByLabelText("Night theme") as HTMLSelectElement;
    expect(["linen", "carbon"]).toContain(day.value);
    expect(["linen", "carbon"]).toContain(night.value);
    expect(day.selectedOptions[0]?.textContent).toBe(SCHEMES.find((s) => s.id === day.value)?.name);
  });
});
