import { useEffect, useRef, useState } from "react";
import { Navigate, useParams } from "react-router";
import { useBootstrap } from "@/api/queries";
import { announce, LiveRegion, toast, Toasts } from "@/shell/toasts";
import { failed } from "@/api/client";
import { useSetupActions } from "./actions";
import { useStore } from "@/lib/store";
import { endRerun, rerunStore } from "./session";
import { FeedsStep } from "./FeedsStep";
import { welcomeGuard } from "./steps";

/**
 * /welcome/feeds: step 2 of the wizard, for a signed-in account whose setup is pending or that opened the step again.
 * Adding feeds writes through the ordinary endpoint; "Continue" then ends setup (POST /api/onboarding/complete) and
 * opens the reader.
 */
export function Welcome() {
  const params = useParams();
  const segment = params["*"] || undefined;
  const boot = useBootstrap();
  const actions = useSetupActions();
  const [ending, setEnding] = useState(false);
  const endingNow = useRef(false);

  const rerun = useStore(rerunStore);
  const over = boot.data !== undefined && boot.data.user.setup_pending !== true && !rerun;
  // Setup ended somewhere else (another tab or device): drop the rerun mark this page may hold.
  useEffect(() => {
    if (over) endRerun();
  }, [over]);

  // Nothing pending (finished already, or a bookmark): the reader.
  if (over) return <Navigate to="/l/unread" replace state={{ arrival: true }} />;
  const redirect = welcomeGuard(segment);
  if (redirect) return <Navigate to={redirect} replace />;

  const end = async () => {
    if (endingNow.current) return;
    endingNow.current = true;
    setEnding(true);
    try {
      await actions.finish();
      announce("Setup finished");
    } catch (e) {
      toast(failed("finish setup", e), "error");
      endingNow.current = false;
      setEnding(false);
    }
  };

  return (
    <>
      <FeedsStep onDone={() => void end()} finishing={ending} />
      <LiveRegion />
      <Toasts inset="none" />
    </>
  );
}
