import { useMutation } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { ApiError, api, errorMessage } from "./client";
import { isRefreshKind, liveStore } from "./events";
import { announce, toast } from "@/shell/toasts";

export const CANT_REFRESH_OFFLINE = "Can't refresh while offline";

/** Manual refresh: fetch every feed now. The result arrives as run.* events. */
export function useRefreshAll() {
  return useMutation({
    mutationFn: () => api<{ run_id: string; total: number; joined?: boolean }>("/api/refresh", { method: "POST" }),
    onSuccess: () => announce("Refreshing"),
    // A failed connection with the browser offline, worded as the pull gesture words it.
    onError: (e) => toast(e instanceof ApiError && e.status === 0 && typeof navigator !== "undefined" && navigator.onLine === false ? CANT_REFRESH_OFFLINE : errorMessage(e), "error"),
  });
}

/** True while a refresh or import run is active (the retention sweep does not count). */
export function useRefreshing(): boolean {
  return useSyncExternalStore(liveStore.subscribe, () => Object.values(liveStore.get().runs).some((r) => isRefreshKind(r.kind)));
}
