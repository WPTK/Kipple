import { describe, expect, it, vi } from "vitest";
import { remeasureMounted } from "./remeasure";

describe("remeasureMounted", () => {
  it("rebuilds the offsets before it measures each mounted row", () => {
    const scroller = document.createElement("div");
    scroller.innerHTML = '<div data-index="0"></div><div data-index="1"></div><div class="other"></div>';
    const calls: string[] = [];
    const v = {
      measure: vi.fn(() => calls.push("measure")),
      getVirtualItems: vi.fn(() => calls.push("rebuild")),
      measureElement: vi.fn((el: Element | null) => calls.push(`el${(el as HTMLElement | null)?.dataset.index}`)),
    };
    remeasureMounted(v, scroller);
    expect(calls).toEqual(["measure", "rebuild", "el0", "el1"]);
  });

  it("copes with no scroll element yet", () => {
    const v = { measure: vi.fn(), getVirtualItems: vi.fn(), measureElement: vi.fn() };
    remeasureMounted(v, null);
    expect(v.measure).toHaveBeenCalledOnce();
    expect(v.measureElement).not.toHaveBeenCalled();
  });
});
