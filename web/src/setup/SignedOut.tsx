import { Suspense } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError, failureCause } from "@/api/client";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { lazyScreen } from "@/lib/lazyScreen";
import { LoginScreen } from "@/screens/LoginScreen";
import { LoadFailed } from "@/ui/LoadFailed";
import { Skeleton } from "@/ui/kit";
import { fetchInstance, INSTANCE_KEY, openReasonText, type InstanceInfo } from "./api";

// The wizard's signed-out half loads only when Kipple has no account or has no password; everyone else gets the sign-in form.
const SetupFlow = lazyScreen(() => import("./SetupFlow").then((m) => ({ default: m.SetupFlow })));
const OpenSignIn = lazyScreen(() => import("./SetupFlow").then((m) => ({ default: m.OpenSignIn })));

/**
 * What a signed-out browser sees. GET /api/instance says which: the setup wizard (no account yet), a silent sign-in
 * (open mode: no password), or the sign-in form. A server that predates the wizard has no such route; the form is
 * shown then, as it always was.
 *
 * `initial` is the launch probe's answer to the same question, when it is what found the launch signed out: the
 * screen starts from it and does not ask again on mount. Without it the screen asks; either way the answer lives only
 * while the screen does (gcTime 0), so the next signed-out screen asks afresh.
 */
export function SignedOut({ initial }: { initial?: InstanceInfo | null }) {
  const inst = useQuery({
    queryKey: INSTANCE_KEY,
    queryFn: ({ signal }) => fetchInstance(signal),
    initialData: initial ?? undefined,
    refetchOnMount: false,
    retry: false,
    staleTime: 0,
    gcTime: 0,
  });
  // The tab names the one screen of this component that has no screen of its own (the others set theirs).
  const status = inst.error instanceof ApiError ? inst.error.status : -1;
  const unreachable = !inst.isPending && (inst.isError || !inst.data) && !(status === 401 || status === 404 || inst.error instanceof SyntaxError);
  useDocumentTitle(unreachable ? "Kipple couldn't load" : undefined, true);
  if (inst.isPending) {
    return (
      <div className="flex h-full items-center justify-center" role="status">
        <span className="text-fg2">Loading Kipple</span>
      </div>
    );
  }
  if (inst.isError || !inst.data) {
    // Only an answer that says "this server has no such route" (or refuses it) means an older Kipple: the form, as it
    // always was. Anything else (offline, a server error, a proxy refusing the address) says so; a password form there
    // would send a no-password Kipple's owner to a screen that cannot let them in.
    // A 200 that is not JSON (a proxy or an older server answering with its page) is the same "no such route".
    if (status === 401 || status === 404 || inst.error instanceof SyntaxError) return <LoginScreen />;
    return (
      <LoadFailed
        cause={failureCause(inst.error) ?? "other"}
        busy={inst.isFetching}
        onRetry={() => void inst.refetch()}
        why={status === 421 ? openReasonText("host") : undefined}
      />
    );
  }
  if (inst.data.setup) {
    return (
      <Suspense fallback={<Skeleton label="Loading setup" />}>
        <SetupFlow options={inst.data} />
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

