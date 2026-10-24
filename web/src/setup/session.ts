// What the wizard keeps outside its screens. Small on purpose: it is imported by the app shell.
import { createStore } from "@/lib/store";

/**
 * A reset was confirmed in Settings and Kipple is restarting: the app shows the waiting page until the instance
 * answers that it is in setup mode. Held here, not in a screen, because the sign-in screen that follows the
 * restart would otherwise replace the page that is waiting.
 */
export const resetting = createStore<{ estimateSeconds: number } | null>(null);

/**
 * "Restore from a backup" in Settings erases the library (keeping a safety copy) and restarts into setup. This remembers, across that restart,
 * that the person asked for the restore step, so setup opens on it instead of the account form. Setup forgets it once shown.
 */
const RESTORE_NEXT_KEY = "kipple.setup.restoreNext";
/** Outlives the one-time flag above until an account exists, so a reload of the restore screen still knows no wizard led to it. */
const RESTORE_FROM_SETTINGS_KEY = "kipple.setup.restoreFromSettings";
export function openRestoreAfterReset(): void {
  try {
    sessionStorage.setItem(RESTORE_NEXT_KEY, "1");
    sessionStorage.setItem(RESTORE_FROM_SETTINGS_KEY, "1");
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
/** The restore step was asked for in Settings (so it is not step 1 of the wizard), in this tab, until an account is created. */
export function restoreFromSettings(): boolean {
  try {
    return sessionStorage.getItem(RESTORE_FROM_SETTINGS_KEY) === "1";
  } catch {
    return false;
  }
}
export function forgetRestoreFromSettings(): void {
  try {
    sessionStorage.removeItem(RESTORE_FROM_SETTINGS_KEY);
  } catch {
    /* nothing to forget */
  }
}
export function forgetRestoreNext(): void {
  try {
    sessionStorage.removeItem(RESTORE_NEXT_KEY);
  } catch {
    /* nothing to forget */
  }
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
 * The steps were opened again with "Run setup again" (or the recommended feeds) in this tab: the wizard stays open
 * although the server says setup is finished. The server is not told, so no other device is sent to the steps. Kept
 * for the tab, so a reload resumes.
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

/** The rerun is over (setup ended, or the person left the steps): forget it. */
export function endRerun(): void {
  try {
    sessionStorage.removeItem(RERUN_KEY);
  } catch {
    /* nothing to remove */
  }
  rerunStore.set(false);
}
