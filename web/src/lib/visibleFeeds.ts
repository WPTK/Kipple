import type { Feed } from "@/api/types";

/**
 * The feeds a reader sees: every feed except the archive feed ("Unsubscribed (starred)"), which only holds
 * starred articles of unsubscribed feeds. An unsubscribed feed shows up nowhere; its starred articles stay in
 * Starred and search. The server already leaves the archive feed out of the bootstrap (listedFeedSQL); this
 * keeps every list right with a bootstrap cached before that (offline, or an older server).
 */
export function visibleFeeds(feeds: readonly Feed[] | undefined): Feed[] {
  return (feeds ?? []).filter((f) => !f.is_archive);
}
