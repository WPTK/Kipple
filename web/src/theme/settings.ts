import { DEFAULT_DAY, DEFAULT_NIGHT, isSchemeId } from "./schemes.ts";

/**
 * How the theme is chosen on this device. "follow" uses the OS light/dark preference, "schedule" switches
 * between the same day and night picks at two fixed times of day (the device's local clock), "fixed" is one scheme.
 */
export type ThemeMode = "follow" | "fixed" | "schedule";

/** Per-device appearance choice. */
export interface ThemeSettings {
  mode: ThemeMode;
  /** Used when mode is "fixed". */
  fixed: string;
  /** Day theme for follow-system and the schedule. Any scheme may be chosen. */
  day: string;
  /** Night theme for follow-system and the schedule. Any scheme may be chosen. */
  night: string;
  /** Schedule: local time ("HH:MM", 24-hour) the night theme starts. */
  nightStart: string;
  /** Schedule: local time ("HH:MM", 24-hour) the day theme starts again. */
  dayStart: string;
}

export const THEME_STORAGE_KEY = "kipple.theme.v1";

export const DEFAULT_NIGHT_START = "21:00";
export const DEFAULT_DAY_START = "07:00";

export const DEFAULT_THEME_SETTINGS: ThemeSettings = {
  mode: "follow",
  fixed: DEFAULT_DAY,
  day: DEFAULT_DAY,
  night: DEFAULT_NIGHT,
  nightStart: DEFAULT_NIGHT_START,
  dayStart: DEFAULT_DAY_START,
};

/** "HH:MM", 00:00 to 23:59: the value of an `<input type="time">` without seconds. The boot script uses the same pattern. */
export const CLOCK_TIME_PATTERN = "^([01][0-9]|2[0-3]):[0-5][0-9]$";
const CLOCK_TIME = new RegExp(CLOCK_TIME_PATTERN);

export function isClockTime(v: unknown): v is string {
  return typeof v === "string" && CLOCK_TIME.test(v);
}

/**
 * A time input's value as "HH:MM", or null when it is not a complete time. Some browsers report seconds
 * ("22:30:00" or "22:30:00.000"); they are dropped, since the schedule works in whole minutes.
 */
export function toClockTime(v: string): string | null {
  const t = /^\d\d:\d\d:\d\d(\.\d{1,3})?$/.test(v) ? v.slice(0, 5) : v;
  return isClockTime(t) ? t : null;
}

/** Minutes since midnight for a valid "HH:MM". */
export function clockMinutes(t: string): number {
  return Number(t.slice(0, 2)) * 60 + Number(t.slice(3, 5));
}

/** Minutes since local midnight, 0 to 1439. */
export function minutesOfDay(d: Date): number {
  return d.getHours() * 60 + d.getMinutes();
}

/**
 * Pure: is `minutes` (since local midnight) inside the night window? Night runs from `nightStart` (inclusive) to
 * `dayStart` (exclusive). When night starts later in the day than day does (21:00 to 07:00) the window wraps past
 * midnight. Equal times mean there is no night window: the day theme stays.
 */
export function isNightAt(nightStart: string, dayStart: string, minutes: number): boolean {
  const n = clockMinutes(nightStart);
  const d = clockMinutes(dayStart);
  if (n === d) return false;
  return n < d ? minutes >= n && minutes < d : minutes >= n || minutes < d;
}

/**
 * Pure: which scheme id is showing for these settings, the OS appearance and the local time of day (minutes since
 * midnight; only the schedule reads it).
 */
export function resolveTheme(s: ThemeSettings, prefersDark: boolean, minutes: number): string {
  if (s.mode === "fixed") return s.fixed;
  if (s.mode === "schedule") return isNightAt(s.nightStart, s.dayStart, minutes) ? s.night : s.day;
  return prefersDark ? s.night : s.day;
}

/**
 * Milliseconds from `now` to the schedule's next switch (the next nightStart or dayStart on the local clock), at
 * least 1. Built with setHours so a daylight-saving change on the way is counted in wall-clock time.
 */
export function msUntilNextSwitch(s: Pick<ThemeSettings, "nightStart" | "dayStart">, now: Date): number {
  let best = Infinity;
  for (const t of [s.nightStart, s.dayStart]) {
    const m = clockMinutes(t);
    const at = new Date(now.getTime());
    at.setHours(Math.floor(m / 60), m % 60, 0, 0);
    if (at.getTime() <= now.getTime()) {
      at.setDate(at.getDate() + 1);
      at.setHours(Math.floor(m / 60), m % 60, 0, 0);
    }
    best = Math.min(best, at.getTime() - now.getTime());
  }
  return Math.max(1, best);
}

/**
 * The format of the theme cache. Version 2 added the schedule (the mode "schedule" and its two times); a cache
 * without `v` was written by an older build, which cannot hold the schedule and reads it as follow-system.
 */
export const THEME_CACHE_VERSION = 2;

/** The cache format version of a parsed theme cache (1 for one written before the field existed). */
export function themeCacheVersion(v: unknown): number {
  const n = typeof v === "object" && v !== null ? (v as { v?: unknown }).v : undefined;
  return typeof n === "number" && Number.isInteger(n) && n > 0 ? n : 1;
}

/**
 * The choice held by a parsed cache, with anything missing or unreadable taken from `base` (the defaults, or for a
 * cache another tab just wrote, this tab's current choice: a tab still on an older build writes the cache without
 * the schedule's times, and they must not be reset by that).
 */
export function themeSettingsFrom(raw: unknown, base: ThemeSettings = DEFAULT_THEME_SETTINGS): ThemeSettings {
  const v = (typeof raw === "object" && raw !== null ? raw : {}) as Partial<ThemeSettings>;
  const d = base;
  return {
    mode: v.mode === "fixed" || v.mode === "schedule" || v.mode === "follow" ? v.mode : d.mode,
    fixed: isSchemeId(v.fixed) ? v.fixed : d.fixed,
    day: isSchemeId(v.day) ? v.day : d.day,
    night: isSchemeId(v.night) ? v.night : d.night,
    nightStart: isClockTime(v.nightStart) ? v.nightStart : d.nightStart,
    dayStart: isClockTime(v.dayStart) ? v.dayStart : d.dayStart,
  };
}

/** JSON text, or undefined when it is missing or not JSON. */
export function parseJson(raw: string | null): unknown {
  if (!raw) return undefined;
  try {
    return JSON.parse(raw) as unknown;
  } catch {
    return undefined;
  }
}

/** The stored choice (see themeSettingsFrom), from the cache's JSON text. */
export function parseThemeSettings(raw: string | null, base: ThemeSettings = DEFAULT_THEME_SETTINGS): ThemeSettings {
  return themeSettingsFrom(parseJson(raw), base);
}

export function loadThemeSettings(): ThemeSettings {
  try {
    return parseThemeSettings(localStorage.getItem(THEME_STORAGE_KEY));
  } catch {
    return { ...DEFAULT_THEME_SETTINGS };
  }
}

export function saveThemeSettings(s: ThemeSettings): void {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ ...s, v: THEME_CACHE_VERSION }));
  } catch {
    /* private mode or blocked storage: the choice just won't persist */
  }
}
