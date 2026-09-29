import { useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router";
import { useBootstrap } from "@/api/queries";
import { announce, LiveRegion, toast, Toasts } from "@/shell/toasts";
import { errorMessage } from "@/api/client";
import { useSetupActions } from "./actions";
import { FeedsStep } from "./FeedsStep";
import { FinishStep } from "./FinishStep";
import { ImportStep } from "./ImportStep";
import { nextWelcome, prevWelcome, welcomeGuard, welcomePath, type StepId } from "./steps";
import { ThemeStep } from "./ThemeStep";
import { TimeZoneStep } from "./TimeZoneStep";

/**
 * /welcome/<step>: steps 3 to 7 of the wizard for a signed-in account whose setup is pending. Each step writes through
 * the ordinary endpoints as it goes, and the step is in the address, so a reload or the Back button lands where you were.
 * "Skip the rest of setup" and "Finish" both end it (POST /api/onboarding/complete) and open the reader.
 */
export function Welcome() {
  const params = useParams();
  const segment = params["*"] || undefined;
  const navigate = useNavigate();
  const boot = useBootstrap();
  const actions = useSetupActions();
  const [ending, setEnding] = useState(false);

  // Nothing pending (finished already, or a bookmark): the reader.
  if (boot.data && boot.data.user.setup_pending !== true) return <Navigate to="/l/unread" replace />;
  const redirect = welcomeGuard(segment);
  if (redirect) return <Navigate to={redirect} replace />;
  const id = segment as StepId;

  const go = (to: StepId | null) => navigate(to ? welcomePath(to) : "/l/unread");
  const next = () => go(nextWelcome(id)?.id ?? null);
  const back = () => go(prevWelcome(id)?.id ?? null);
  const end = async () => {
    setEnding(true);
    try {
      await actions.finish();
      announce("Setup finished");
    } catch (e) {
      toast(errorMessage(e), "error");
      setEnding(false);
    }
  };

  const common = { onSkipAll: () => void end(), skipAllBusy: ending };
  return (
    <>
      {id === "timezone" ? <TimeZoneStep onNext={next} {...common} /> : null}
      {id === "theme" ? <ThemeStep onBack={back} onNext={next} {...common} /> : null}
      {id === "import" ? <ImportStep onBack={back} onNext={next} {...common} /> : null}
      {id === "feeds" ? <FeedsStep onBack={back} onNext={next} {...common} /> : null}
      {id === "finish" ? <FinishStep onBack={back} onFinish={() => void end()} busy={ending} /> : null}
      <LiveRegion />
      <Toasts inset="none" />
    </>
  );
}
