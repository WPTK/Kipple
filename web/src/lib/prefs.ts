import { createStore } from "./store";
import { fontById, isFontId, type FontId } from "./fonts";

export type { FontId };

// Per-device appearance and behavior. Stored in localStorage only: the owner's
// decision is that appearance settings are per device, never synced.

export const STEPS = ["dense", "snug", "standard", "relaxed", "airy"] as const;
export type Step = (typeof STEPS)[number];
export const STEP_LABELS: Record<Step, string> = {
  dense: "Dense",
  snug: "Snug",
  standard: "Standard",
  relaxed: "Relaxed",
  airy: "Airy",
};

export const TEXT_SIZES = [0.875, 1, 1.125, 1.25, 1.5] as const;
export const TEXT_SIZE_LABELS = ["Smaller", "Default", "Large", "Larger", "Largest"] as const;

/** Text spacing (WCAG 1.4.12): Default follows the Density step; Less and More override it. Stored ids are unchanged. */
export const SPACINGS = ["snug", "normal", "roomy"] as const;
export type Spacing = (typeof SPACINGS)[number];
export const SPACING_LABELS: Record<Spacing, string> = { snug: "Less", normal: "Default", roomy: "More" };

export const MOTIONS = ["system", "on", "off"] as const;
/** "Reduce motion": follow the OS, force it on, or force it off. */
export type Motion = (typeof MOTIONS)[number];
export const MOTION_LABELS: Record<Motion, string> = { system: "Follow system", on: "On", off: "Off" };

export const RATES = [0.8, 1, 1.2, 1.5] as const;

export interface Prefs {
  font: FontId;
  textSize: number;
  /** One "Density" choice drives both by default; "Adjust separately" splits them. */
  listDensity: Step;
  readingDensity: Step;
  adjustSeparately: boolean;
  /** Single-key shortcuts (WCAG 2.1.4): global on/off. */
  shortcuts: boolean;
  /** The user has set shortcuts themselves; until then the default follows the device (off when touch-first). */
  shortcutsChosen: boolean;
  spacing: Spacing;
  motion: Motion;
  /** Bigger touch and click targets everywhere (56 px instead of 44 px). */
  largeTargets: boolean;
  /** Adds a Listen button to articles (Web Speech API). */
  listen: boolean;
  /** SpeechSynthesisVoice.voiceURI, or "" for the device default. */
  voice: string;
  rate: number;
}

export const DEFAULT_PREFS: Prefs = {
  font: "default",
  textSize: 1,
  listDensity: "standard",
  readingDensity: "standard",
  adjustSeparately: false,
  shortcuts: true,
  shortcutsChosen: false,
  spacing: "normal",
  motion: "system",
  largeTargets: false,
  listen: false,
  voice: "",
  rate: 1,
};

export const PREFS_KEY = "kipple.prefs.v1";

/**
 * A touch-first device: the primary pointer is coarse (a finger) and no mouse or trackpad is attached. Single-key
 * shortcuts default to off there (there is no keyboard to press them on, and letter keys clash with the on-screen
 * one); an iPad with a trackpad or mouse reports a fine secondary pointer and keeps them on.
 */
export function touchFirst(): boolean {
  try {
    return window.matchMedia("(pointer: coarse)").matches && !window.matchMedia("(any-pointer: fine)").matches;
  } catch {
    return false;
  }
}

const isStep = (v: unknown): v is Step => STEPS.includes(v as Step);

export function parsePrefs(raw: string | null): Prefs {
  const d = DEFAULT_PREFS;
  if (!raw) return { ...d, shortcuts: !touchFirst() };
  try {
    const v = JSON.parse(raw) as Partial<Prefs> | null;
    // Chosen only when flagged. A saved value with no flag at all predates the flag, when an "off" could only have
    // been the user's doing, so that one stays chosen. A flagged-false "off" is the device default saved back.
    const chosen = typeof v?.shortcuts === "boolean" && (v.shortcutsChosen === true || (v.shortcutsChosen === undefined && v.shortcuts === false));
    return {
      font: isFontId(v?.font) ? v.font : d.font,
      textSize: (TEXT_SIZES as readonly number[]).includes(v?.textSize as number) ? (v?.textSize as number) : d.textSize,
      listDensity: isStep(v?.listDensity) ? v.listDensity : d.listDensity,
      readingDensity: isStep(v?.readingDensity) ? v.readingDensity : d.readingDensity,
      adjustSeparately: v?.adjustSeparately === true,
      // An explicit "off" is always kept. An "on" that was only the old default is not a choice: it follows the device.
      shortcuts: chosen ? (v?.shortcuts as boolean) : !touchFirst(),
      shortcutsChosen: chosen,
      spacing: (SPACINGS as readonly unknown[]).includes(v?.spacing) ? (v?.spacing as Spacing) : d.spacing,
      motion: (MOTIONS as readonly unknown[]).includes(v?.motion) ? (v?.motion as Motion) : d.motion,
      largeTargets: v?.largeTargets === true,
      listen: v?.listen === true,
      voice: typeof v?.voice === "string" ? v.voice : d.voice,
      rate: (RATES as readonly number[]).includes(v?.rate as number) ? (v?.rate as number) : d.rate,
    };
  } catch {
    return { ...d };
  }
}

function load(): Prefs {
  try {
    return parsePrefs(localStorage.getItem(PREFS_KEY));
  } catch {
    return { ...DEFAULT_PREFS, shortcuts: !touchFirst() };
  }
}

/**
 * Evidence of a hardware keyboard on a touch-first device: a key pressed while nothing is being typed into (the
 * on-screen keyboard only appears for text fields). The default then switches on; a choice the user made stays.
 */
function watchHardwareKeyboard(): void {
  const p = prefsStore.get();
  if (p.shortcutsChosen || p.shortcuts || typeof document === "undefined") return;
  const onKey = (e: KeyboardEvent) => {
    const t = e.target;
    const typing = t instanceof HTMLElement && (t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName));
    if (typing || e.ctrlKey || e.metaKey || e.altKey || e.key.length !== 1) return;
    document.removeEventListener("keydown", onKey, true);
    if (!prefsStore.get().shortcutsChosen) prefsStore.set((q) => ({ ...q, shortcuts: true }));
  };
  document.addEventListener("keydown", onKey, true);
}

export const prefsStore = createStore<Prefs>(load());

export function updatePrefs(patch: Partial<Prefs>): void {
  prefsStore.set((p) => {
    const next = { ...p, ...patch };
    if ("shortcuts" in patch && !("shortcutsChosen" in patch)) next.shortcutsChosen = true;
    // One Density choice drives both unless "Adjust separately" is on.
    if (!next.adjustSeparately) {
      if (patch.listDensity && !patch.readingDensity) next.readingDensity = patch.listDensity;
      if (patch.readingDensity && !patch.listDensity) next.listDensity = patch.readingDensity;
    }
    return next;
  });
}

export function applyPrefs(p: Prefs, root: HTMLElement = document.documentElement): void {
  root.dataset.font = p.font;
  root.dataset.listDensity = p.listDensity;
  root.dataset.readingDensity = p.readingDensity;
  root.style.setProperty("--kp-scale", String(p.textSize));
  // One font choice (the Aa menu) is both the article font and the app font. "Default" keeps the system UI
  // font for lists and chrome and the reading serif for articles; any other choice applies to both, at once.
  // Settings and the menus opt out in CSS (.ui-font), so they always stay in the system UI font.
  const stack = fontById(p.font).stack;
  if (stack) {
    root.style.setProperty("--kp-reading-font", stack);
    root.style.setProperty("--kp-app-font", stack);
  } else {
    root.style.removeProperty("--kp-reading-font");
    root.style.removeProperty("--kp-app-font");
  }
  root.dataset.spacing = p.spacing;
  root.dataset.motion = p.motion;
  root.dataset.targets = p.largeTargets ? "large" : "normal";
}

/** Whether animation should be off: the in-app override, else the OS setting. */
export function prefersReducedMotion(): boolean {
  const m = prefsStore.get().motion;
  if (m === "on") return true;
  if (m === "off") return false;
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}

/** Wire the store to the DOM and localStorage. Call once at startup. */
export function initPrefs(): void {
  applyPrefs(prefsStore.get());
  watchHardwareKeyboard();
  prefsStore.subscribe(() => {
    const p = prefsStore.get();
    applyPrefs(p);
    try {
      localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {
      /* not persisted */
    }
  });
}
