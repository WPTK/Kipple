import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { STEPPER_DEBOUNCE_MS, Stepper, stepperProblem } from "./kit";
import { SettingField } from "@/screens/SettingField";
import type { SettingMeta } from "@/api/admin";
import { json, mockFetch } from "@/test/mockApi";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

const type = (box: HTMLElement, v: string) => fireEvent.change(box, { target: { value: v } });

describe("Stepper", () => {
  it("typing 120 commits once, 120, after the debounce (not 1 and 12 on the way)", () => {
    const onChange = vi.fn();
    render(<Stepper label="Interval" value={30} min={5} max={1440} onChange={onChange} />);
    const box = screen.getByRole("spinbutton", { name: "Interval" });
    type(box, "1");
    act(() => void vi.advanceTimersByTime(200));
    type(box, "12");
    act(() => void vi.advanceTimersByTime(200));
    type(box, "120");
    expect(onChange).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(STEPPER_DEBOUNCE_MS + 10));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(120);
  });

  it("the debounce is at least 600 ms", () => {
    expect(STEPPER_DEBOUNCE_MS).toBeGreaterThanOrEqual(600);
  });

  it("Enter and blur commit at once", () => {
    const onChange = vi.fn();
    render(<Stepper label="Interval" value={30} min={5} max={1440} onChange={onChange} />);
    const box = screen.getByRole("spinbutton", { name: "Interval" });
    type(box, "45");
    fireEvent.keyDown(box, { key: "Enter" });
    expect(onChange).toHaveBeenLastCalledWith(45);
    type(box, "50");
    fireEvent.blur(box);
    expect(onChange).toHaveBeenLastCalledWith(50);
    expect(onChange).toHaveBeenCalledTimes(2);
  });

  it("validates min and max locally and sends nothing for a bad value", () => {
    const onChange = vi.fn();
    render(<Stepper label="Interval" value={30} min={5} max={1440} onChange={onChange} />);
    const box = screen.getByRole("spinbutton", { name: "Interval" });
    type(box, "1");
    fireEvent.blur(box);
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("Enter a number from 5 to 1440.");
    expect(box).toHaveAttribute("aria-invalid", "true");
    type(box, "");
    fireEvent.blur(box);
    expect(onChange).not.toHaveBeenCalled();
    type(box, "9999");
    fireEvent.keyDown(box, { key: "Enter" });
    expect(onChange).not.toHaveBeenCalled();
  });

  it("rapid + clicks coalesce into one commit", () => {
    const onChange = vi.fn();
    render(<Stepper label="Interval" value={30} step={5} min={5} max={1440} onChange={onChange} />);
    const plus = screen.getByRole("button", { name: "Increase Interval" });
    fireEvent.click(plus);
    fireEvent.click(plus);
    fireEvent.click(plus);
    expect(screen.getByRole("spinbutton", { name: "Interval" })).toHaveValue(45);
    expect(onChange).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(STEPPER_DEBOUNCE_MS + 10));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(45);
  });

  it("an edit pending when the screen is left is not dropped", () => {
    const onChange = vi.fn();
    const { unmount } = render(<Stepper label="Interval" value={30} min={5} max={1440} onChange={onChange} />);
    type(screen.getByRole("spinbutton", { name: "Interval" }), "60");
    unmount();
    expect(onChange).toHaveBeenCalledWith(60);
  });

  it("stepperProblem reports each failure", () => {
    expect(stepperProblem("", 5, 10, 1)).toMatch(/Enter a whole number/);
    expect(stepperProblem("1.5", 5, 10, 1)).toMatch(/whole number/);
    expect(stepperProblem("4", 5, 10, 1)).toMatch(/from 5 to 10/);
    expect(stepperProblem("7", 5, 10, 1)).toBeNull();
  });
});

describe("SettingField integer PATCHes", () => {
  const meta: SettingMeta = {
    key: "refresh.interval_minutes",
    label: "How often to check feeds",
    description: "",
    kind: "int",
    value: 30,
    default: 30,
    min: 5,
    max: 1440,
    unit: "minutes",
    group: "sync",
    surface: "server",
  } as unknown as SettingMeta;

  it("sends one PATCH at a time, latest value wins, and never lands an older value last", async () => {
    vi.useRealTimers();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const gates: (() => void)[] = [];
    const sent: number[] = [];
    mockFetch({
      "PATCH /api/settings": async (_u, init) => {
        const v = (JSON.parse(String(init?.body)) as Record<string, number>)["refresh.interval_minutes"] as number;
        sent.push(v);
        await new Promise<void>((r) => gates.push(r));
        return json({ settings: [{ ...meta, value: v }], values: { "refresh.interval_minutes": v } });
      },
    });
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <SettingField meta={meta} />
      </QueryClientProvider>,
    );
    const box = screen.getByRole("spinbutton", { name: "How often to check feeds" });
    type(box, "40");
    fireEvent.blur(box);
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)));
    expect(sent).toEqual([40]);
    // While 40 is in flight: two more edits. Neither may be sent yet.
    type(box, "50");
    fireEvent.blur(box);
    type(box, "60");
    fireEvent.blur(box);
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)));
    expect(sent).toEqual([40]);
    await act(async () => {
      gates.shift()?.();
      await vi.advanceTimersByTimeAsync(0);
    });
    // Only the latest queued value follows.
    expect(sent).toEqual([40, 60]);
    await act(async () => {
      gates.shift()?.();
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(sent).toEqual([40, 60]);
  });
});
