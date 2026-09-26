import { CloudOff, RefreshCw } from "lucide-react";
import { offlineStore } from "@/lib/offlineState";
import { useStore } from "@/lib/store";

const bar = "pt-safe flex shrink-0 items-center gap-2 border-b border-line bg-surface px-4 py-2 text-sm";

/**
 * One line above the app while it is offline, while offline changes are being sent, or when a newer build is
 * ready. A polite live region, so a screen reader hears it once when it appears.
 */
export function OfflineNotice() {
  const { online, pending, updateReady } = useStore(offlineStore);
  if (updateReady) {
    return (
      <div role="status" className={bar}>
        <RefreshCw aria-hidden="true" className="size-4 shrink-0 text-accent" />
        <p className="min-w-0 flex-1">A newer version of Kipple is ready.</p>
        <button type="button" className="hit rounded-lg px-2 font-medium text-link underline underline-offset-2" onClick={() => window.location.reload()}>
          Reload
        </button>
      </div>
    );
  }
  if (online && pending === 0) return null;
  const changes = pending === 1 ? "1 change" : `${pending} changes`;
  return (
    <div role="status" className={bar}>
      <CloudOff aria-hidden="true" className="size-4 shrink-0 text-fg2" />
      <p className="min-w-0 flex-1">
        {online
          ? `Sending ${changes} made while offline.`
          : pending > 0
            ? `You're offline. Reading what's on this device; ${changes} will be sent when you're back.`
            : "You're offline. Reading what's on this device."}
      </p>
    </div>
  );
}
