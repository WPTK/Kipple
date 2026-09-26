import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Vitest runs with css:false, so the stylesheet is checked as text.
const css = readFileSync("src/index.css", "utf8");

function rules(): { sel: string; body: string }[] {
  return [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1]!.trim(), body: m[2]! }));
}

describe("article headings", () => {
  for (const h of ["h1", "h2", "h3", "h4", "h5", "h6"]) {
    it(`${h} has a non-zero size`, () => {
      const sel = `.article-body ${h}`;
      const matching = rules().filter((r) => r.sel.split(",").some((s) => s.trim() === sel));
      const sizes = matching.flatMap((r) => [...r.body.matchAll(/font-size:\s*([^;]+);/g)].map((m) => m[1]!.trim()));
      expect(sizes.length).toBeGreaterThan(0);
      expect(sizes.at(-1)).toMatch(/^0?\.\d+em$|^[1-9]\d*(\.\d+)?em$/);
      expect(parseFloat(sizes.at(-1)!)).toBeGreaterThan(0);
    });
  }
});

describe("hit-row tap target", () => {
  it("is 44px on coarse pointers", () => {
    expect(css).toMatch(/@media \(pointer: coarse\) \{\s*\.hit-row \{ min-width: 44px; min-height: 44px; \}/);
  });
});
