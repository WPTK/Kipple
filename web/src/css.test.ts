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

describe("top safe-area inset", () => {
  it("is taken once per column: banners after the first, and the screen header under them, drop it", () => {
    const r = rules().find((x) => x.sel.includes(".kp-stack > .pt-safe ~ .pt-safe"));
    expect(r?.sel).toMatch(/\.kp-stack > \.pt-safe ~ \.pt-safe,\s*\.kp-stack > \.pt-safe ~ main \.pt-safe$/);
    expect(r?.body).toMatch(/padding-top:\s*0;/);
  });
});

describe("hit-row tap target", () => {
  it("is 44px on coarse pointers", () => {
    expect(css).toMatch(/@media \(pointer: coarse\) \{\s*\.hit-row \{ min-width: 44px; min-height: 44px; \}/);
  });
});

describe("status-bar cover", () => {
  it("is a solid, fixed, touch-through strip the height of the notch inset, in the page background", () => {
    const r = rules().find((x) => x.sel.endsWith("#kp-top-cover"));
    expect(r?.body).toMatch(/position:\s*fixed;/);
    expect(r?.body).toMatch(/top:\s*0;/);
    expect(r?.body).toMatch(/height:\s*env\(safe-area-inset-top\);/);
    expect(r?.body).toMatch(/background:\s*var\(--kp-bg\);/);
    expect(r?.body).toMatch(/pointer-events:\s*none;/);
    // below dialogs and menus (z-50)
    expect(Number(/z-index:\s*(\d+);/.exec(r?.body ?? "")?.[1])).toBeLessThan(50);
  });

  it("is in the page before the app root, hidden from assistive technology", () => {
    const html = readFileSync("index.html", "utf8");
    expect(html).toMatch(/<div id="kp-top-cover" aria-hidden="true"><\/div>\s*<div id="root">/);
  });
});
