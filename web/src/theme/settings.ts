import { DEFAULT_DAY, DEFAULT_NIGHT, isSchemeId } from "./schemes.ts";

/** Per-device appearance choice. `mode: "follow"` uses the OS light/dark preference. */
export interface ThemeSettings {
  mode: "follow" | "fixed";
  /** Used when mode is "fixed". */
  fixed: string;
  /** Follow-system day theme. Any scheme may be chosen. */
  day: string;
  /** Follow-system night theme. Any scheme may be chosen. */
  night: string;
}

export const THEME_STORAGE_KEY = "kipple.theme.v1";

export const DEFAULT_THEME_SETTINGS: ThemeSettings = {
  mode: "follow",
  fixed: DEFAULT_DAY,
  day: DEFAULT_DAY,
  night: DEFAULT_NIGHT,
};

/** Pure: which scheme id is showing for these settings and OS appearance. */
export function resolveTheme(s: ThemeSettings, prefersDark: boolean): string {
  if (s.mode === "fixed") return s.fixed;
  return prefersDark ? s.night : s.day;
}

export function parseThemeSettings(raw: string | null): ThemeSettings {
  if (!raw) return { ...DEFAULT_THEME_SETTINGS };
  try {
    const v = JSON.parse(raw) as Partial<ThemeSettings> | null;
    const d = DEFAULT_THEME_SETTINGS;
    return {
      mode: v?.mode === "fixed" ? "fixed" : "follow",
      fixed: isSchemeId(v?.fixed) ? v.fixed : d.fixed,
      day: isSchemeId(v?.day) ? v.day : d.day,
      night: isSchemeId(v?.night) ? v.night : d.night,
    };
  } catch {
    return { ...DEFAULT_THEME_SETTINGS };
  }
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
