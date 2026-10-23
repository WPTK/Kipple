import { vi } from "vitest";

/** Stubs matchMedia so the wide layout (the sidebar breakpoint) is on. Undo with vi.unstubAllGlobals(). */
export function wide(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes("min-width: 820px"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}
