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
    // The bad number is not kept: the box shows the saved one again, under the reason, and is no longer invalid.
    expect(box).toHaveValue(30);
    expect(box).not.toHaveAttribute("aria-invalid");
    expect(screen.getByRole("alert")).toHaveTextContent("Kept 30.");
    type(box, "");
    fireEvent.blur(box);
    expect(onChange).not.toHaveBeenCalled();
    type(box, "9999");
    fireEvent.keyDown(box, { key: "Enter" });
    expect(onChange).not.toHaveBeenCalled();
  });

  it("a pause on half-typed text does not snap it back (3 on the way to 30)", () => {
    const onChange = vi.fn();
    render(<Stepper label="Interval" value={60} min={5} max={1440} onChange={onChange} />);
    const box = screen.getByRole("spinbutton", { name: "Interval" });
    type(box, "3");
    act(() => void vi.advanceTimersByTime(STEPPER_DEBOUNCE_MS + 10));
    expect(box).toHaveValue(3);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
    type(box, "30");
    act(() => void vi.advanceTimersByTime(STEPPER_DEBOUNCE_MS + 10));
    expect(onChange).toHaveBeenCalledWith(30);
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

describe("SettingField save model", () => {
  const base = { group: "sync", surface: "settings", default: "", options: undefined } as unknown as Partial<SettingMeta>;
  const text: SettingMeta = { ...base, key: "fetch.user_agent", label: "Custom browser identity", description: "Replaces the built-in identity. Leave empty for the default.", kind: "text", value: "" } as SettingMeta;
  const flag: SettingMeta = { ...base, key: "links.strip_tracking", label: "Remove tracking from links", description: "Removes tracking parameters. The stored article is not changed.", kind: "bool", value: false, default: false } as SettingMeta;

  const mount = (meta: SettingMeta, patch: (body: Record<string, unknown>) => Response | Promise<Response>) => {
    mockFetch({ "PATCH /api/settings": (_u, init) => patch(JSON.parse(String(init?.body)) as Record<string, unknown>) });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <SettingField meta={meta} />
      </QueryClientProvider>,
    );
  };

  it("an immediate control shows a visible Saved, and the first sentence of its help with the rest behind More", async () => {
    vi.useRealTimers();
    mount(flag, (b) => json({ settings: [{ ...flag, value: b["links.strip_tracking"] }], values: b }));
    expect(screen.getByText(/Removes tracking parameters\./)).toBeInTheDocument();
    expect(screen.queryByText(/stored article is not changed/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "More about Remove tracking from links" }));
    expect(screen.getByText(/stored article is not changed/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("switch"));
    expect(await screen.findByText("Saved")).toBeVisible();
  });

  it("a text box saves with Save, Revert puts the saved text back, and a refusal keeps what was typed with its reason", async () => {
    vi.useRealTimers();
    mount(text, () => json({ error: "invalid", keys: ["fetch.user_agent"], issues: [{ key: "fetch.user_agent", message: "Use one line of at most 200 characters." }] }, 400));
    const box = screen.getByRole("textbox", { name: "Custom browser identity" });
    expect(screen.getByRole("button", { name: "Save Custom browser identity" })).toBeDisabled();
    fireEvent.change(box, { target: { value: "abc" } });
    fireEvent.click(screen.getByRole("button", { name: "Revert Custom browser identity" }));
    expect(box).toHaveValue("");
    fireEvent.change(box, { target: { value: "abc" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Custom browser identity" }));
    expect(await screen.findByText("Use one line of at most 200 characters.")).toBeInTheDocument();
    expect(box).toHaveValue("abc");
    // Typing again clears the old message.
    fireEvent.change(box, { target: { value: "abcd" } });
    expect(screen.queryByText("Use one line of at most 200 characters.")).toBeNull();
  });
});
