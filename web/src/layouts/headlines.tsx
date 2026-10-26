import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { PublishedTime, RowMenu, StarButton, UnreadDot, rowLinkProps, rowState, titleTone, useOneFeed } from "./parts";
import type { RowLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";
import { useRowTabIndex, ROW_KEYSHORTCUTS } from "./rowTabStop";

/**
 * Headlines (layout id "headlines"): one line per article, title only, like a compact mail list.
 * No favicon at all, on phone or desktop — text density only, so it stays the clearly terser option next
 * to Compact (which always shows one). Phone: dot, title, time. A wide list pane also shows the source
 * name as a column (table-like on desktop). 44 px on touch; 28 to 36 px with a mouse, by density step.
 */
function HeadlineRow({ item, selected, to, onOpen, onToggleStar, actions }: RowProps) {
  const tabIndex = useRowTabIndex();
  const unread = !item.read;
  const one = useOneFeed();
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host row-headline relative flex items-center gap-2 border-b border-line bg-bg px-4",
        rowState(selected),
      )}
    >
      <UnreadDot unread={unread} />
      {one ? null : <span className="hidden w-40 shrink-0 truncate text-xs text-fg2 @[32rem]:inline">{item.source}</span>}
      <h3 className={cn("min-w-0 flex-1 text-sm", titleTone(unread))}>
        <Link
          tabIndex={tabIndex}
          to={to}
          onClick={() => onOpen(item)}
          aria-keyshortcuts={ROW_KEYSHORTCUTS}
          {...rowLinkProps(item, "headline-title block")}
        >
          <Hl text={item.title || "Untitled"} field="title" feedId={item.feed_id} />
        </Link>
      </h3>
      <PublishedTime item={item} className="w-10 shrink-0 text-right text-xs text-fg2 tabular-nums" />
      <div className="-mr-2 flex shrink-0 items-center">
        {item.starred ? <StarButton item={item} onToggleStar={onToggleStar} /> : null}
        <RowMenu item={item} actions={actions} />
      </div>
    </article>
  );
}

export const headlines: RowLayout = {
  id: "headlines",
  label: "Headlines",
  Row: HeadlineRow,
  estimateRow: () => 44,
  paneRem: 22,
};
