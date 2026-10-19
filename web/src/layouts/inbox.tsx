import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { ListImage, useImageFailure, PublishedTime, RowMenu, StarButton, UnreadDot } from "./parts";
import type { ListLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";
import { useRowTabIndex, ROW_KEYSHORTCUTS } from "./rowTabStop";

/**
 * Inbox (email style): the feed is the sender (bold while unread), the title
 * is the subject, then a snippet, with the time right-aligned and an optional
 * thumbnail. On a wide screen it sits in the three-pane shell beside the reader.
 * Unread is a dot AND weight, never color alone.
 */
function InboxRow({ item, selected, to, onOpen, onToggleStar, actions, showThumb = true }: RowProps) {
  const tabIndex = useRowTabIndex();
  const unread = !item.read;
  const thumb = showThumb && item.image;
  const failure = useImageFailure(item.image);
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host relative flex min-h-[var(--row-min)] gap-2 border-b border-line bg-bg py-[var(--row-py)] pr-4 pl-2",
        "@container",
        selected ? "bg-selection" : "hover:bg-surface",
      )}
    >
      <div className="flex w-3 shrink-0 justify-center pt-1.5">
        <UnreadDot unread={unread} />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-[var(--row-gap)]">
        <div className="flex items-baseline gap-2">
          <span className={cn("min-w-0 flex-1 truncate text-sm", unread ? "font-bold text-fg" : "font-medium text-fg2")}>{item.source}</span>
          <PublishedTime item={item} className="shrink-0 text-xs text-fg2 tabular-nums" />
        </div>
        <h3 className={cn("truncate text-sm", unread ? "font-semibold text-fg" : "font-normal text-fg2")}>
          <Link
            tabIndex={tabIndex}
            to={to}
            onClick={() => onOpen(item)}
            aria-keyshortcuts={ROW_KEYSHORTCUTS}
            data-row-unread={item.read ? undefined : ""}
            className="row-link"
          >
            <Hl text={item.title || "Untitled"} field="title" feedId={item.feed_id} />
          </Link>
        </h3>
        {item.excerpt ? <p className="clamp-snippet text-sm leading-snug text-fg2"><Hl text={item.excerpt} field="content" feedId={item.feed_id} /></p> : null}
      </div>
      <div className="pointer-events-none relative z-10 flex shrink-0 flex-col items-end justify-between gap-1">
        {thumb ? (
          <ListImage src={item.image!} failure={failure} className={cn("size-14 rounded-lg bg-surface object-cover @max-[20rem]:hidden", item.read && "opacity-60")} />
        ) : null}
        <div className="-mr-2 flex items-center">
          <StarButton item={item} onToggleStar={onToggleStar} />
          <RowMenu item={item} actions={actions} revealOnHover />
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
