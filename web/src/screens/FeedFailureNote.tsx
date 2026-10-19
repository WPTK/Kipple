import { useMemo } from "react";
import { Link } from "react-router";
import type { Feed, Folder, Scope } from "@/api/types";
import { failureCopy } from "@/lib/feedStatus";
import { folderTree, subtreeOf } from "@/lib/folderTree";
import { visibleFeeds } from "@/lib/visibleFeeds";

/** The feeds whose last fetch failed that a list shows: its one feed, or every feed in its folder and the folders inside. */
export function failingFeedsIn(scope: Scope, feeds: readonly Feed[] | undefined, folders: readonly Folder[] | undefined): Feed[] {
  const all = visibleFeeds(feeds);
  if (scope.feed) return all.filter((f) => f.id === scope.feed && f.last_error);
  if (scope.folder) {
    const inside = subtreeOf(folderTree(folders ?? []), scope.folder);
    return all.filter((f) => inside.has(f.folder_id) && f.last_error);
  }
  return [];
}

/**
 * A note above a feed's or folder's list when a feed in it can't be fetched, so older articles are not mistaken for
 * current ones (issue #511). It sits outside the virtualized rows. The reason is the empty list's own wording
 * (`failureCopy`); a feed with new articles waiting has fetched since, so it stays quiet (as the empty list does).
 */
export function FeedFailureNote({ scope, feeds, folders, pendingNew }: { scope: Scope; feeds: readonly Feed[] | undefined; folders: readonly Folder[] | undefined; pendingNew: number }) {
  const failing = useMemo(() => failingFeedsIn(scope, feeds, folders), [scope, feeds, folders]);
  if (failing.length === 0 || (scope.feed && scope.view === "unread" && pendingNew > 0)) return null;
  const first = failing[0]!;
  const one = scope.feed ? failureCopy(first) : null;
  return (
    <div role="status" data-testid="feed-failure-note" className="mx-3 mb-2 rounded-xl border border-line bg-surface px-3 py-2 text-sm text-fg">
      {one ? (
        <p>
          <strong className="font-semibold">{one.title}.</strong> <span className="text-fg2 break-words">{one.body}</span>
        </p>
      ) : (
        <p>
          <strong className="font-semibold">{failing.length === 1 ? "1 feed in this folder can't be fetched." : `${failing.length} feeds in this folder can't be fetched.`}</strong>{" "}
          <span className="text-fg2 break-words">
            {first.title}: {first.last_error}
            {failing.length > 1 ? ` (and ${failing.length - 1} more)` : ""}
          </span>
        </p>
      )}
      <Link to="/health" className="hit mt-1 inline-flex items-center text-link underline underline-offset-2">
        Open Feed health
      </Link>
    </div>
  );
}
