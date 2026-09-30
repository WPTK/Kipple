// What the wizard remembers between its steps, in this page's memory only (a reload forgets all of it). Small on
// purpose: it is imported by the app shell so a sign-out can drop it.
import { createStore } from "@/lib/store";
import { updateTheme } from "@/theme/theme";
import type { ThemeSettings } from "@/theme/settings";
import type { StepId } from "./steps";

/**
 * The web password typed in step 2, so that step 7 can create the API password without asking again. Never stored
 * anywhere; dropped when setup ends and when the app signs out.
 */
export const setupSecret = createStore<string | null>(null);

/** The theme this device had before the wizard touched it, so a Skip (or leaving setup unsaved) can put it back. */
let themeBaseline: ThemeSettings | null = null;
let themeSaved = false;

/** Remembers the device's theme the first time the theme step opens; later visits keep the original. */
export function rememberTheme(t: ThemeSettings): void {
  themeBaseline ??= t;
}
export const themeBefore = (): ThemeSettings | null => themeBaseline;
/** The theme step saved a pick (as the default for every device): nothing to put back. */
export function markThemeSaved(): void {
  themeSaved = true;
}
/** Setup ended without the theme step saving: put this device's theme back the way it was. */
export function revertUnsavedTheme(): void {
  if (themeBaseline && !themeSaved) updateTheme(themeBaseline);
}

const RERUN_KEY = "kipple.setup.rerun";
/** "Run setup again" was used: the saved time zone is the person's choice, not a default to replace. */
export function markRerun(): void {
  try {
    sessionStorage.setItem(RERUN_KEY, "1");
  } catch {
    /* no session storage: the step then treats a saved UTC as unchosen */
  }
}
export function isRerun(): boolean {
  try {
    return sessionStorage.getItem(RERUN_KEY) === "1";
  } catch {
    return false;
  }
}

let welcomeTarget: StepId | null = null;
/** Where the app sends an account with setup pending: the first step, or the one "Run setup again" was asked to open. */
export function setWelcomeTarget(id: StepId | null): void {
  welcomeTarget = id;
}
export const welcomeEntry = (): string => `/welcome/${welcomeTarget ?? "timezone"}`;

/** Setup is over, or the app signed out: forget everything above. */
export function forgetWizardMemory(): void {
  setupSecret.set(null);
  themeBaseline = null;
  themeSaved = false;
  welcomeTarget = null;
  try {
    sessionStorage.removeItem(RERUN_KEY);
  } catch {
    /* nothing to remove */
  }
}
