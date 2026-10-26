import { ApiError } from "@/api/client";
import type { Feed, Scope } from "@/api/types";
import { failureCopy, showsFailure } from "@/lib/feedStatus";

/** What an empty list says, and the one next step it offers: show everything, or add a feed. */
export interface EmptyCopy {
  title: string;
  body: string;
  health?: boolean;
  next?: "all" | "add";
}

/**
 * What an empty list says. `feed` is the one feed a feed list shows: a failing feed says why instead of "All caught up"
 * (issue #476). `scopeFeeds` is how many feeds the list covers (a folder with none cannot have articles), so the reason
 * given is the real one, not the view's.
 */
export function emptyCopy(scope: Scope, pendingNew = 0, feed?: Feed, scopeFeeds?: number): EmptyCopy {
  if (scope.q) {
    // Only a search narrowed to a feed or folder has a wider place to try (the chip on the Search screen widens it).
    const narrowed = scope.feed || scope.folder;
    return { title: `No results for "${scope.q}"`, body: narrowed ? "Try fewer words, or search the whole library." : "Try fewer words." };
  }
  if (scope.after) return { title: "Nothing new", body: "Nothing has arrived since you last looked. Unread has everything you have not read yet." };
  if (scope.view === "muted") return { title: "Nothing muted", body: "No filter has muted an article yet." };
  if (scope.view === "starred") return { title: "No starred articles", body: "Tap the star on an article to keep it here." };
  if (scope.folder && scopeFeeds === 0) return { title: "This folder has no feeds", body: "Move a feed here or add one.", next: "add" };
  // The server sends last_error only while the feed is failing. New articles waiting mean a fetch has worked since.
  const failure = feed && showsFailure(scope.view, pendingNew, feed) ? failureCopy(feed) : null;
  if (failure) return { title: failure.title, body: failure.body, health: true };
  if (scope.view === "unread") {
    return pendingNew > 0
      ? { title: "All caught up", body: `${pendingNew} new article${pendingNew === 1 ? "" : "s"} arrived. Load ${pendingNew === 1 ? "it" : "them"} above to continue reading.` }
      : { title: "All caught up", body: "No unread articles. New ones appear after the next refresh.", next: "all" };
  }
  return { title: "No articles yet", body: "Kipple hasn't fetched anything from these feeds yet." };
}

/** The line under the last row of a list that has been read to its end. Worded like the empty state: it names the list. */
export function endCopy(scope: Scope): string {
  if (scope.q) return "End of results";
  if (scope.after) return "End of new articles";
  if (scope.view === "muted") return "End of muted";
  if (scope.view === "starred") return "End of starred";
  return scope.view === "unread" ? "End of unread" : "End of articles";
}

/** What a search that came back 422 says: the server's own message, or a plain one. */
export function tooBroadMessage(e: unknown): string | null {
  if (!(e instanceof ApiError) || e.status !== 422 || e.code !== "search_too_broad") return null;
  const m = e.body && typeof e.body.message === "string" ? e.body.message : "";
  return m || "That search matches too much. Add a longer or more specific word.";
}
