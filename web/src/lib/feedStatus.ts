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

/**
 * Why Kipple can't fetch a feed, in words: the one source for the empty list and the note above a list that still has
 * articles. The server sends `last_error` only while the feed is failing; a feed without one has nothing to say.
 */
export function failureCopy(feed: { status: string; last_error?: string | null }): { title: string; body: string; short: string } | null {
  if (!feed.last_error) return null;
  const dead = feed.status === "dead";
  const erroring = feed.status === "erroring";
  const title = dead ? "This feed is gone" : erroring ? "The last fetch of this feed failed" : "Kipple can't fetch this feed";
  // The title already says the last fetch failed; the body says what happens next and why.
  const next = erroring ? "Kipple will retry with longer waits." : statusInfo(feed.status).hint;
  // `short` follows a feed's name where the title would not read (a folder's note).
  return { title, body: `${next} Last error: ${feed.last_error}`, short: dead ? "is gone" : erroring ? "failed its last fetch" : "can't be fetched" };
}

/**
 * Whether a feed's failure is current for a list: the one rule for the empty list and the note above the articles. The
 * server sends `last_error` only while the feed is failing. In a feed's unread list, new articles waiting mean a fetch
 * has worked since. A folder's waiting articles can't be traced to one feed, so they say nothing about its feeds.
 */
export function showsFailure(scope: { view: string; feed?: string; folder?: string }, pendingNew: number, feed: { last_error?: string | null }): boolean {
  return !!feed.last_error && !(scope.feed && scope.view === "unread" && pendingNew > 0);
}
