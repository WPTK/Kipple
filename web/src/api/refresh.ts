import { useMutation } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { api, errorMessage } from "./client";
import { isRefreshKind, liveStore } from "./events";
import { announce, toast } from "@/shell/toasts";

/** Manual refresh: fetch every feed now. The result arrives as run.* events. */
export function useRefreshAll() {
  return useMutation({
    mutationFn: () => api<{ run_id: string; total: number; joined?: boolean }>("/api/refresh", { method: "POST" }),
    onSuccess: () => announce("Refreshing"),
    onError: (e) => toast(errorMessage(e), "error"),
  });
}

/** True while a refresh or import run is active (the retention sweep does not count). */
export function useRefreshing(): boolean {
  return useSyncExternalStore(liveStore.subscribe, () => Object.values(liveStore.get().runs).some((r) => isRefreshKind(r.kind)));
}
