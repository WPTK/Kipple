// The wizard's step registry: two steps. The account (step 1) runs before a sign-in, in setup mode; the feeds (step 2)
// run after it, at /welcome/feeds. The guard turns any other /welcome address into the feeds step.

export type StepId = "account" | "feeds";

export interface StepInfo {
  id: StepId;
  /** 1 or 2. */
  n: number;
  /** The step's heading. */
  title: string;
  /** Before the account exists (setup mode) or after it (a signed-in session). */
  phase: "setup" | "welcome";
}

export const STEPS: readonly StepInfo[] = [
  { id: "account", n: 1, title: "Create your account", phase: "setup" },
  { id: "feeds", n: 2, title: "Choose your feeds", phase: "welcome" },
];

export const STEP_COUNT = STEPS.length;

export const stepById = (id: StepId): StepInfo => STEPS.find((s) => s.id === id) as StepInfo;

export const welcomePath = (id: StepId): string => `/welcome/${id}`;

/** Where the app sends an account whose first-run setup is pending: the feeds step. */
export const WELCOME_ENTRY = welcomePath("feeds");

/** Where /welcome/<segment> should really go: nowhere (null) when it names the feeds step, else there. */
export function welcomeGuard(segment: string | undefined): string | null {
  return segment === "feeds" ? null : WELCOME_ENTRY;
}
