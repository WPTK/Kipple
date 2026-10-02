// The wizard's step registry: the six steps in order, which of them runs before a sign-in (1, the account, in setup
// mode) and which after (2 to 6, at /welcome/<id>), and the guard that turns an unknown or unavailable step into a real one.

export type StepId = "account" | "timezone" | "theme" | "import" | "feeds" | "finish";

export interface StepInfo {
  id: StepId;
  /** 1 to 6. */
  n: number;
  /** The step's heading. */
  title: string;
  /** Before the account exists (setup mode) or after it (a signed-in session). */
  phase: "setup" | "welcome";
  /** Whether "Skip" is offered. Step 1 cannot be skipped; step 6 is the end. */
  skippable: boolean;
}

export const STEPS: readonly StepInfo[] = [
  { id: "account", n: 1, title: "Create your account", phase: "setup", skippable: false },
  { id: "timezone", n: 2, title: "Choose your time zone", phase: "welcome", skippable: true },
  { id: "theme", n: 3, title: "Look and feel", phase: "welcome", skippable: true },
  { id: "import", n: 4, title: "Bring your feeds along", phase: "welcome", skippable: true },
  { id: "feeds", n: 5, title: "Recommended feeds", phase: "welcome", skippable: true },
  { id: "finish", n: 6, title: "You're all set", phase: "welcome", skippable: false },
];

export const STEP_COUNT = STEPS.length;
export const WELCOME_STEPS = STEPS.filter((s) => s.phase === "welcome");
export const FIRST_WELCOME = WELCOME_STEPS[0] as StepInfo;

export const stepById = (id: StepId): StepInfo => STEPS.find((s) => s.id === id) as StepInfo;

/** The signed-in step for a URL segment, or null when the segment names none (a typo, or a step from before sign-in). */
export function welcomeStep(segment: string | undefined): StepInfo | null {
  return WELCOME_STEPS.find((s) => s.id === segment) ?? null;
}

export const welcomePath = (id: StepId): string => `/welcome/${id}`;

/** The step after `id` in the signed-in run, or null after the last. */
export function nextWelcome(id: StepId): StepInfo | null {
  const i = WELCOME_STEPS.findIndex((s) => s.id === id);
  return i >= 0 ? (WELCOME_STEPS[i + 1] ?? null) : null;
}

/** The step before `id` in the signed-in run, or null for the first. */
export function prevWelcome(id: StepId): StepInfo | null {
  const i = WELCOME_STEPS.findIndex((s) => s.id === id);
  return i > 0 ? (WELCOME_STEPS[i - 1] ?? null) : null;
}

/**
 * Where /welcome/<segment> should really go: nowhere (null) when it names a step, else the first step. A signed-in
 * account that no longer has setup pending leaves /welcome altogether (that is the caller's check, made first).
 */
export function welcomeGuard(segment: string | undefined): string | null {
  return welcomeStep(segment) ? null : welcomePath(FIRST_WELCOME.id);
}
