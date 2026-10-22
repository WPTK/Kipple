import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AUTO_RETRY_MS, LoadFailed } from "./LoadFailed";

describe("LoadFailed", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it.each([
    ["offline", "You're offline"],
    ["unreachable", "Kipple can't be reached"],
    ["server", "Kipple's server had a problem"],
  ] as const)("names the %s cause and drops the log advice", (cause, title) => {
    render(<LoadFailed cause={cause} busy={false} onRetry={() => {}} />);
    expect(screen.getByRole("heading", { level: 1, name: title })).toBeInTheDocument();
    expect(screen.queryByText(/logs/i)).toBeNull();
  });

  it("asks again by itself, quietly, and says so while it does", () => {
    const retry = vi.fn();
    const { rerender } = render(<LoadFailed cause="unreachable" busy={false} onRetry={retry} />);
    expect(retry).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(AUTO_RETRY_MS));
    expect(retry).toHaveBeenCalledTimes(1);
    rerender(<LoadFailed cause="unreachable" busy onRetry={retry} />);
    expect(screen.getByText("Trying again")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeDisabled();
  });
});
