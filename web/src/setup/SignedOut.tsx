import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LoginScreen } from "@/screens/LoginScreen";
import { Button } from "@/ui/button";
import { Notice } from "@/ui/kit";
import { fetchInstance, fetchSetupState, openRefusedReason, signInOpen, takeSetupFragment, isOpenRefused, type OpenReason } from "./api";
import { AccountStep } from "./AccountStep";
import { StepActions, WizardFrame } from "./Frame";
import { OpenRefusedScreen } from "./OpenRefused";
import { stepById } from "./steps";
import { TokenStep } from "./TokenStep";

// The query keys start with "auth" so the app's sign-out cleanup (App.tsx) leaves them alone.
const INSTANCE_KEY = ["auth", "instance"] as const;
const SETUP_STATE_KEY = ["auth", "setup-state"] as const;

/**
 * What a signed-out browser sees. GET /api/instance says which: the setup wizard (no account yet), a silent sign-in
 * (open mode: no password), or the sign-in form. A server that predates the wizard has no such route; the form is
 * shown then, as it always was.
 */
export function SignedOut() {
  // Read once, and removed from the address at once, whatever mode Kipple is in: the code in a link is a credential.
  const [code] = useState(() => takeSetupFragment());
  const inst = useQuery({ queryKey: INSTANCE_KEY, queryFn: ({ signal }) => fetchInstance(signal), retry: false, staleTime: 0, gcTime: 0 });
  if (inst.isPending) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Loading Kipple</span>
      </div>
    );
  }
  if (inst.isError || !inst.data) return <LoginScreen />;
  if (inst.data.setup) return <SetupFlow code={code} />;
  if (inst.data.auth === "open") return <OpenSignIn />;
  return <LoginScreen />;
}

/** Steps 1 and 2, before there is an account. Step 2 signs the browser in; the app then continues at /welcome. */
function SetupFlow({ code }: { code: string }) {
  const qc = useQueryClient();
  const [phase, setPhase] = useState<"token" | "account" | null>(null);
  const [notice, setNotice] = useState<string | undefined>();
 const st = useQuery({ queryKey: SETUP_STATE_KEY, queryFn: ({ signal }) => fetchSetupState(signal), retry: false, staleTime: 0, gcTime: 0 });

  if (st.isPending) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Loading Kipple</span>
      </div>
    );
  }
  if (st.isError || !st.data) {
    const gone = (st.error as { status?: number } | null)?.status === 404;
    return (
      <WizardFrame step={stepById("token")}>
        <Notice tone={gone ? "warn" : "error"} role="alert">{gone ? "Kipple was set up a moment ago. Reload the page to sign in." : "Kipple couldn't reach the server. Try again."}</Notice>
        <StepActions>
          <Button variant="solid" onClick={() => (gone ? window.location.reload() : void st.refetch())}>
            {gone ? "Reload" : "Try again"}
          </Button>
        </StepActions>
      </WizardFrame>
    );
  }
  const current = phase ?? (st.data.claimed ? "account" : "token");
  if (current === "token") {
    return (
      <TokenStep
        initialCode={code}
        issuedAt={st.data.token_issued_at}
        notice={notice}
        onClaimed={() => {
          setNotice(undefined);
          setPhase("account");
        }}
      />
    );
  }
  return (
    <AccountStep
      state={st.data}
      onCreated={() => void qc.invalidateQueries()}
      onRestart={() => {
        setNotice("The setup code was accepted a while ago and has timed out. Enter it again to continue.");
        setPhase("token");
      }}
      onDone={() => window.location.reload()}
    />
  );
}

/** Open mode: nothing to type. Signs in by itself and says why if the server refuses this address. */
function OpenSignIn() {
  const qc = useQueryClient();
  const [refused, setRefused] = useState<OpenReason | null | undefined>(undefined);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(true);
  const started = useRef(false);

  const run = async () => {
    setBusy(true);
    setFailed(false);
    try {
      await signInOpen();
      await qc.invalidateQueries();
    } catch (e) {
      if (isOpenRefused(e)) setRefused(openRefusedReason(e));
      else setFailed(true);
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (refused !== undefined) {
    return (
      <OpenRefusedScreen
        reason={refused}
        busy={busy}
        onRetry={() => {
          setRefused(undefined);
          void run();
        }}
      />
    );
  }
  if (failed) {
    return (
      <main className="flex h-full items-center justify-center px-4">
        <div className="flex max-w-sm flex-col gap-3" role="alert">
          <h1 className="text-xl font-bold">Kipple couldn't sign you in</h1>
          <p className="text-fg2">Kipple couldn't reach the server, or it answered with something unexpected. Try again, and check the Kipple logs if it keeps happening.</p>
          <Button variant="solid" className="self-start" onClick={() => void run()}>
            Try again
          </Button>
        </div>
      </main>
    );
  }
  return (
    <div className="flex h-full items-center justify-center" role="status">
      <span className="text-fg2">Signing you in</span>
    </div>
  );
}
