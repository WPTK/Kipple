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

/** Reading spacing (WCAG 1.4.12 friendly): Normal follows the Density step, the others override it. */
export const SPACINGS = ["snug", "normal", "roomy"] as const;
export type Spacing = (typeof SPACINGS)[number];
export const SPACING_LABELS: Record<Spacing, string> = { snug: "Snug", normal: "Normal", roomy: "Roomy" };

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
  spacing: "normal",
  motion: "system",
  largeTargets: false,
  listen: false,
  voice: "",
  rate: 1,
};

export const PREFS_KEY = "kipple.prefs.v1";

const isStep = (v: unknown): v is Step => STEPS.includes(v as Step);

export function parsePrefs(raw: string | null): Prefs {
  const d = DEFAULT_PREFS;
  if (!raw) return { ...d };
  try {
    const v = JSON.parse(raw) as Partial<Prefs> | null;
    return {
      font: isFontId(v?.font) ? v.font : d.font,
      textSize: (TEXT_SIZES as readonly number[]).includes(v?.textSize as number) ? (v?.textSize as number) : d.textSize,
      listDensity: isStep(v?.listDensity) ? v.listDensity : d.listDensity,
      readingDensity: isStep(v?.readingDensity) ? v.readingDensity : d.readingDensity,
      adjustSeparately: v?.adjustSeparately === true,
      shortcuts: v?.shortcuts !== false,
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
    return { ...DEFAULT_PREFS };
  }
}

export const prefsStore = createStore<Prefs>(load());

export function updatePrefs(patch: Partial<Prefs>): void {
  prefsStore.set((p) => {
    const next = { ...p, ...patch };
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
  const stack = fontById(p.font).stack;
  if (stack) root.style.setProperty("--kp-reading-font", stack);
  else root.style.removeProperty("--kp-reading-font");
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
