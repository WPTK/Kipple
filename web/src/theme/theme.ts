import { createStore } from "@/lib/store";
import { schemeById } from "./schemes";
import { loadThemeSettings, resolveTheme, saveThemeSettings, type ThemeSettings } from "./settings";

export const themeStore = createStore<ThemeSettings>(loadThemeSettings());

const DARK_QUERY = "(prefers-color-scheme: dark)";

export function systemPrefersDark(): boolean {
  try {
    return window.matchMedia(DARK_QUERY).matches;
  } catch {
    return false;
  }
}

/** The scheme currently showing (resolved from settings and the OS). */
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

/** Wire the store and the OS appearance listener. Call once at startup. */
export function initTheme(): () => void {
  const apply = () => applyTheme(currentThemeId());
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
  return () => {
    off();
    mql?.removeEventListener("change", apply);
  };
}
