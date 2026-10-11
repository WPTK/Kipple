import { describe, expect, it } from "vitest";

// A ref refreshed in a passive effect (`useEffect(() => { ref.current = x })`, no dependency list) is stale until
// the browser has run the effect, which React may defer past the next key press or pointer event. useLatestRef
// refreshes in a layout effect; this keeps the passive form from coming back.
const sources = import.meta.glob("../**/*.{ts,tsx}", { query: "?raw", import: "default", eager: true }) as Record<string, string>;

const PASSIVE_REF_SYNC = /useEffect\(\(\) => \{\s*\w+(?:\.\w+)*\.current = [^;]+;\s*\}\);/;

describe("latest-value refs", () => {
  it("sees the source files", () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50);
  });

  it("are refreshed by useLatestRef, never in a passive effect", () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => !path.endsWith(".test.ts") && !path.endsWith(".test.tsx"))
      .filter(([, src]) => PASSIVE_REF_SYNC.test(src))
      .map(([path]) => path);
    expect(offenders).toEqual([]);
  });

  it("the pattern matches what it is meant to catch", () => {
    expect(PASSIVE_REF_SYNC.test("useEffect(() => {\n  ref.current = { a, b };\n});")).toBe(true);
    expect(PASSIVE_REF_SYNC.test("useEffect(() => {\n  ref.current = v;\n}, [v]);")).toBe(false);
  });
});
