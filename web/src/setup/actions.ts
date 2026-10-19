import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { keys, type BootstrapAnswer } from "@/api/queries";
import { completeOnboarding } from "./api";
import { forgetWizardMemory, markRerun, revertUnsavedTheme } from "./session";
import { welcomePath, type StepId } from "./steps";

/** Records in the app's copy of the account that first-run setup is (or is not) pending, so the routing follows at once. */
function setPending(qc: QueryClient, pending: boolean): void {
  qc.setQueryData<BootstrapAnswer>(keys.bootstrap, (old) => (old ? { ...old, user: { ...old.user, setup_pending: pending } } : old));
  void qc.invalidateQueries({ queryKey: keys.me });
}

/**
 * "Finish" and "Skip the rest" (POST /api/onboarding/complete), which throws on a failed call; the caller says so. "Run setup
 * again" opens the steps in this tab only and calls nothing.
 */
export function useSetupActions() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return {
    /** Ends the wizard. The password kept from step 1 is forgotten, and a theme picked but never saved is put back. */
    finish: async () => {
      await completeOnboarding();
      revertUnsavedTheme();
      forgetWizardMemory();
      setPending(qc, false);
    },
    /** Opens the first-run steps again (or at one of them) in this tab for an account that finished them. Nothing is changed on the server. */
    restart: (at: StepId = "timezone") => {
      markRerun();
      void navigate(welcomePath(at));
    },
  };
}
