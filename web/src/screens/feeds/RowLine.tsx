import { Folder as FolderIcon } from "lucide-react";
import type { Feed } from "@/api/types";
import { cn } from "@/lib/cn";
import { UnreadCount } from "@/ui/UnreadCount";
import { FeedRowStatus } from "../StatusChip";
import { FeedRowName } from "./FeedRowName";

/** A feed's icon, or a blank of the same size so names line up. */
function FeedIcon({ feed }: { feed: Pick<Feed, "icon"> }) {
  return feed.icon ? (
    <img src={feed.icon} alt="" width={16} height={16} loading="lazy" className="size-4 shrink-0 rounded-sm" />
  ) : (
    <span className="size-4 shrink-0" aria-hidden="true" />
  );
}

/**
 * What a feed row says: its icon, its name (dimmed when nothing is unread), optionally its status, and its unread count.
 * The sidebar tree and the Feeds screen both draw their feed rows with this, so a count, the "Unread badge" setting and
 * the dimming are the same on both. The trailing space in the name keeps "Feed" and its count apart in the text a
 * checker reads; it sits inside the name because a space in the zero-width spacer is skipped.
 */
export function FeedLine({ feed, status }: { feed: Feed; status?: boolean }) {
  return (
    <>
      <FeedIcon feed={feed} />
      <span className={cn("min-w-0", feed.unread <= 0 && "text-fg2")}>
        <FeedRowName feed={feed} />{" "}
        {status ? <FeedRowStatus status={feed.status} error={feed.last_error} /> : null}
      </span>
      <span className="ml-auto" />
      <UnreadCount n={feed.unread} />
    </>
  );
}

/** What a folder row says: its name and the unread count of its whole subtree (a collapsed folder still shows what is inside it). */
export function FolderLine({ name, unread, icon }: { name: string; unread: number; icon?: boolean }) {
  return (
    <>
      {icon ? <FolderIcon aria-hidden="true" className="size-4 shrink-0 text-fg2" /> : null}
      <span className={cn("truncate", unread <= 0 && "text-fg2")}>{name}{" "}</span>
      <span className="ml-auto" />
      <UnreadCount n={unread} />
    </>
  );
}
