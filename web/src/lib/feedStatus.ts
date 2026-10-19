// Feed health statuses (docs/design.md 4.6) in plain English. The label always carries the state, never
// color alone; the icon name picks a lucide icon in the UI.

export type StatusTone = "ok" | "warn" | "bad" | "muted";

export interface StatusInfo {
  label: string;
  tone: StatusTone;
  /** One sentence for the health view. */
  hint: string;
}

const T: Record<string, StatusInfo> = {
  ok: { label: "Working", tone: "ok", hint: "Fetching normally." },
  silent: { label: "Quiet for 3 months", tone: "muted", hint: "Working, but nothing new for 90 days." },
  redirecting: { label: "Moved", tone: "warn", hint: "The site says this feed has a new address." },
  throttled: { label: "Waiting", tone: "warn", hint: "The site asked Kipple to slow down, so this feed is paused for a while." },
  erroring: { label: "Having trouble", tone: "warn", hint: "The last fetch failed. Kipple will retry with longer waits." },
  failing: { label: "Failing", tone: "bad", hint: "Failed 14 or more times in a row. Check the address." },
  dead: { label: "Gone", tone: "bad", hint: "The site says this feed no longer exists." },
  disabled: { label: "Turned off", tone: "muted", hint: "Kipple isn't checking this feed." },
  archive: { label: "Archive", tone: "muted", hint: "Holds starred articles from deleted feeds." },
};

export const statusInfo = (s: string): StatusInfo => T[s] ?? { label: s, tone: "muted", hint: "" };

/** Sort rank: worst first. */
export const STATUS_RANK: Record<string, number> = { dead: 0, failing: 1, erroring: 2, redirecting: 3, throttled: 4, disabled: 5, silent: 6, ok: 7, archive: 8 };
