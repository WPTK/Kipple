import { Suspense, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { lazyScreen } from "@/lib/lazyScreen";
import { LoginScreen } from "@/screens/LoginScreen";
import { Skeleton } from "@/ui/kit";
import { fetchInstance, takeSetupFragment } from "./api";

// The wizard's signed-out half loads only when Kipple has no account or has no password; everyone else gets the sign-in form.
const SetupFlow = lazyScreen(() => import("./SetupFlow").then((m) => ({ default: m.SetupFlow })));
const OpenSignIn = lazyScreen(() => import("./SetupFlow").then((m) => ({ default: m.OpenSignIn })));

// The query key starts with "auth" so the app's sign-out cleanup (App.tsx) leaves it alone.
const INSTANCE_KEY = ["auth", "instance"] as const;
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
  if (inst.data.setup) {
    return (
      <Suspense fallback={<Skeleton label="Loading setup" />}>
        <SetupFlow code={code} />
      </Suspense>
    );
  }
  if (inst.data.auth === "open") {
    return (
      <Suspense fallback={<Skeleton label="Signing you in" />}>
        <OpenSignIn />
      </Suspense>
    );
  }
  return <LoginScreen />;
}

