import { DEFAULT_DAY, DEFAULT_NIGHT, isSchemeId } from "./schemes.ts";

/**
 * Per-device appearance choice. `mode: "follow"` shows the day or night pick: by the OS light/dark preference, or,
 * with `schedule` on, by two fixed times of day on the device's local clock. `mode: "fixed"` is one scheme.
 *
 * The schedule is a flag under follow rather than a third mode so a build that predates it passes it through: such a
 * build reads the choice as follow-system and never writes the flag (a missing field keeps its value, see
 * themeSettingsFrom), and the server holds it as its own key (`ui.theme_schedule`) next to `ui.theme: "system"`.
 */
export interface ThemeSettings {
  mode: "follow" | "fixed";
  /** Used when mode is "fixed". */
  fixed: string;
  /** Day theme for follow-system and the schedule. Any scheme may be chosen. */
  day: string;
  /** Night theme for follow-system and the schedule. Any scheme may be chosen. */
  night: string;
  /** With mode "follow": switch at nightStart and dayStart instead of following the OS. */
  schedule: boolean;
  /** Schedule: local time ("HH:MM", 24-hour) the night theme starts. */
  nightStart: string;
  /** Schedule: local time ("HH:MM", 24-hour) the day theme starts again. */
  dayStart: string;
}

/** The three choices the pickers offer. */
export type ThemeChoice = "follow" | "schedule" | "fixed";

export function themeChoice(s: ThemeSettings): ThemeChoice {
  return s.mode === "fixed" ? "fixed" : s.schedule ? "schedule" : "follow";
}

/** The settings change for picking Follow system or On a schedule (the day and night picks and the times are kept). */
export function choosePair(choice: "follow" | "schedule"): Partial<ThemeSettings> {
  return { mode: "follow", schedule: choice === "schedule" };
}

/** Whether the scheduled switch is in force. */
export function onSchedule(s: ThemeSettings): boolean {
  return themeChoice(s) === "schedule";
}

export const THEME_STORAGE_KEY = "kipple.theme.v1";

export const DEFAULT_NIGHT_START = "21:00";
export const DEFAULT_DAY_START = "07:00";

export const DEFAULT_THEME_SETTINGS: ThemeSettings = {
  mode: "follow",
  fixed: DEFAULT_DAY,
  day: DEFAULT_DAY,
  night: DEFAULT_NIGHT,
  schedule: false,
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
  const night = s.schedule ? isNightAt(s.nightStart, s.dayStart, minutes) : prefersDark;
  return night ? s.night : s.day;
}

/**
 * Milliseconds from `now` to the schedule's next switch (the next nightStart or dayStart on the local clock), at
 * least 1. Built with setHours so a daylight-saving change on the way is counted in wall-clock time. A time the clock
 * skips (02:30 on a spring-forward night) switches when the clock first passes it, at the end of the gap: setHours
 * lands after the gap (03:30), so the target is moved back to the start of that hour (03:00).
 */
export function msUntilNextSwitch(s: Pick<ThemeSettings, "nightStart" | "dayStart">, now: Date): number {
  let best = Infinity;
  for (const t of [s.nightStart, s.dayStart]) {
    const m = clockMinutes(t);
    const at = new Date(now.getTime());
    const place = () => {
      at.setHours(Math.floor(m / 60), m % 60, 0, 0);
      if (minutesOfDay(at) !== m) at.setMinutes(0, 0, 0);
    };
    place();
    if (at.getTime() <= now.getTime()) {
      at.setDate(at.getDate() + 1);
      place();
    }
    best = Math.min(best, at.getTime() - now.getTime());
  }
  return Math.max(1, best);
}

/**
 * The choice held by a parsed cache, with anything missing or unreadable taken from `base` (the defaults, or for a
 * cache another tab just wrote, this tab's current choice: a tab still on an older build writes the cache without
 * the schedule's fields, and they must not be reset by that).
 */
export function themeSettingsFrom(raw: unknown, base: ThemeSettings = DEFAULT_THEME_SETTINGS): ThemeSettings {
  const v = (typeof raw === "object" && raw !== null ? raw : {}) as Partial<Record<keyof ThemeSettings, unknown>>;
  const d = base;
  return {
    mode: v.mode === "fixed" || v.mode === "follow" ? v.mode : d.mode,
    fixed: isSchemeId(v.fixed) ? v.fixed : d.fixed,
    day: isSchemeId(v.day) ? v.day : d.day,
    night: isSchemeId(v.night) ? v.night : d.night,
    schedule: typeof v.schedule === "boolean" ? v.schedule : d.schedule,
    nightStart: isClockTime(v.nightStart) ? v.nightStart : d.nightStart,
    dayStart: isClockTime(v.dayStart) ? v.dayStart : d.dayStart,
  };
}

/** The stored choice (see themeSettingsFrom), from the cache's JSON text. */
export function parseThemeSettings(raw: string | null, base: ThemeSettings = DEFAULT_THEME_SETTINGS): ThemeSettings {
  let v: unknown;
  try {
    v = raw ? (JSON.parse(raw) as unknown) : undefined;
  } catch {
    v = undefined;
  }
  return themeSettingsFrom(v, base);
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
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify(s));
  } catch {
    /* private mode or blocked storage: the choice just won't persist */
  }
}
