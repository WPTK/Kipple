import { FINGER_ONLY_QUERY, RAIL_QUERY, WIDE_QUERY } from "@/lib/useMedia";
import { vi } from "vitest";

/** Stubs matchMedia so the wide layout (the sidebar breakpoint) is on. Undo with vi.unstubAllGlobals(). */
export function wide(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes(WIDE_QUERY),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

/** Stubs matchMedia for a window of the three-pane layout under 1100 px wide: the sidebar is an icon rail until opened. Undo with vi.unstubAllGlobals(). */
export function rail(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes(WIDE_QUERY) || query === RAIL_QUERY,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

/** Stubs matchMedia as a phone with only a finger: no mouse, trackpad or pen (FINGER_ONLY_QUERY matches). Undo with vi.unstubAllGlobals(). */
export function fingerOnly(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === FINGER_ONLY_QUERY,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}
