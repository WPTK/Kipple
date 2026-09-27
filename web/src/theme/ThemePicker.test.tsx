import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
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

describe("ThemePicker schedule", () => {
  const radio = (name: RegExp) => screen.getByRole("radio", { name });

  it("offers On a schedule next to Follow system, with the pair and the times in its summary", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, night: "carbon" });
    render(<ThemePicker />);
    const r = radio(/On a schedule/);
    expect(r).not.toBeChecked();
    // In the reader's own clock format (9:00 PM in en-US, 21:00 in most others).
    const fmt = (h: number) => new Date(2000, 0, 1, h, 0).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
    expect(r.closest("label")?.textContent).toContain(`Carbon from ${fmt(21)} to ${fmt(7)}`);
    // No time fields until the schedule is chosen.
    expect(screen.queryByLabelText("Night starts")).toBeNull();
  });

  it("choosing it keeps the day and night picks and shows the two time fields", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, day: "linen", night: "carbon" });
    render(<ThemePicker />);
    fireEvent.click(radio(/On a schedule/));
    expect(themeStore.get()).toMatchObject({ mode: "follow", schedule: true, day: "linen", night: "carbon" });
    expect(radio(/On a schedule/)).toBeChecked();
    expect(radio(/Follow system/)).not.toBeChecked();
    expect((screen.getByLabelText("Day theme") as HTMLSelectElement).value).toBe("linen");
    expect((screen.getByLabelText("Night theme") as HTMLSelectElement).value).toBe("carbon");
    expect((screen.getByLabelText("Night starts") as HTMLInputElement).value).toBe("21:00");
    expect((screen.getByLabelText("Day starts") as HTMLInputElement).value).toBe("07:00");
    expect(screen.getByText(/this device.s clock/)).toBeInTheDocument();
  });

  it("a typed time is saved on leaving the field, not on each keystroke; a cleared field restores the saved time", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true });
    render(<ThemePicker />);
    const night = screen.getByLabelText("Night starts") as HTMLInputElement;
    // Typing "22:30" over 21:00 passes through "02:00": nothing is saved yet, however long the pauses.
    fireEvent.keyDown(night, { key: "2" });
    fireEvent.change(night, { target: { value: "02:00" } });
    fireEvent.change(night, { target: { value: "22:00" } });
    fireEvent.change(night, { target: { value: "22:30" } });
    expect(themeStore.get().nightStart).toBe("21:00");
    fireEvent.blur(night);
    expect(themeStore.get().nightStart).toBe("22:30");
    fireEvent.keyDown(night, { key: "Backspace" });
    fireEvent.change(night, { target: { value: "" } });
    expect(night.value).toBe("");
    fireEvent.blur(night);
    expect(themeStore.get().nightStart).toBe("22:30");
    expect(night.value).toBe("22:30");
  });

  it("Enter saves the time; so does closing the picker (leaving the field), but never a change on its own", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true });
    render(<ThemePicker />);
    const day = screen.getByLabelText("Day starts") as HTMLInputElement;
    fireEvent.change(day, { target: { value: "06:15" } });
    expect(themeStore.get().dayStart).toBe("07:00");
    fireEvent.keyDown(day, { key: "Enter" });
    expect(themeStore.get().dayStart).toBe("06:15");
    // A desktop picker reports each column click: 02:30, then 22:30. Only the time the picker closes on is saved.
    const night = screen.getByLabelText("Night starts") as HTMLInputElement;
    fireEvent.change(night, { target: { value: "02:30" } });
    fireEvent.change(night, { target: { value: "22:30" } });
    expect(themeStore.get().nightStart).toBe("21:00");
    fireEvent.blur(night);
    expect(themeStore.get().nightStart).toBe("22:30");
  });
  it("a typed time waiting when Settings closes, or the page is left, is saved", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true });
    const { unmount } = render(<ThemePicker />);
    const day = screen.getByLabelText("Day starts");
    fireEvent.keyDown(day, { key: "5" });
    fireEvent.change(day, { target: { value: "05:30" } });
    window.dispatchEvent(new Event("pagehide"));
    expect(themeStore.get().dayStart).toBe("05:30");
    const night = screen.getByLabelText("Night starts");
    fireEvent.keyDown(night, { key: "2" });
    fireEvent.change(night, { target: { value: "23:15" } });
    unmount();
    expect(themeStore.get()).toMatchObject({ dayStart: "05:30", nightStart: "23:15" });
  });

  it("a change made elsewhere while a typed time waits wins over the waiting time", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true });
    render(<ThemePicker />);
    const night = screen.getByLabelText("Night starts") as HTMLInputElement;
    fireEvent.keyDown(night, { key: "2" });
    fireEvent.change(night, { target: { value: "22:30" } });
    act(() => themeStore.set((s) => ({ ...s, nightStart: "23:00" })));
    expect(night.value).toBe("23:00");
    fireEvent.blur(night);
    expect(themeStore.get().nightStart).toBe("23:00");
  });
  it("a browser that reports seconds still saves the time, in whole minutes", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true });
    render(<ThemePicker />);
    const day = screen.getByLabelText("Day starts") as HTMLInputElement;
    fireEvent.change(day, { target: { value: "06:45:00" } });
    fireEvent.blur(day);
    expect(themeStore.get().dayStart).toBe("06:45");
  });
  it("follows a change made elsewhere (another tab, the server)", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true });
    render(<ThemePicker />);
    act(() => themeStore.set((s) => ({ ...s, dayStart: "05:45" })));
    expect((screen.getByLabelText("Day starts") as HTMLInputElement).value).toBe("05:45");
  });

  it("says when equal times leave the day theme on", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true, nightStart: "08:00", dayStart: "08:00" });
    render(<ThemePicker />);
    expect(screen.getByText(/same time, so the day theme stays on/)).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /On a schedule/ }).closest("label")?.textContent).toContain("Paper all day");
    expect(screen.queryByText(/two times are the same/)).toBeNull();
  });

  it("picking a fixed theme leaves the schedule and hides its fields, keeping the times for later", () => {
    themeStore.set({ ...DEFAULT_THEME_SETTINGS, mode: "follow", schedule: true, nightStart: "22:00" });
    render(<ThemePicker />);
    fireEvent.click(radio(/Graphite/));
    expect(themeStore.get()).toMatchObject({ mode: "fixed", fixed: "graphite", schedule: false, nightStart: "22:00" });
    expect(screen.queryByLabelText("Night starts")).toBeNull();
    expect(screen.queryByLabelText("Day theme")).toBeNull();
  });
});