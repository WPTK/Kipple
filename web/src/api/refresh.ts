import { useMutation } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { api, errorMessage } from "./client";
import { liveStore } from "./events";
import { announce, toast } from "@/shell/toasts";

/** Manual refresh: fetch every feed now. The result arrives as run.* events. */
export function useRefreshAll() {
  return useMutation({
    mutationFn: () => api<{ run_id: string; total: number; joined?: boolean }>("/api/refresh", { method: "POST" }),
    onSuccess: () => announce("Refreshing"),
    onError: (e) => toast(errorMessage(e), "error"),
  });
}

/** True while any fetch run is active. */
export function useRefreshing(): boolean {
  return useSyncExternalStore(liveStore.subscribe, () => Object.keys(liveStore.get().runs).length > 0);
}
