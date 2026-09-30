import { useEffect, useRef, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/button";
import { STEP_COUNT, type StepInfo } from "./steps";

/**
 * The page every wizard step sits in: a narrow column with "Step 3 of 7", the step's heading (focused when the step
 * appears, so a screen reader says where it is) and the step's content. Scrolls on its own so a phone's keyboard or
 * a long list never traps the buttons.
 */
export function WizardFrame({
  step,
  description,
  onSkipAll,
  skipAllBusy,
  children,
}: {
  step: StepInfo;
  description?: ReactNode;
  /** Finishes setup without the remaining steps (POST /api/onboarding/complete). Offered on the signed-in steps. */
  onSkipAll?: () => void;
  skipAllBusy?: boolean;
  children: ReactNode;
}) {
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    heading.current?.focus({ preventScroll: true });
  }, [step.id]);
  return (
    <main className="pt-safe pb-safe h-full overflow-y-auto" data-testid="wizard" data-step={step.id}>
      <div className="mx-auto flex min-h-full w-full max-w-xl flex-col gap-5 px-4 py-6">
        <header className="flex flex-col gap-3">
          <div className="flex items-center justify-between gap-3">
            <p className="text-sm font-semibold text-fg2">Kipple setup</p>
            {onSkipAll ? (
              <Button variant="link" className="min-h-11" disabled={skipAllBusy} onClick={onSkipAll}>
                Skip the rest of setup
              </Button>
            ) : null}
          </div>
          <Progress n={step.n} />
          <h1 ref={heading} tabIndex={-1} className="text-2xl font-bold outline-none">
            {step.title}
          </h1>
          {description ? <p className="text-base text-fg2">{description}</p> : null}
        </header>
        {children}
      </div>
    </main>
  );
}

function Progress({ n }: { n: number }) {
  return (
    <div>
      <p className="text-xs font-semibold tracking-wide text-fg2 uppercase">
        Step {n} of {STEP_COUNT}
      </p>
      <div aria-hidden="true" className="mt-1 flex gap-1">
        {Array.from({ length: STEP_COUNT }, (_, i) => (
          <span key={i} className={cn("h-1.5 flex-1 rounded-full", i < n ? "bg-accent" : "bg-line")} />
        ))}
      </div>
    </div>
  );
}

/** The row of buttons under a step: Back on the left, Skip and the main button on the right (wrapping on a phone). */
export function StepActions({ children, back }: { children: ReactNode; back?: ReactNode }) {
  return (
    <div className="mt-auto flex flex-wrap items-center justify-between gap-2 border-t border-line pt-4">
      <div>{back}</div>
      <div className="flex flex-wrap items-center justify-end gap-2">{children}</div>
    </div>
  );
}
