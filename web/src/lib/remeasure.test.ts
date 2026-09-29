import { describe, expect, it, vi } from "vitest";
import { remeasureMounted } from "./remeasure";

function fakeVirtualizer(calls: string[] = []) {
  return {
    measure: vi.fn(() => void calls.push("measure")),
    getVirtualItems: vi.fn(() => void calls.push("rebuild")),
    indexFromElement: (el: Element) => Number((el as HTMLElement).dataset.index),
    resizeItem: vi.fn((i: number, size: number) => void calls.push(`el${i}=${size}`)),
  };
}

describe("remeasureMounted", () => {
  it("rebuilds the offsets before it measures each mounted row", () => {
    const scroller = document.createElement("div");
    scroller.innerHTML = '<div data-index="0"></div><div data-index="1"></div><div class="other"></div>';
    const calls: string[] = [];
    remeasureMounted(fakeVirtualizer(calls), scroller);
    // setup.ts gives every element an offsetHeight of 800.
    expect(calls).toEqual(["measure", "rebuild", "el0=800", "el1=800"]);
  });

  it("hands each row's height to the virtualizer directly, so a list that has just scrolled is measured too", () => {
    // measureElement ignores rows while the virtualizer believes the list is scrolling, which it does right after a
    // restored offset: the rows kept their estimated heights and stood apart with gaps (#94).
    const scroller = document.createElement("div");
    scroller.innerHTML = '<div data-index="3"></div>';
    const v = { ...fakeVirtualizer(), measureElement: vi.fn() };
    remeasureMounted(v, scroller);
    expect(v.resizeItem).toHaveBeenCalledWith(3, 800);
    expect(v.measureElement).not.toHaveBeenCalled();
  });

  it("copes with no scroll element yet", () => {
    const v = fakeVirtualizer();
    remeasureMounted(v, null);
    expect(v.measure).toHaveBeenCalledOnce();
    expect(v.resizeItem).not.toHaveBeenCalled();
  });
});
