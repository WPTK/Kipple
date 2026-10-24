import { Folder as FolderIcon } from "lucide-react";
import type { Feed } from "@/api/types";
import { cn } from "@/lib/cn";
import { FeedIcon } from "@/ui/FeedIcon";
import { UnreadCount } from "@/ui/UnreadCount";
import { FeedRowStatus } from "../StatusChip";
import { FeedRowName } from "./FeedRowName";

/**
 * What a feed row says: its icon, its name (dimmed when nothing is unread), optionally its status, and its unread count.
 * The sidebar tree and the Feeds screen both draw their feed rows with this, so a count, the "Unread badge" setting and
 * the dimming are the same on both. The trailing space in the name keeps "Feed" and its count apart in the text a
 * checker reads; it sits inside the name because a space in the zero-width spacer is skipped.
 */
export function FeedLine({ feed, status }: { feed: Feed; status?: boolean }) {
  return (
    <>
      <FeedIcon src={feed.icon} title={feed.title} />
      <span className={cn("min-w-0", feed.unread <= 0 && "text-fg2")}>
        <FeedRowName feed={feed} />{" "}
        {status ? <FeedRowStatus feed={feed} /> : null}
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
