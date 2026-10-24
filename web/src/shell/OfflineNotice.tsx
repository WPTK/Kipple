import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ChevronDown, CloudOff, LogIn, RefreshCw } from "lucide-react";
import { CAUSE_PHRASE } from "@/api/client";
import { findCached } from "@/api/queries";
import { reloadForUpdate } from "@/lib/buildInfo";
import { queuedChanges, type Queued } from "@/lib/offline";
import { browserOnlineStore, offlineStore, type OfflineState } from "@/lib/offlineState";
import { reloadToSignIn } from "@/lib/reload";
import { useStore } from "@/lib/store";

type Notice = { text: string; icon: "offline" | "update" | "session"; reload: boolean; changes: boolean };

/** Why Kipple is not answering: the browser says it has no network, or it has one and Kipple does not answer. */
function lead(browserOnline: boolean): string {
  return browserOnline ? CAUSE_PHRASE.unreachable : CAUSE_PHRASE.offline;
}

function noticeFor({ online, pending, updateReady, sessionExpired }: OfflineState, browserOnline: boolean): Notice | null {
  if (sessionExpired) return { text: "Your sign-in has expired. Reload Kipple to sign in again.", icon: "session", reload: true, changes: false };
  if (updateReady) return { text: "A newer version of Kipple is ready.", icon: "update", reload: true, changes: false };
  if (online && pending === 0) return null;
  const changes = pending === 1 ? "1 change" : `${pending} changes`;
  const text = online
    ? `Sending ${changes} made while offline.`
    : pending > 0
      ? `${lead(browserOnline)}. Reading what's on this device; ${changes} will be sent when you're back.`
      : `${lead(browserOnline)}. Reading what's on this device.`;
  return { text, icon: "offline", reload: false, changes: pending > 0 };
}

/** One queued change in words: "Starred Ember", "Marked 3 articles read". */
function describe(q: Queued, title: (id: string) => string | undefined): string {
  if (q.kind === "star") return `${q.starred ? "Starred" : "Unstarred"} ${title(q.id) ?? "an article"}`;
  const state = q.read ? "read" : "unread";
  const [only] = q.ids;
  return q.ids.length === 1 && only ? `Marked ${title(only) ?? "an article"} ${state}` : `Marked ${q.ids.length} articles ${state}`;
}

/** The changes waiting to be sent, named. Read when opened, and again when the count moves. */
function Changes({ pending }: { pending: number }) {
  const qc = useQueryClient();
  const [rows, setRows] = useState<string[] | null>(null);
  useEffect(() => {
    let live = true;
    void queuedChanges().then((q) => {
      if (live) setRows(q.map((c) => describe(c, (id) => findCached(qc, id)?.title)));
    });
    return () => {
      live = false;
    };
  }, [qc, pending]);
  return (
    <ul className="w-full list-disc pl-9 text-fg2" data-testid="offline-changes">
      {(rows ?? []).map((r, i) => (
        <li key={i}>{r}</li>
      ))}
    </ul>
  );
}

/**
 * One pill over the app (in the toast region, so it stands above the tab bar and never moves the page) while it is
 * offline, while offline changes are being sent, when a newer build is ready or when the sign-in in front of Kipple
 * expired. While changes wait it can list them. The message sits in a polite live region that is always mounted
 * (visually hidden while there is nothing to say): a region that appears with its text already inside is often not
 * announced (VoiceOver). The buttons are outside the region, so they are not read out with every change.
 */
export function OfflineNotice() {
  const state = useStore(offlineStore);
  const browserOnline = useStore(browserOnlineStore);
  const n = noticeFor(state, browserOnline);
  const [open, setOpen] = useState(false);
  // The list keeps more room at its end while the pill stands over it (screens/ListPane.tsx).
  const showingPill = n !== null;
  useEffect(() => {
    document.documentElement.toggleAttribute("data-pill", showingPill);
    return () => document.documentElement.removeAttribute("data-pill");
  }, [showingPill]);
  const showing = open && n?.changes === true;
  return (
    <div
      className={n ? "pointer-events-auto flex min-h-11 w-full max-w-[min(28rem,calc(var(--list-w,100vw)-2rem))] flex-wrap items-center gap-x-2 gap-y-1 rounded-xl border border-line bg-surface px-3 py-2 text-sm shadow-md" : "sr-only-live"}
      data-testid="offline-notice"
    >
      {n?.icon === "offline" ? <CloudOff aria-hidden="true" className="size-4 shrink-0 text-fg2" /> : null}
      {n?.icon === "update" ? <RefreshCw aria-hidden="true" className="size-4 shrink-0 text-accent" /> : null}
      {n?.icon === "session" ? <LogIn aria-hidden="true" className="size-4 shrink-0 text-danger" /> : null}
      <p role="status" aria-live="polite" aria-atomic="true" className="min-w-0 flex-1">
        {n?.text ?? ""}
      </p>
      {n?.changes ? (
        <button
          type="button"
          aria-expanded={showing}
          className="hit inline-flex items-center gap-1 rounded-lg px-2 font-medium text-link underline underline-offset-2"
          onClick={() => setOpen((v) => !v)}
        >
          {showing ? "Hide" : "Show"} changes
          <ChevronDown aria-hidden="true" className={`size-4 transition-transform ${showing ? "rotate-180" : ""}`} />
        </button>
      ) : null}
      {n?.reload ? (
        <button
          type="button"
          className="hit rounded-lg px-2 font-medium text-link underline underline-offset-2"
          onClick={() => (n.icon === "session" ? reloadToSignIn() : n.icon === "update" ? void reloadForUpdate() : window.location.reload())}
        >
          Reload
        </button>
      ) : null}
      {showing ? <Changes pending={state.pending} /> : null}
    </div>
  );
}
