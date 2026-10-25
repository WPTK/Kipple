import { describe, expect, it, vi } from "vitest";
import { MAX_LIST_IMAGES, SLOT_HOLD_MS, takeImageSlot } from "./imageSlots";

describe("takeImageSlot", () => {
  it("runs at most MAX_LIST_IMAGES at once and starts the next when one is given back", () => {
    const started: number[] = [];
    const releases = Array.from({ length: MAX_LIST_IMAGES + 2 }, (_, i) => takeImageSlot(() => started.push(i)));
    expect(started).toEqual([0, 1, 2].slice(0, MAX_LIST_IMAGES));
    releases[0]!();
    expect(started).toHaveLength(MAX_LIST_IMAGES + 1);
    releases[0]!(); // giving back twice changes nothing
    expect(started).toHaveLength(MAX_LIST_IMAGES + 1);
    releases[1]!();
    expect(started).toHaveLength(MAX_LIST_IMAGES + 2); // each place given back starts the next waiting one
    releases.forEach((r) => r());
  });

  it("a withdrawn request never runs", () => {
    const held = Array.from({ length: MAX_LIST_IMAGES }, () => takeImageSlot(() => undefined));
    const go = vi.fn();
    const withdraw = takeImageSlot(go);
    withdraw();
    held.forEach((r) => r());
    expect(go).not.toHaveBeenCalled();
  });

  it("gives a hung picture's place back after SLOT_HOLD_MS", () => {
    vi.useFakeTimers();
    try {
      const held = Array.from({ length: MAX_LIST_IMAGES }, () => takeImageSlot(() => undefined));
      const go = vi.fn();
      takeImageSlot(go);
      vi.advanceTimersByTime(SLOT_HOLD_MS - 1);
      expect(go).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1);
      expect(go).toHaveBeenCalledTimes(1);
      held.forEach((r) => r());
    } finally {
      vi.useRealTimers();
    }
  });
});
