import type { Feed, Folder, Scope } from "@/api/types";
import { folderTree, subtreeOf } from "@/lib/folderTree";
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
export function failureCopy(feed: { status: string; title?: string; last_error?: string | null }): { title: string; body: string; line: string } | null {
  if (!feed.last_error) return null;
  const dead = feed.status === "dead";
  const erroring = feed.status === "erroring";
  const title = dead ? "This feed is gone" : erroring ? "The last fetch of this feed failed" : "Kipple can't fetch this feed";
  // The title already says the last fetch failed; the body says what happens next and why.
  const next = erroring ? "Kipple will retry with longer waits." : statusInfo(feed.status).hint;
  // `line` is one short line for a note: what happened and the reason, in the feed's own list or named in a folder's.
  const named = `${feed.title ?? "A feed"} ${dead ? "is gone" : erroring ? "failed its last fetch" : "can't be fetched"}`;
  return { title, body: `${next} Last error: ${feed.last_error}`, line: `${feed.title === undefined ? title : named}: ${feed.last_error}` };
}

/** The feeds a list shows: its one feed, or every feed in its folder and the folders inside. */
function feedsOf(scope: Scope, feeds: readonly Feed[] | undefined, folders: readonly Folder[] | undefined): Feed[] {
  const all = visibleFeeds(feeds);
  if (scope.feed) return all.filter((f) => f.id === scope.feed);
  if (scope.folder) {
    const inside = subtreeOf(folderTree(folders ?? []), scope.folder);
    return all.filter((f) => inside.has(f.folder_id));
  }
  return [];
}

/**
 * Whether a feed's failure is current for a list: the one rule for the empty list and the note above the articles. The
 * server sends `last_error` only while the feed is failing. In a feed's unread list, new articles waiting mean a fetch
 * has worked since. A folder's waiting articles can't be traced to one feed, so they say nothing about its feeds.
 */
export function showsFailure(scope: { view: string; feed?: string; folder?: string }, pendingNew: number, feed: { last_error?: string | null }): boolean {
  return !!feed.last_error && !(scope.feed && scope.view === "unread" && pendingNew > 0);
}

/**
 * The note above a list of articles (issue #511): the feeds in it that can't be fetched, worst first, or none when it
 * should not show. One decision for the whole note: only a loaded list of articles of a feed or folder (not a search,
 * Starred or Muted), not a feed's empty list (that says it in its own box), and only failures that are current.
 */
export function failureNoteFeeds(o: { scope: Scope; pendingNew: number; feeds: readonly Feed[] | undefined; folders: readonly Folder[] | undefined; loaded: boolean; empty: boolean }): Feed[] {
  const { scope } = o;
  if (!o.loaded || scope.q || (scope.view !== "unread" && scope.view !== "all") || (scope.feed && o.empty)) return [];
  return feedsOf(scope, o.feeds, o.folders)
    .filter((f) => showsFailure(scope, o.pendingNew, f))
    .sort((a, b) => (STATUS_RANK[a.status] ?? 99) - (STATUS_RANK[b.status] ?? 99));
}

/** The text of the note: one feed in words, or a folder's count once with up to three names. */
export function failureNoteText(failing: readonly Feed[], inFolder: boolean): string {
  const one = failing[0];
  if (!one) return "";
  if (!inFolder || failing.length === 1) return failureCopy(inFolder ? one : { status: one.status, last_error: one.last_error })?.line ?? "";
  const names = failing.slice(0, 3).map((f) => f.title);
  const more = failing.length - names.length;
  return `${failing.length} feeds in this folder have fetch problems: ${names.join(", ")}${more > 0 ? ` and ${more} more` : ""}.`;
}
