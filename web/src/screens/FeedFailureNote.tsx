import { useMemo } from "react";
import { Link } from "react-router";
import type { Feed, Folder, Scope } from "@/api/types";
import { STATUS_RANK, failureCopy, showsFailure } from "@/lib/feedStatus";
import { folderTree, subtreeOf } from "@/lib/folderTree";
import { visibleFeeds } from "@/lib/visibleFeeds";

/** The feeds a list shows whose failure is current (`showsFailure`): its one feed, or every feed in its folder and the folders inside. Worst first. */
export function failingFeedsIn(scope: Scope, pendingNew: number, feeds: readonly Feed[] | undefined, folders: readonly Folder[] | undefined): Feed[] {
  const all = visibleFeeds(feeds);
  let mine: Feed[] = [];
  if (scope.feed) mine = all.filter((f) => f.id === scope.feed);
  else if (scope.folder) {
    const inside = subtreeOf(folderTree(folders ?? []), scope.folder);
    mine = all.filter((f) => inside.has(f.folder_id));
  }
  return mine.filter((f) => showsFailure(scope, pendingNew, f)).sort((a, b) => (STATUS_RANK[a.status] ?? 99) - (STATUS_RANK[b.status] ?? 99));
}

/**
 * A note above a feed's or folder's list when a feed in it can't be fetched, so older articles are not mistaken for
 * current ones (issue #511). It sits outside the virtualized rows. The reason is the empty list's own wording
 * (`failureCopy`) and the rule for a current failure is the empty list's (`showsFailure`).
 *
 * Not a live region: it is part of the list's page, and announcing the full error on every navigation to the list would
 * repeat a long error the reader already knows. The reason is cut to two lines (the full text is its tooltip and Feed
 * health has it whole) so it can't take over a phone's screen.
 */
export function FeedFailureNote({ scope, feeds, folders, pendingNew }: { scope: Scope; feeds: readonly Feed[] | undefined; folders: readonly Folder[] | undefined; pendingNew: number }) {
  const failing = useMemo(() => failingFeedsIn(scope, pendingNew, feeds, folders), [scope, pendingNew, feeds, folders]);
  const first = failing[0];
  const copy = first ? failureCopy(first) : null;
  if (!first || !copy) return null;
  const text = scope.feed ? `${copy.title}. ${copy.body}` : `${first.title} ${copy.short}: ${first.last_error}${failing.length > 1 ? ` (and ${failing.length - 1} more)` : ""}`;
  return (
    <div data-testid="feed-failure-note" className="mx-3 mb-2 rounded-xl border border-line bg-surface px-3 py-2 text-sm text-fg">
      {scope.feed ? null : <p className="font-semibold">{failing.length === 1 ? "1 feed in this folder has a fetch problem." : `${failing.length} feeds in this folder have fetch problems.`}</p>}
      <p title={text} className="line-clamp-2 text-fg2 break-words">
        {scope.feed ? (
          <>
            <strong className="font-semibold text-fg">{copy.title}.</strong> {copy.body}
          </>
        ) : (
          text
        )}
      </p>
      <Link to="/health" className="hit mt-1 inline-flex items-center text-link underline underline-offset-2">
        Open Feed health
      </Link>
    </div>
  );
}
