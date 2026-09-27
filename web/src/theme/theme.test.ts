import { readFileSync } from "node:fs";
import { afterEach, describe, expect, it, beforeEach, vi } from "vitest";
import { contrast, deltaE, mixHex } from "./contrast";
import { SCHEMES, schemeById } from "./schemes";
import {
  DEFAULT_THEME_SETTINGS,
  isClockTime,
  isNightAt,
  toClockTime,
  msUntilNextSwitch,
  parseThemeSettings,
  resolveTheme,
  THEME_STORAGE_KEY,
  type ThemeSettings,
} from "./settings";
import { bootScript, themesCss } from "./css";
import { applyTheme, initTheme, SCHEDULE_RECHECK_MS, themeStore } from "./theme";

const at = (hhmm: string) => Number(hhmm.slice(0, 2)) * 60 + Number(hhmm.slice(3, 5));
const base: ThemeSettings = { ...DEFAULT_THEME_SETTINGS };

describe("resolveTheme", () => {
  it("follow-system defaults to Paper by day and Midnight by night", () => {
    expect(resolveTheme(DEFAULT_THEME_SETTINGS, false, 0)).toBe("paper");
    expect(resolveTheme(DEFAULT_THEME_SETTINGS, true, 0)).toBe("midnight");
  });

  it("custom pair: any scheme may be day or night, regardless of kind", () => {
    const s: ThemeSettings = { ...base, mode: "follow", day: "inkwell", night: "linen" };
    expect(resolveTheme(s, false, 0)).toBe("inkwell"); // a dark scheme by day
    expect(resolveTheme(s, true, 0)).toBe("linen"); // a light scheme by night
  });

  it("fixed mode ignores the OS setting", () => {
    const s: ThemeSettings = { ...base, mode: "fixed", fixed: "fountain" };
    expect(resolveTheme(s, false, 0)).toBe("fountain");
    expect(resolveTheme(s, true, 0)).toBe("fountain");
  });

  it("schedule mode follows the clock and ignores the OS setting", () => {
    const s: ThemeSettings = { ...base, mode: "schedule", day: "linen", night: "carbon", nightStart: "21:00", dayStart: "07:00" };
    for (const dark of [false, true]) {
      expect(resolveTheme(s, dark, at("12:00"))).toBe("linen");
      expect(resolveTheme(s, dark, at("23:30"))).toBe("carbon");
      expect(resolveTheme(s, dark, at("03:00"))).toBe("carbon");
    }
  });

  it("follow and fixed modes ignore the time of day", () => {
    for (const m of [0, at("12:00"), at("23:59")]) {
      expect(resolveTheme({ ...base, mode: "follow" }, false, m)).toBe("paper");
      expect(resolveTheme({ ...base, mode: "fixed", fixed: "tissue" }, true, m)).toBe("tissue");
    }
  });
});

describe("isNightAt", () => {
  it("a window that crosses midnight (21:00 to 07:00): starts inclusive, ends exclusive", () => {
    const night = (t: string) => isNightAt("21:00", "07:00", at(t));
    expect(night("20:59")).toBe(false);
    expect(night("21:00")).toBe(true);
    expect(night("23:59")).toBe(true);
    expect(night("00:00")).toBe(true);
    expect(night("06:59")).toBe(true);
    expect(night("07:00")).toBe(false);
    expect(night("12:00")).toBe(false);
  });

  it("a window inside one day (01:00 to 06:00)", () => {
    const night = (t: string) => isNightAt("01:00", "06:00", at(t));
    expect(night("00:59")).toBe(false);
    expect(night("01:00")).toBe(true);
    expect(night("05:59")).toBe(true);
    expect(night("06:00")).toBe(false);
    expect(night("23:00")).toBe(false);
  });

  it("night starting at midnight and day at midnight", () => {
    expect(isNightAt("00:00", "07:00", at("00:00"))).toBe(true);
    expect(isNightAt("00:00", "07:00", at("23:59"))).toBe(false);
    expect(isNightAt("20:00", "00:00", at("23:59"))).toBe(true);
    expect(isNightAt("20:00", "00:00", at("00:00"))).toBe(false);
    expect(isNightAt("20:00", "00:00", at("19:59"))).toBe(false);
  });

  it("equal times mean no night: the day theme stays all day", () => {
    for (const t of ["00:00", "08:00", "08:01", "23:59"]) expect(isNightAt("08:00", "08:00", at(t))).toBe(false);
  });

  it("a one-minute night", () => {
    expect(isNightAt("23:59", "00:00", at("23:59"))).toBe(true);
    expect(isNightAt("23:59", "00:00", at("00:00"))).toBe(false);
    expect(isNightAt("23:59", "00:00", at("23:58"))).toBe(false);
  });
});

describe("isClockTime", () => {
  it("accepts 24-hour HH:MM from 00:00 to 23:59 only", () => {
    for (const t of ["00:00", "07:05", "19:30", "23:59"]) expect(isClockTime(t)).toBe(true);
    for (const t of ["24:00", "7:00", "07:60", "07:00:00", "0700", "", " 07:00", "ab:cd", 700, null, undefined]) expect(isClockTime(t)).toBe(false);
  });
});

describe("toClockTime", () => {
  it("drops seconds a browser may report, and refuses anything incomplete", () => {
    expect(toClockTime("22:30")).toBe("22:30");
    expect(toClockTime("22:30:00")).toBe("22:30");
    expect(toClockTime("22:30:59.123")).toBe("22:30");
    for (const v of ["", "2:30", "24:00:00", "22:3", "22:30:0", "22:30pm"]) expect(toClockTime(v)).toBeNull();
  });
});

describe("msUntilNextSwitch", () => {
  const s = { nightStart: "21:00", dayStart: "07:00" };
  const local = (h: number, m: number, sec = 0) => new Date(2026, 8, 27, h, m, sec);

  it("counts to the nearer of the two switches", () => {
    expect(msUntilNextSwitch(s, local(12, 0))).toBe(9 * 3600_000);
    expect(msUntilNextSwitch(s, local(22, 0))).toBe(9 * 3600_000); // to 07:00 tomorrow
    expect(msUntilNextSwitch(s, local(6, 59, 30))).toBe(30_000);
  });

  it("a switch exactly now counts as passed: the next one is the other boundary", () => {
    expect(msUntilNextSwitch(s, local(21, 0))).toBe(10 * 3600_000);
    expect(msUntilNextSwitch(s, local(7, 0))).toBe(14 * 3600_000);
  });

  it("equal times: one day away at most, never zero", () => {
    const same = { nightStart: "08:00", dayStart: "08:00" };
    expect(msUntilNextSwitch(same, local(8, 0))).toBe(24 * 3600_000);
    expect(msUntilNextSwitch(same, local(7, 0))).toBe(3600_000);
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
    expect(parseThemeSettings(JSON.stringify({ mode: "sunset", nightStart: "25:00", dayStart: 7 }))).toEqual(DEFAULT_THEME_SETTINGS);
  });

  it("fills missing or unreadable fields from a base, not the defaults", () => {
    const mine: ThemeSettings = { ...base, mode: "schedule", nightStart: "22:15", dayStart: "06:45" };
    // What an older build writes: no times.
    expect(parseThemeSettings(JSON.stringify({ mode: "follow", fixed: "paper", day: "linen", night: "carbon" }), mine)).toEqual({
      ...mine,
      mode: "follow",
      day: "linen",
      night: "carbon",
    });
    expect(parseThemeSettings("{{{", mine)).toEqual(mine);
    expect(parseThemeSettings(JSON.stringify({ mode: "sunset" }), mine).mode).toBe("schedule");
  });

  it("keeps a schedule and its times; a cache from before the schedule gets the default times", () => {
    expect(parseThemeSettings(JSON.stringify({ ...base, mode: "schedule", nightStart: "22:15", dayStart: "06:45" }))).toEqual({
      ...base,
      mode: "schedule",
      nightStart: "22:15",
      dayStart: "06:45",
    });
    expect(parseThemeSettings(JSON.stringify({ mode: "follow", fixed: "paper", day: "linen", night: "carbon" }))).toEqual({
      ...base,
      day: "linen",
      night: "carbon",
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

  afterEach(() => {
    vi.useRealTimers();
    localStorage.clear();
  });

  const sched = (nightStart: string, dayStart: string): ThemeSettings => ({ ...base, mode: "schedule", day: "linen", night: "carbon", nightStart, dayStart });
  const cases: { stored: ThemeSettings | object | null; dark: boolean; clock?: string }[] = [
    { stored: null, dark: false },
    { stored: null, dark: true },
    { stored: { ...base, mode: "fixed", fixed: "teletype" }, dark: false },
    { stored: { ...base, mode: "follow", day: "directory", night: "fountain" }, dark: false },
    { stored: { ...base, mode: "follow", day: "directory", night: "fountain" }, dark: true },
    // A cache written before the schedule existed (no times).
    { stored: { mode: "follow", fixed: "paper", day: "directory", night: "fountain" }, dark: true },
    { stored: sched("21:00", "07:00"), dark: true, clock: "12:00" },
    { stored: sched("21:00", "07:00"), dark: false, clock: "21:00" },
    { stored: sched("21:00", "07:00"), dark: false, clock: "20:59" },
    { stored: sched("21:00", "07:00"), dark: false, clock: "00:00" },
    { stored: sched("21:00", "07:00"), dark: false, clock: "06:59" },
    { stored: sched("21:00", "07:00"), dark: true, clock: "07:00" },
    { stored: sched("01:00", "06:00"), dark: false, clock: "03:00" },
    { stored: sched("01:00", "06:00"), dark: false, clock: "23:00" },
    { stored: sched("08:00", "08:00"), dark: false, clock: "08:00" },
    // Bad times fall back to the defaults (21:00 to 07:00) in both.
    { stored: { ...sched("9pm", "25:00") }, dark: false, clock: "22:00" },
    { stored: { ...sched("9pm", "25:00") }, dark: false, clock: "08:00" },
  ];
  it.each(cases)("boot script matches resolveTheme: %j", ({ stored, dark, clock }) => {
    // The clock is always pinned, so the boot script and resolveTheme read the same minute.
    const now = clock ?? "12:00";
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 8, 27, Number(now.slice(0, 2)), Number(now.slice(3, 5)), 30));
    if (stored) localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify(stored));
    window.matchMedia = ((q: string) => ({ matches: dark && q.includes("dark"), media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
    document.documentElement.removeAttribute("data-theme");
    new Function(bootScript())();
    const want = resolveTheme(parseThemeSettings(localStorage.getItem(THEME_STORAGE_KEY)), dark, at(now));
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

describe("initTheme on a schedule", () => {
  let stop: (() => void) | undefined;
  beforeEach(() => {
    vi.useFakeTimers();
    window.matchMedia = ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
  });
  afterEach(() => {
    stop?.();
    stop = undefined;
    themeStore.set({ ...DEFAULT_THEME_SETTINGS });
    vi.useRealTimers();
    localStorage.clear();
  });
  const theme = () => document.documentElement.dataset.theme;

  it("switches to the night theme at the start time and back at the day time, with no reload", () => {
    vi.setSystemTime(new Date(2026, 8, 27, 20, 58));
    themeStore.set({ ...base, mode: "schedule", day: "linen", night: "carbon", nightStart: "21:00", dayStart: "07:00" });
    stop = initTheme();
    expect(theme()).toBe("linen");
    vi.advanceTimersByTime(60_000);
    expect(theme()).toBe("linen"); // 20:59
    vi.advanceTimersByTime(61_000);
    expect(theme()).toBe("carbon"); // just past 21:00
    vi.advanceTimersByTime(10 * 3600_000);
    expect(theme()).toBe("linen"); // past 07:00 the next morning
  });

  it("re-arms when the times change, and stops when the mode leaves the schedule", () => {
    vi.setSystemTime(new Date(2026, 8, 27, 12, 0));
    themeStore.set({ ...base, mode: "schedule", day: "linen", night: "carbon" });
    stop = initTheme();
    expect(theme()).toBe("linen");
    themeStore.set((s) => ({ ...s, nightStart: "11:00", dayStart: "13:00" }));
    expect(theme()).toBe("carbon");
    themeStore.set((s) => ({ ...s, mode: "follow" }));
    expect(theme()).toBe("linen");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("equal times arm no timer (nothing ever switches)", () => {
    vi.setSystemTime(new Date(2026, 8, 27, 12, 0));
    themeStore.set({ ...base, mode: "schedule", day: "linen", night: "carbon", nightStart: "08:00", dayStart: "08:00" });
    stop = initTheme();
    expect(theme()).toBe("linen");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("focus with nothing to change leaves the theme alone but re-arms from the clock", () => {
    vi.setSystemTime(new Date(2026, 8, 27, 20, 50));
    themeStore.set({ ...base, mode: "schedule", day: "linen", night: "carbon", nightStart: "21:00", dayStart: "07:00" });
    stop = initTheme();
    // A meta tag the re-apply would replace the content of; it must stay untouched.
    const meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')!;
    meta.content = "#123456";
    // The device slept with ten minutes left on the timer and wakes ten seconds before the switch.
    vi.setSystemTime(new Date(2026, 8, 27, 20, 59, 50));
    window.dispatchEvent(new Event("focus"));
    expect(theme()).toBe("linen");
    expect(meta.content).toBe("#123456");
    expect(vi.getTimerCount()).toBe(1);
    vi.advanceTimersByTime(11_000);
    expect(theme()).toBe("carbon");
  });

  it("never sleeps longer than the recheck interval, and a tab coming back re-checks the clock", () => {
    vi.setSystemTime(new Date(2026, 8, 27, 8, 0));
    themeStore.set({ ...base, mode: "schedule", day: "linen", night: "carbon", nightStart: "21:00", dayStart: "07:00" });
    stop = initTheme();
    expect(vi.getTimerCount()).toBe(1);
    expect(SCHEDULE_RECHECK_MS).toBeLessThanOrEqual(15 * 60_000);
    // The device slept: the clock jumps past 21:00 without the timer firing.
    vi.setSystemTime(new Date(2026, 8, 27, 22, 0));
    expect(theme()).toBe("linen");
    window.dispatchEvent(new Event("focus"));
    expect(theme()).toBe("carbon");
  });
});