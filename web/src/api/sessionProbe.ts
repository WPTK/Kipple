import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { fetchInstance, INSTANCE_KEY } from "@/setup/api";
import { authStore, setSignedIn } from "./client";

/**
 * Settles whether this browser is signed in before the app asks for anything that needs a session. A request that
 * answers 401 is logged to the console by the browser itself, so a signed-out launch must not send one: GET
 * /api/instance answers 200 either way and says whether the session is live (signed_in). A signed-out answer is also
 * the data the sign-in or setup screen shows, so it is kept for them.
 *
 * Returns true once that is settled. When the question cannot be answered (offline, a proxy page in the way) the auth
 * state stays unknown and the first request that needs a session decides, as it always did; the service worker may
 * answer that one from its stored copy.
 */
export function useSessionProbe(): boolean {
  const qc = useQueryClient();
  const [settled, setSettled] = useState(() => authStore.get() !== "unknown");
  useEffect(() => {
    if (settled) return;
    const ctl = new AbortController();
    fetchInstance(ctl.signal).then(
      (info) => {
        if (ctl.signal.aborted) return;
        if (typeof info?.signed_in === "boolean") {
          if (info.signed_in) setSignedIn();
          else {
            qc.setQueryData(INSTANCE_KEY, info);
            authStore.set("out");
          }
        }
        setSettled(true);
      },
      () => {
        if (!ctl.signal.aborted) setSettled(true);
      },
    );
    return () => ctl.abort();
  }, [settled, qc]);
  return settled;
}
