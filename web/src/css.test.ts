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

describe("hit-pad tap target", () => {
  it("grows a text link to a 24px box with a mouse and 44px on coarse pointers, without moving the text", () => {
    const all = rules().filter((x) => x.sel === ".hit-pad::before");
    expect(all).toHaveLength(2);
    const [base, coarse] = all;
    expect(base?.body).toMatch(/width:\s*max\(100%, 24px\);/);
    expect(base?.body).toMatch(/height:\s*max\(100%, 24px\);/);
    expect(base?.body).toMatch(/position:\s*absolute;/);
    expect(css).toMatch(/\.hit-pad \{ position: relative; \}/);
    expect(css).toMatch(/@media \(pointer: coarse\) \{\s*\.hit-pad::before/);
    expect(coarse?.body).toMatch(/width:\s*max\(100%, 44px\);/);
    expect(coarse?.body).toMatch(/height:\s*max\(100%, 44px\);/);
  });
});

describe("choice rows keep a 44px touch size", () => {
  const read = (p: string) => readFileSync(p, "utf8");
  it("segmented choices are 44px wide at least and wrap rather than squeeze", () => {
    const src = read("src/ui/segmented.tsx");
    expect(src).toMatch(/min-h-11 min-w-11 flex-1/);
    expect(src).toMatch(/flex w-full flex-wrap/);
  });
  it("the layout menu's descriptions wrap instead of being cut off", () => {
    const src = read("src/screens/LayoutMenu.tsx");
    const radio = src.slice(src.indexOf("function Radio"));
    expect(radio).not.toContain("truncate");
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

describe("forced colors", () => {
  it("the selected card's muted lines follow HighlightText", () => {
    expect(css).toMatch(/label:has\(input:checked\) \* \{ color: HighlightText; \}/);
  });
});

describe("long words", () => {
  it("the article body breaks a long word only where it would overflow, so table cells keep their words whole (#477)", () => {
    // A rule's selector text carries the comment above it, so match its end.
    const r = rules().find((x) => x.sel.endsWith("\n.article-body"));
    expect(r?.body).toMatch(/overflow-wrap:\s*break-word;/);
    expect(r?.body).not.toMatch(/overflow-wrap:\s*anywhere/);
  });

  it("a list title's long word wraps in every layout (#478)", () => {
    expect(rules().find((x) => x.sel.endsWith("\n.row-link"))?.body).toMatch(/overflow-wrap:\s*anywhere;/);
  });
});
