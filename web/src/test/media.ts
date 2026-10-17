import { vi } from "vitest";

/** Stubs matchMedia so the wide layout (min-width 900px) is on. Undo with vi.unstubAllGlobals(). */
export function wide(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes("min-width: 900px"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}
