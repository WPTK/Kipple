import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import { CheckBadge, RowMenu, SourceIcon, StarButton, UnreadDot, rowLabel } from "./parts";
import type { ListLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";

/**
 * Compact: unread dot, a small source and time line over a 1-2 line title, no
 * image. The star shows only when starred (or on hover/focus); everything else
 * is in the row menu. Rows are min 44 px on touch (see .row-compact).
 */
function CompactRow({ item, feed, selected, checked, to, onOpen, onToggleStar, actions }: RowProps) {
  const unread = !item.read;
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host row-compact relative flex items-center gap-2 border-b border-line bg-bg px-4",
        selected ? "bg-selection" : "hover:bg-surface",
      )}
    >
      <CheckBadge checked={checked} />
      <UnreadDot unread={unread} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5 py-1">
        <div className="flex items-center gap-1.5 text-xs text-fg2">
          <SourceIcon src={feed?.icon} />
          <span className="truncate">{item.source}</span>
          <span aria-hidden="true">·</span>
          <time dateTime={new Date(item.published_at * 1000).toISOString()} className="shrink-0">
            {relativeTime(item.published_at)}
          </time>
        </div>
        <h3 className={cn("line-clamp-2 text-sm leading-snug", unread ? "font-bold text-fg" : "font-normal text-fg2")}>
          <Link
            to={to}
            onClick={() => onOpen(item)}
            className="after:absolute after:inset-0 focus-visible:after:outline-2 focus-visible:after:outline-accent"
            aria-label={rowLabel(item)}
          >
            <Hl text={item.title || "Untitled"} field="title" feedId={item.feed_id} />
          </Link>
        </h3>
      </div>
      <div className="-mr-2 flex shrink-0 items-center">
        {item.starred ? <StarButton item={item} onToggleStar={onToggleStar} /> : <StarButton item={item} onToggleStar={onToggleStar} className="row-fine-only row-reveal" />}
        <RowMenu item={item} actions={actions} revealOnHover />
      </div>
    </article>
  );
}

export const compact: ListLayout = {
  id: "compact",
  label: "Compact",
  Row: CompactRow,
  estimateRow: () => 56,
  paneRem: 22.5,
};
