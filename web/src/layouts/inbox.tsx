import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { ListImage, useImageFailure, RowMenu, SourceTimeMeta, StarButton, UnreadDot, rowLinkProps, rowState, titleTone } from "./parts";
import type { ListLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";
import { useRowTabIndex, ROW_KEYSHORTCUTS } from "./rowTabStop";

/**
 * Inbox (email style): the title is the subject, in up to two lines; under it the source and time, then a snippet,
 * with an optional thumbnail beside. In a list of one feed the source is left out. On a wide screen it sits in the
 * three-pane shell beside the reader. Unread is a dot AND a bold title, never color alone.
 */
export function InboxRow({ item, feed, selected, to, onOpen, onToggleStar, actions, showThumb = true, snippetLines }: RowProps & { snippetLines?: number }) {
  const tabIndex = useRowTabIndex();
  const unread = !item.read;
  const thumb = showThumb && item.image;
  const failure = useImageFailure(item.image);
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn("row-host relative flex min-h-[var(--row-min)] gap-2 border-b border-line bg-bg py-[var(--row-py)] pr-4 pl-2", "@container", rowState(selected))}
    >
      <div className="flex w-3 shrink-0 justify-center pt-1.5">
        <UnreadDot unread={unread} />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-[var(--row-gap)]">
        <h3 className={cn("line-clamp-2 min-w-0 text-sm leading-snug [overflow-wrap:anywhere]", titleTone(unread))}>
          <Link
            tabIndex={tabIndex}
            to={to}
            onClick={() => onOpen(item)}
            aria-keyshortcuts={ROW_KEYSHORTCUTS}
            {...rowLinkProps(item)}
          >
            <Hl text={item.title || "Untitled"} field="title" feedId={item.feed_id} />
          </Link>
        </h3>
        <SourceTimeMeta item={item} feed={feed} />
        {item.excerpt ? (
          <p className={cn("text-sm leading-snug text-fg2 [overflow-wrap:anywhere]", snippetLines === 1 ? "line-clamp-1" : "clamp-snippet")}>
            <Hl text={item.excerpt} field="content" feedId={item.feed_id} />
          </p>
        ) : null}
      </div>
      <div className="pointer-events-none relative z-10 flex shrink-0 flex-col items-end justify-between gap-1">
        {thumb ? (
          <ListImage src={item.image!} failure={failure} className={cn("size-14 rounded-lg bg-surface @max-[20rem]:hidden", item.read && "opacity-60")} />
        ) : null}
        <div className="-mr-2 flex items-center">
          <StarButton item={item} onToggleStar={onToggleStar} />
          <RowMenu item={item} actions={actions} />
        </div>
      </div>
    </article>
  );
}

export const inbox: ListLayout = {
  id: "inbox",
  label: "Inbox",
  Row: InboxRow,
  estimateRow: () => 92,
  paneRem: 23.75,
};