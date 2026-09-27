import { createStore } from "@/lib/store";
import { schemeById } from "./schemes";
import { loadThemeSettings, minutesOfDay, msUntilNextSwitch, onSchedule, resolveTheme, saveThemeSettings, type ThemeSettings } from "./settings";

export const themeStore = createStore<ThemeSettings>(loadThemeSettings());

/**
 * The scheme id showing on this page. It changes without the settings changing (the OS appearance, a scheduled
 * switch), so what is drawn in the theme's colors outside CSS (the Wrapped card image) re-renders on this.
 */
export const activeThemeStore = createStore<string>("");

const DARK_QUERY = "(prefers-color-scheme: dark)";

export function systemPrefersDark(): boolean {
  try {
    return window.matchMedia(DARK_QUERY).matches;
  } catch {
    return false;
  }
}

/** The scheme currently showing (resolved from settings, the OS and, on a schedule, the local time). */
export function currentThemeId(): string {
  return resolveTheme(themeStore.get(), systemPrefersDark(), minutesOfDay(new Date()));
}

/** Set data-theme and the single <meta name="theme-color">, live (no reload). */
export function applyTheme(id: string, doc: Document = document): void {
  const scheme = schemeById(id);
  doc.documentElement.dataset.theme = scheme.id;
  const metas = Array.from(doc.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]'));
  let meta = metas[0];
  // The static media-qualified fallbacks in index.html give way to one live tag.
  metas.slice(1).forEach((m) => m.remove());
  if (!meta) {
    meta = doc.createElement("meta");
    meta.name = "theme-color";
    doc.head.appendChild(meta);
  }
  meta.removeAttribute("media");
  meta.content = scheme.tokens.meta;
  if (doc === document) activeThemeStore.set(scheme.id);
}

export function updateTheme(patch: Partial<ThemeSettings>): void {
  themeStore.set((s) => ({ ...s, ...patch }));
}

/**
 * The longest the schedule timer sleeps before it looks at the clock again. Timers pause while a device sleeps
 * and drift when the clock is changed, so a switch is never more than this late (waking the tab also re-checks).
 */
export const SCHEDULE_RECHECK_MS = 15 * 60_000;

/** Wire the store, the OS appearance listener and the schedule timer. Call once at startup. */
export function initTheme(): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let shown: string | undefined;
  // Shows the theme in force (only touching the page when it changed) and, on a schedule, arms one timer for the next
  // switch, a little after it so the clock has passed the boundary when it fires, always from the real clock. Equal
  // times never switch, so there is nothing to wait for.
  function apply() {
    const id = currentThemeId();
    if (id !== shown) {
      applyTheme(id);
      shown = id;
    }
    clearTimeout(timer);
    timer = undefined;
    const s = themeStore.get();
    if (onSchedule(s) && s.nightStart !== s.dayStart) timer = setTimeout(apply, Math.min(msUntilNextSwitch(s, new Date()) + 500, SCHEDULE_RECHECK_MS));
  }
  apply();
  const off = themeStore.subscribe(() => {
    saveThemeSettings(themeStore.get());
    apply();
  });
  let mql: MediaQueryList | null = null;
  try {
    mql = window.matchMedia(DARK_QUERY);
    mql.addEventListener("change", apply);
  } catch {
    /* no matchMedia: day theme stays */
  }
  // A device waking from sleep, or a tab coming back to the front, may have slept through a switch or hold a timer
  // that paused while it slept: apply() corrects the theme and re-arms from the clock. Coming back usually fires both
  // visibilitychange and focus; the second one in the same moment is skipped.
  let wokeAt = -Infinity;
  const wake = () => {
    if (!onSchedule(themeStore.get()) || document.visibilityState !== "visible") return;
    const now = performance.now();
    if (now - wokeAt < 1000) return;
    wokeAt = now;
    apply();
  };
  document.addEventListener("visibilitychange", wake);
  window.addEventListener("focus", wake);
  return () => {
    off();
    clearTimeout(timer);
    mql?.removeEventListener("change", apply);
    document.removeEventListener("visibilitychange", wake);
    window.removeEventListener("focus", wake);
  };
}
