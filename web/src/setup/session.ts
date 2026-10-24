// What the wizard remembers between its steps, in this page's memory only (a reload forgets all of it). Small on
// purpose: it is imported by the app shell so a sign-out can drop it.
import { adoptThemeDefaults, holdThemeSync } from "@/lib/deviceSync";
import { prefsStore, updatePrefs } from "@/lib/prefs";
import { createStore } from "@/lib/store";
import { updateTheme } from "@/theme/theme";
import type { ThemeSettings } from "@/theme/settings";

/**
 * The web password typed in step 1, so that step 7 can create the API password without asking again. Never stored
 * anywhere; dropped when setup ends and when the app signs out.
 */
export const setupSecret = createStore<string | null>(null);

/**
 * The feeds file from a restore that took "Feeds only": held here (in this page's memory) from the restore screen,
 * through the account step, to the import step, which offers it. Dropped when setup ends.
 */
export const restoredFeeds = createStore<File | null>(null);

/**
 * A reset was confirmed in Settings and Kipple is restarting: the app shows the waiting page until the instance
 * answers that it is in setup mode. Held here, not in a screen, because the sign-in screen that follows the
 * restart would otherwise replace the page that is waiting.
 */
export const resetting = createStore<{ estimateSeconds: number } | null>(null);

/**
 * "Restore from a backup" in Settings sets the library aside and restarts into setup. This remembers, across that restart,
 * that the person asked for the restore step, so setup opens on it instead of the account form. Setup forgets it once shown.
 */
const RESTORE_NEXT_KEY = "kipple.setup.restoreNext";
export function openRestoreAfterReset(): void {
  try {
    sessionStorage.setItem(RESTORE_NEXT_KEY, "1");
  } catch {
    /* no session storage: setup then opens on the account form, which has a restore link */
  }
}
export function wantsRestoreNext(): boolean {
  try {
    return sessionStorage.getItem(RESTORE_NEXT_KEY) === "1";
  } catch {
    return false;
  }
}
export function forgetRestoreNext(): void {
  try {
    sessionStorage.removeItem(RESTORE_NEXT_KEY);
  } catch {
    /* nothing to forget */
  }
}

/** An app password was made in step 7 during this setup (it is shown once, so a second one would silently replace it). */
export const apiPasswordMade = createStore<boolean>(false);

/** The theme this device had before the wizard touched it, so a Skip (or leaving setup unsaved) can put it back. */
let themeBaseline: ThemeSettings | null = null;
/** The pick the theme step saved as the default for every device, once it has. */
let themeSaved: ThemeSettings | null = null;
/** The same two for the reading font (a font id from lib/fonts), which the look step previews and saves with the theme. */
let fontBaseline: string | null = null;
let fontSaved: string | null = null;

/** Remembers the device's theme and reading font the first time the look step opens; later visits keep the original. */
export function rememberTheme(t: ThemeSettings, font: string = prefsStore.get().font): void {
  themeBaseline ??= t;
  fontBaseline ??= font;
}
/** What Skip goes back to: the pick that was saved, else what this device had before the wizard. */
const themeBefore = (): ThemeSettings | null => themeSaved ?? themeBaseline;
/** The reading font Skip goes back to, on the same rule. */
const fontBefore = (): string | null => fontSaved ?? fontBaseline;
/**
 * A pick is being previewed: it shows on this page but is not written to this device's profile (a preview is not a
 * choice, and the profile would pin the device to values it only tried).
 */
export function previewTheme(): void {
  holdThemeSync(true);
}
/**
 * The theme step saved `t` as the default for every device without its own choice: nothing to put back, and this
 * device follows that default rather than holding an override of it.
 */
export function markThemeSaved(t: ThemeSettings, matchesLocal = true, font?: { id: string; matchesLocal: boolean }): void {
  themeSaved = t;
  // Only when this device shows exactly what was saved; otherwise the difference stays pending and is written normally.
  if (matchesLocal) adoptThemeDefaults(["ui.theme", "ui.theme_day", "ui.theme_night"]);
  if (font) {
    fontSaved = font.id;
    if (font.matchesLocal) adoptThemeDefaults(["ui.font_body"]);
  }
  holdThemeSync(false);
}
/** A preview that is over (Skip, or setup ended): the theme and font are put back on this device and the profile is written normally again. */
export function settleThemePreview(): void {
  const back = themeBefore();
  if (back) updateTheme(back);
  const font = fontBefore();
  if (font !== null && prefsStore.get().font !== font) updatePrefs({ font });
  holdThemeSync(false);
}
/** Setup ended without the theme step saving: put this device's theme back the way it was. */
export function revertUnsavedTheme(): void {
  settleThemePreview();
}

const RERUN_KEY = "kipple.setup.rerun";
const readRerun = (): boolean => {
  try {
    return sessionStorage.getItem(RERUN_KEY) === "1";
  } catch {
    return false;
  }
};
/**
 * The steps were opened again with "Run setup again" (or the recommended feeds) in this tab: the saved time zone is the
 * person's choice, not a default to replace, and the wizard stays open although the server says setup is finished. The
 * server is not told, so no other device is sent to the steps. Kept for the tab, so a reload resumes.
 */
export const rerunStore = createStore<boolean>(readRerun());
export function markRerun(): void {
  try {
    sessionStorage.setItem(RERUN_KEY, "1");
  } catch {
    /* no session storage: the rerun then lasts until this page is reloaded */
  }
  rerunStore.set(true);
}
export const isRerun = (): boolean => rerunStore.get();

/** Where the app sends an account whose first-run setup is pending: the first step. */
export const welcomeEntry = (): string => "/welcome/timezone";

/** Setup is over, or the app signed out: forget everything above. */
export function forgetWizardMemory(): void {
  setupSecret.set(null);
  apiPasswordMade.set(false);
  restoredFeeds.set(null);
  settleThemePreview();
  themeBaseline = null;
  themeSaved = null;
  fontBaseline = null;
  fontSaved = null;
  try {
    sessionStorage.removeItem(RERUN_KEY);
  } catch {
    /* nothing to remove */
  }
  rerunStore.set(false);
}
