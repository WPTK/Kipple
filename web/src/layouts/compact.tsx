import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { RowMenu, SourceTimeMeta, StarButton, UnreadDot, rowLinkProps, rowState, titleTone } from "./parts";
import type { RowLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";
import { useRowTabIndex, ROW_KEYSHORTCUTS } from "./rowTabStop";

/**
 * Compact: unread dot, a small source and time line over a 1-2 line title, no
 * image, then the star and the row menu. Rows are min 44 px on touch (see .row-compact).
 */
function CompactRow({ item, feed, selected, to, onOpen, onToggleStar, actions }: RowProps) {
  const tabIndex = useRowTabIndex();
  const unread = !item.read;
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host row-compact relative flex items-center gap-2 border-b border-line bg-bg px-4",
        rowState(selected),
      )}
    >
      <UnreadDot unread={unread} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5 py-1">
        <SourceTimeMeta item={item} feed={feed} />
        <h3 className={cn("line-clamp-2 text-sm leading-snug [overflow-wrap:anywhere]", titleTone(unread))}>
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
      </div>
      <div className="-mr-2 flex shrink-0 items-center">
        <StarButton item={item} onToggleStar={onToggleStar} />
        <RowMenu item={item} actions={actions} />
      </div>
    </article>
  );
}

export const compact: RowLayout = {
  id: "compact",
  label: "Compact",
  Row: CompactRow,
  estimateRow: () => 56,
  paneRem: 22.5,
};
