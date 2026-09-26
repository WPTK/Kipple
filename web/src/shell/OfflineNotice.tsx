import { CloudOff, LogIn, RefreshCw } from "lucide-react";
import { offlineStore, type OfflineState } from "@/lib/offlineState";
import { useStore } from "@/lib/store";

const bar = "pt-safe flex shrink-0 items-center gap-2 border-b border-line bg-surface px-4 py-2 text-sm";

type Notice = { text: string; icon: "offline" | "update" | "session"; reload: boolean };

function noticeFor({ online, pending, updateReady, sessionExpired }: OfflineState): Notice | null {
  if (sessionExpired) return { text: "Your sign-in has expired. Reload Kipple to sign in again.", icon: "session", reload: true };
  if (updateReady) return { text: "A newer version of Kipple is ready.", icon: "update", reload: true };
  if (online && pending === 0) return null;
  const changes = pending === 1 ? "1 change" : `${pending} changes`;
  const text = online
    ? `Sending ${changes} made while offline.`
    : pending > 0
      ? `You're offline. Reading what's on this device; ${changes} will be sent when you're back.`
      : "You're offline. Reading what's on this device.";
  return { text, icon: "offline", reload: false };
}

/**
 * One line above the app while it is offline, while offline changes are being sent, when a newer build is ready or
 * when the sign-in in front of Kipple expired. The message sits in a polite live region that is always mounted
 * (visually hidden while there is nothing to say): a region that appears with its text already inside is often not
 * announced (VoiceOver). The Reload button is outside the region, so it is not read out with every change.
 */
export function OfflineNotice() {
  const n = noticeFor(useStore(offlineStore));
  return (
    <div className={n ? bar : "sr-only-live"} data-testid="offline-notice">
      {n?.icon === "offline" ? <CloudOff aria-hidden="true" className="size-4 shrink-0 text-fg2" /> : null}
      {n?.icon === "update" ? <RefreshCw aria-hidden="true" className="size-4 shrink-0 text-accent" /> : null}
      {n?.icon === "session" ? <LogIn aria-hidden="true" className="size-4 shrink-0 text-danger" /> : null}
      <p role="status" aria-live="polite" aria-atomic="true" className="min-w-0 flex-1">
        {n?.text ?? ""}
      </p>
      {n?.reload ? (
        <button type="button" className="hit rounded-lg px-2 font-medium text-link underline underline-offset-2" onClick={() => window.location.reload()}>
          Reload
        </button>
      ) : null}
    </div>
  );
}
