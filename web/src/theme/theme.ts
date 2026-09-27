import { createStore } from "@/lib/store";
import { schemeById } from "./schemes";
import { loadThemeSettings, msUntilNextSwitch, resolveTheme, saveThemeSettings, type ThemeSettings } from "./settings";

export const themeStore = createStore<ThemeSettings>(loadThemeSettings());

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
  return resolveTheme(themeStore.get(), systemPrefersDark());
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
  // Applies the theme and, on a schedule, arms one timer for the next switch (a little after it, so the clock has
  // passed the boundary when it fires).
  const apply = () => {
    applyTheme(currentThemeId());
    clearTimeout(timer);
    timer = undefined;
    const s = themeStore.get();
    // Equal times never switch (the day theme stays), so there is nothing to wait for.
    if (s.mode === "schedule" && s.nightStart !== s.dayStart) timer = setTimeout(apply, Math.min(msUntilNextSwitch(s, new Date()) + 500, SCHEDULE_RECHECK_MS));
  };
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
  // A device waking from sleep, or a tab coming back to the front, may have slept through a scheduled switch.
  const wake = () => {
    if (themeStore.get().mode === "schedule" && document.visibilityState === "visible" && currentThemeId() !== document.documentElement.dataset.theme) apply();
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
