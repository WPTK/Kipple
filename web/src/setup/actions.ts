import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { keys, type BootstrapAnswer } from "@/api/queries";
import { completeOnboarding, restartOnboarding } from "./api";
import { forgetWizardMemory, markRerun, revertUnsavedTheme, setWelcomeTarget } from "./session";
import type { StepId } from "./steps";

/** Records in the app's copy of the account that first-run setup is (or is not) pending, so the routing follows at once. */
function setPending(qc: QueryClient, pending: boolean): void {
  qc.setQueryData<BootstrapAnswer>(keys.bootstrap, (old) => (old ? { ...old, user: { ...old.user, setup_pending: pending } } : old));
  void qc.invalidateQueries({ queryKey: keys.me });
}

/**
 * "Finish" and "Skip the rest" (POST /api/onboarding/complete) and "Run setup again" (POST /api/onboarding/restart).
 * Both throw on a failed call; the caller says so.
 */
export function useSetupActions() {
  const qc = useQueryClient();
  return {
    /** Ends the wizard. The password kept from step 2 is forgotten, and a theme picked but never saved is put back. */
    finish: async () => {
      await completeOnboarding();
      revertUnsavedTheme();
      forgetWizardMemory();
      setPending(qc, false);
    },
    /** Starts the first-run steps over (or at one of them) for an account that finished them. Nothing is changed by that. */
    restart: async (at: StepId = "timezone") => {
      await restartOnboarding();
      markRerun();
      setWelcomeTarget(at);
      // The app shell sends an account with setup pending to the step named above.
      setPending(qc, true);
    },
  };
}
