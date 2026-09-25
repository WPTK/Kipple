import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, expect } from "vitest";
import * as axeMatchers from "vitest-axe/matchers";

expect.extend(axeMatchers);

afterEach(() => {
  cleanup();
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});

// jsdom has no layout: give the virtualizer a viewport, and stub the rest.
Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 800 });
Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, value: 375 });
Element.prototype.getBoundingClientRect = function () {
  return { x: 0, y: 0, top: 0, left: 0, right: 375, bottom: 800, width: 375, height: 800, toJSON() {} } as DOMRect;
};
Element.prototype.scrollTo = function () {};
Element.prototype.scrollIntoView = function () {};

class RO {
  constructor(private cb: ResizeObserverCallback) {}
  observe(el: Element) {
    this.cb([{ target: el, contentRect: el.getBoundingClientRect(), borderBoxSize: [{ inlineSize: 375, blockSize: 800 }] } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver);
  }
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver = RO as unknown as typeof ResizeObserver;

if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}
