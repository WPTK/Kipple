import type { Feed, Folder, Scope } from "@/api/types";
import { feedsInFolder } from "@/lib/folderTree";
import { whenLabel } from "@/lib/format";
import { visibleFeeds } from "@/lib/visibleFeeds";

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
  erroring: { label: "Having trouble", tone: "warn", hint: "Kipple can't fetch this feed. It keeps trying, with longer waits." },
  failing: { label: "Failing", tone: "bad", hint: "Failed 14 or more times in a row. Check the address." },
  dead: { label: "Gone", tone: "bad", hint: "The site says this feed no longer exists." },
  disabled: { label: "Paused", tone: "muted", hint: "Kipple isn't checking this feed." },
  archive: { label: "Archive", tone: "muted", hint: "Holds starred articles from deleted feeds." },
};

export const statusInfo = (s: string): StatusInfo => T[s] ?? { label: s, tone: "muted", hint: "" };

/** Sort rank: worst first. */
export const STATUS_RANK: Record<string, number> = { dead: 0, failing: 1, erroring: 2, redirecting: 3, throttled: 4, disabled: 5, silent: 6, ok: 7, archive: 8 };



/** A feed's name where it is shown by name: its title, or a plain stand-in for a feed whose title is empty. */
export const feedName = (feed: { title?: string | null }): string => feed.title || "Unnamed feed";

/**
 * Why a fetch failed, in a reader's words. The server sends the kind of failure (`last_error_class`) with its own text;
 * the text is for Feed health's Details and never shown as the reason. An HTTP failure is told by its status code.
 */
export function failureReason(feed: { last_error?: string | null; last_error_class?: string | null }): string {
  switch (feed.last_error_class) {
    case "timeout":
      return "The site took too long to answer";
    case "dns":
      return "This address does not exist";
    case "connect":
      return "Kipple can't connect to this address";
    case "tls":
      return "The site's security certificate isn't valid";
    case "cloudflare":
      return "The site blocked Kipple";
    case "too_large":
      return "The feed is too big for Kipple to read";
    case "empty":
      return "The site sent an empty reply";
    case "parse":
      return "The reply isn't a feed";
    case "ssrf":
      return "Kipple is not allowed to fetch this address";
    case "redirect_loop":
      return "The address keeps redirecting";
    case "gone":
      return "The site says this feed no longer exists";
    case "http": {
      const code = Number(/HTTP (\d{3})/.exec(feed.last_error ?? "")?.[1]);
      if (code === 404 || code === 410) return "The site says this feed isn't there";
      if (code === 401 || code === 403) return "The site won't let Kipple read this feed";
      if (code === 429) return "The site asked Kipple to slow down";
      if (code >= 500) return "The site had a problem answering";
      return "The site answered with an error";
    }
    default:
      return "Kipple couldn't read this feed";
  }
}

/** "Trying again in 1 hour" for a feed's next fetch time (unix seconds); "" when there is none or the feed is off. */
export function retryLabel(next: number | null | undefined, nowMs: number = Date.now()): string {
  if (!next) return "";
  const when = whenLabel(next, nowMs);
  return when.startsWith("in ") ? `Trying again ${when}` : "Trying again shortly";
}

/**
 * Why Kipple can't fetch a feed, in words, for a feed's own empty list. The server sends `last_error` only while the
 * feed is failing; a feed without one has nothing to say. The one name for the state is "can't fetch" (a gone feed
 * says it is gone).
 */
export function failureCopy(feed: { status: string; last_error?: string | null; last_error_class?: string | null }): { title: string; body: string } | null {
  if (!feed.last_error) return null;
  return { title: feed.status === "dead" ? "This feed is gone" : "Kipple can't fetch this feed", body: `${failureReason(feed)}.` };
}

/** The note in a feed's own list: what happened and the reason, then when Kipple tries again (when known). */
export function failureLine(feed: { status: string; last_error?: string | null; last_error_class?: string | null }, retry = ""): string {
  const copy = failureCopy(feed);
  return copy ? [`${copy.title}: ${failureReason(feed)}.`, retry ? `${retry}.` : ""].filter(Boolean).join(" ") : "";
}

/** The same line for a note in a folder's list, which names the feed. */
export function namedFailureLine(feed: { status: string; title?: string | null; last_error?: string | null; last_error_class?: string | null }): string {
  if (!feed.last_error) return "";
  return `${feedName(feed)} ${feed.status === "dead" ? "is gone" : "can't be fetched"}: ${failureReason(feed)}.`;
}

/** The feeds a list shows: its one feed, or every feed in its folder and the folders inside. */
function feedsOf(scope: Scope, feeds: readonly Feed[] | undefined, folders: readonly Folder[] | undefined): Feed[] {
  const all = visibleFeeds(feeds);
  if (scope.feed) return all.filter((f) => f.id === scope.feed);
  if (scope.folder) return feedsInFolder(scope.folder, all, folders ?? []);
  return [];
}

/**
 * Whether a feed's failure is current for a list: the one rule for the empty list and the note above the articles. The
 * server sends `last_error` only while the feed is failing. In an unread list, new articles waiting from that feed
 * (`pending`) mean a fetch has worked since, so the old failure is not shown for it.
 */
export function showsFailure(view: string, pending: number, feed: { last_error?: string | null }): boolean {
  return !!feed.last_error && !(view === "unread" && pending > 0);
}

/**
 * The note above a list of articles (issue #511): the feeds in it that can't be fetched, worst first, or none when it
 * should not show. One decision for the whole note: only a loaded list of articles of a feed or folder (not a search,
 * Starred or Muted), not a feed's empty list (that says it in its own box), and only failures that are current
 * (each judged by the new articles waiting from its own feed).
 */
export function failureNoteFeeds(o: { scope: Scope; pendingOf: (feedId: string) => number; feeds: readonly Feed[] | undefined; folders: readonly Folder[] | undefined; loaded: boolean; empty: boolean }): Feed[] {
  const { scope } = o;
  if (!o.loaded || scope.q || (scope.view !== "unread" && scope.view !== "all") || (scope.feed && o.empty)) return [];
  return feedsOf(scope, o.feeds, o.folders)
    .filter((f) => showsFailure(scope.view, o.pendingOf(f.id), f))
    .sort((a, b) => (STATUS_RANK[a.status] ?? 99) - (STATUS_RANK[b.status] ?? 99));
}

/** The text of the note: one feed in words, or a folder's count once with up to three names. */
export function failureNoteText(failing: readonly Feed[], inFolder: boolean): string {
  const one = failing[0];
  if (!one) return "";
  if (!inFolder || failing.length === 1) return inFolder ? namedFailureLine(one) : failureLine(one);
  const names = failing.slice(0, 3).map(feedName);
  const more = failing.length - names.length;
  return `${failing.length} feeds in this folder have fetch problems: ${names.join(", ")}${more > 0 ? ` and ${more} more` : ""}.`;
}
