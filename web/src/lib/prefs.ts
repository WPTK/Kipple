import { createStore } from "./store";

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

export type LayoutId = "magazine";
export type FontId = "default" | "easy";

export interface Prefs {
  layout: LayoutId;
  font: FontId;
  textSize: number;
  /** One "Density" choice drives both by default; "Adjust separately" splits them. */
  listDensity: Step;
  readingDensity: Step;
  adjustSeparately: boolean;
  /** Single-key shortcuts (WCAG 2.1.4): global on/off. */
  shortcuts: boolean;
}

export const DEFAULT_PREFS: Prefs = {
  layout: "magazine",
  font: "default",
  textSize: 1,
  listDensity: "standard",
  readingDensity: "standard",
  adjustSeparately: false,
  shortcuts: true,
};

export const PREFS_KEY = "kipple.prefs.v1";

const isStep = (v: unknown): v is Step => STEPS.includes(v as Step);

export function parsePrefs(raw: string | null): Prefs {
  const d = DEFAULT_PREFS;
  if (!raw) return { ...d };
  try {
    const v = JSON.parse(raw) as Partial<Prefs> | null;
    return {
      layout: "magazine",
      font: v?.font === "easy" ? "easy" : "default",
      textSize: (TEXT_SIZES as readonly number[]).includes(v?.textSize as number) ? (v?.textSize as number) : d.textSize,
      listDensity: isStep(v?.listDensity) ? v.listDensity : d.listDensity,
      readingDensity: isStep(v?.readingDensity) ? v.readingDensity : d.readingDensity,
      adjustSeparately: v?.adjustSeparately === true,
      shortcuts: v?.shortcuts !== false,
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
