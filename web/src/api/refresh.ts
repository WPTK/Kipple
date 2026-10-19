import { useIsMutating, useMutation } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { ApiError, api, errorMessage } from "./client";
import { isRefreshKind, liveStore, seedRun } from "./events";
import { announce, toast } from "@/shell/toasts";

export const CANT_REFRESH_OFFLINE = "Can't refresh while offline";

const REFRESH_ALL = ["refresh-all"];

/** Manual refresh: fetch every feed now. The result arrives as run.* events. */
export function useRefreshAll() {
  return useMutation({
    mutationKey: REFRESH_ALL,
    mutationFn: () => api<{ run_id: string; total: number; joined?: boolean; event_id?: string }>("/api/refresh", { method: "POST" }),
    onSuccess: (r) => {
      // The run is shown from the answer, not from its run.start event (which can come later, or not while the stream is down).
      seedRun({ id: String(r.run_id), kind: "manual", done: 0, total: r.total, new_items: 0, errors: 0 }, r.event_id);
      announce("Refreshing");
    },
    // A failed connection with the browser offline, worded as the pull gesture words it.
    onError: (e) => toast(e instanceof ApiError && e.status === 0 && typeof navigator !== "undefined" && navigator.onLine === false ? CANT_REFRESH_OFFLINE : errorMessage(e), "error"),
  });
}

/**
 * True from the moment a refresh is asked for until its run ends, and while an import run is active (the retention
 * sweep does not count). The request itself counts: on a slow connection the run starts only once it arrives, and the
 * button must show it is working from the tap.
 */
export function useRefreshing(): boolean {
  const asking = useIsMutating({ mutationKey: REFRESH_ALL }) > 0;
  const running = useSyncExternalStore(liveStore.subscribe, () => Object.values(liveStore.get().runs).some((r) => isRefreshKind(r.kind)));
  return asking || running;
}
