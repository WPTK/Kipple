import { readFileSync } from "node:fs";
import { describe, expect, it, beforeEach } from "vitest";
import { contrast, deltaE, mixHex } from "./contrast";
import { SCHEMES, schemeById } from "./schemes";
import { DEFAULT_THEME_SETTINGS, parseThemeSettings, resolveTheme, THEME_STORAGE_KEY, type ThemeSettings } from "./settings";
import { bootScript, themesCss } from "./css";
import { applyTheme } from "./theme";

describe("resolveTheme", () => {
  it("follow-system defaults to Paper by day and Midnight by night", () => {
    expect(resolveTheme(DEFAULT_THEME_SETTINGS, false)).toBe("paper");
    expect(resolveTheme(DEFAULT_THEME_SETTINGS, true)).toBe("midnight");
  });

  it("custom pair: any scheme may be day or night, regardless of kind", () => {
    const s: ThemeSettings = { mode: "follow", fixed: "paper", day: "inkwell", night: "linen" };
    expect(resolveTheme(s, false)).toBe("inkwell"); // a dark scheme by day
    expect(resolveTheme(s, true)).toBe("linen"); // a light scheme by night
  });

  it("fixed mode ignores the OS setting", () => {
    const s: ThemeSettings = { mode: "fixed", fixed: "fountain", day: "paper", night: "midnight" };
    expect(resolveTheme(s, false)).toBe("fountain");
    expect(resolveTheme(s, true)).toBe("fountain");
  });
});

describe("parseThemeSettings", () => {
  it("falls back to defaults for junk and unknown ids", () => {
    expect(parseThemeSettings(null)).toEqual(DEFAULT_THEME_SETTINGS);
    expect(parseThemeSettings("{not json")).toEqual(DEFAULT_THEME_SETTINGS);
    expect(parseThemeSettings(JSON.stringify({ mode: "fixed", fixed: "fern", day: "nope", night: 3 }))).toEqual({
      ...DEFAULT_THEME_SETTINGS,
      mode: "fixed",
    });
  });
});

describe("scheme roster", () => {
  it("has the decided names and nothing retired", () => {
    const names = SCHEMES.map((s) => s.name);
    expect(names).toHaveLength(20);
    for (const n of ["Directory", "Newsprint", "Cocoa Kraft", "Cocoa Mid", "Foolscap", "Tracing", "Carbon", "Lamplight", "Inkwell", "Teletype", "Airmail", "Stationery", "Tissue", "Fountain", "Signal"]) {
      expect(names).toContain(n);
    }
    expect(names).not.toContain("Fern");
    expect(new Set(SCHEMES.map((s) => s.id)).size).toBe(SCHEMES.length);
  });

  it("every scheme passes WCAG AA (text 4.5, accent/unread/star 3)", () => {
    for (const s of SCHEMES) {
      const t = s.tokens;
      const text: [string, string, string][] = [
        ["text/bg", t.text, t.bg],
        ["text/surface", t.text, t.surface],
        ["text2/bg", t.text2, t.bg],
        ["text2/surface", t.text2, t.surface],
        ["link/bg", t.link, t.bg],
        ["link/surface", t.link, t.surface],
        ["danger/bg", t.danger, t.bg],
        ["danger/surface", t.danger, t.surface],
        ["text/selection", t.text, t.selection],
      ];
      for (const [label, fg, bg] of text) expect(contrast(fg, bg), `${s.name} ${label}`).toBeGreaterThanOrEqual(4.5);
      for (const [label, fg] of [["accent", t.accent], ["unread", t.unread], ["star", t.star]] as const) {
        expect(contrast(fg, t.bg), `${s.name} ${label}`).toBeGreaterThanOrEqual(3);
      }
    }
  });

  it("toasts: text on the accent-tinted toast surface is AA in every scheme, and the borders are visible", () => {
    // --kp-toast-bg in index.css is color-mix(in srgb, accent 18%, surface).
    for (const s of SCHEMES) {
      const t = s.tokens;
      const bg = mixHex(t.accent, t.surface, 18);
      expect(contrast(t.text, bg), `${s.name} toast text`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.accent, t.bg), `${s.name} toast border`).toBeGreaterThanOrEqual(3);
      expect(contrast(t.danger, t.bg), `${s.name} error toast border`).toBeGreaterThanOrEqual(3);
      // It must read as a toast: clearly different from the plain surface it floats over.
      expect(bg, `${s.name} toast surface`).not.toBe(t.surface);
    }
    expect(mixHex("#000000", "#ffffff", 50)).toBe("#808080");
  });

  it("highlighted keywords: the scheme's text on the star tint is AA in every scheme, and the underline is visible", () => {
    // The percentage is read from index.css so the test cannot drift from the stylesheet.
    const css = readFileSync("src/index.css", "utf8") // vitest runs from web/;
    const pct = Number(/--kp-hl-bg:\s*color-mix\(in srgb,\s*var\(--kp-star\)\s*(\d+)%,\s*var\(--kp-bg\)\)/.exec(css)?.[1]);
    expect(pct).toBeGreaterThan(0);
    for (const s of SCHEMES) {
      const t = s.tokens;
      const bg = mixHex(t.star, t.bg, pct);
      expect(contrast(t.text, bg), `${s.name} highlight text`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(t.star, t.bg), `${s.name} highlight underline`).toBeGreaterThanOrEqual(3);
      expect(bg, `${s.name} highlight tint`).not.toBe(t.bg);
    }
  });

  it("Signal's danger is no longer confusable with star under deuteranopia", () => {
    const s = schemeById("signal").tokens;
    expect(s.danger).toBe("#a0006a");
    expect(deltaE(s.star, s.danger, "deuteranopia")).toBeGreaterThan(15);
    expect(contrast(s.danger, s.bg)).toBeGreaterThanOrEqual(7);
  });

  it("Carbon and Fountain are visually far apart", () => {
    const c = schemeById("carbon").tokens;
    const f = schemeById("fountain").tokens;
    expect(deltaE(c.bg, f.bg)).toBeGreaterThan(25);
    expect(deltaE(c.accent, f.accent)).toBeGreaterThan(40);
    // Fountain: navy (blue dominant) with cream text; Carbon: neutral cool gray-black.
    const [, , fb] = [parseInt(f.bg.slice(1, 3), 16), parseInt(f.bg.slice(3, 5), 16), parseInt(f.bg.slice(5, 7), 16)];
    expect(fb).toBeGreaterThan(0x40);
    const [cr, cg, cb] = [1, 3, 5].map((i) => parseInt(c.bg.slice(i, i + 2), 16));
    expect(Math.max(cr!, cg!, cb!) - Math.min(cr!, cg!, cb!)).toBeLessThan(0x0c);
    // Both stay AA.
    expect(contrast(c.text, c.bg)).toBeGreaterThanOrEqual(4.5);
    expect(contrast(f.text, f.bg)).toBeGreaterThanOrEqual(4.5);
  });

  it("emits one CSS variable block per scheme", () => {
    const css = themesCss();
    for (const s of SCHEMES) expect(css).toContain(`:root[data-theme="${s.id}"]`);
    expect(css).toContain("--kp-danger: #a0006a");
  });
});

describe("meta theme-color and the boot script", () => {
  beforeEach(() => {
    document.head.innerHTML =
      '<meta name="theme-color" content="#fff" media="(prefers-color-scheme: light)"><meta name="theme-color" content="#000" media="(prefers-color-scheme: dark)">';
  });

  it("applyTheme collapses the fallbacks into one live tag and updates it", () => {
    applyTheme("cocoa-kraft");
    let metas = document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]');
    expect(metas).toHaveLength(1);
    expect(metas[0]?.content).toBe(schemeById("cocoa-kraft").tokens.meta);
    expect(metas[0]?.hasAttribute("media")).toBe(false);
    expect(document.documentElement.dataset.theme).toBe("cocoa-kraft");
    applyTheme("fountain");
    metas = document.querySelectorAll('meta[name="theme-color"]');
    expect(metas).toHaveLength(1);
    expect(metas[0]?.content).toBe("#13284f");
  });

  const cases: { stored: ThemeSettings | null; dark: boolean }[] = [
    { stored: null, dark: false },
    { stored: null, dark: true },
    { stored: { mode: "fixed", fixed: "teletype", day: "paper", night: "midnight" }, dark: false },
    { stored: { mode: "follow", fixed: "paper", day: "directory", night: "fountain" }, dark: false },
    { stored: { mode: "follow", fixed: "paper", day: "directory", night: "fountain" }, dark: true },
  ];
  it.each(cases)("boot script matches resolveTheme: %j", ({ stored, dark }) => {
    if (stored) localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify(stored));
    window.matchMedia = ((q: string) => ({ matches: dark && q.includes("dark"), media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
    document.documentElement.removeAttribute("data-theme");
    new Function(bootScript())();
    const want = resolveTheme(parseThemeSettings(localStorage.getItem(THEME_STORAGE_KEY)), dark);
    expect(document.documentElement.dataset.theme).toBe(want);
    const metas = document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]');
    expect(metas).toHaveLength(1);
    expect(metas[0]?.content).toBe(schemeById(want).tokens.meta);
  });

  it("boot script survives corrupt storage", () => {
    window.matchMedia = ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
    localStorage.setItem(THEME_STORAGE_KEY, "{{{");
    new Function(bootScript())();
    expect(document.documentElement.dataset.theme).toBe("paper");
  });
});
