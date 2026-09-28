import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { CheckBadge, PublishedTime, RowMenu, StarButton, UnreadDot, rowLabel } from "./parts";
import type { ListLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";

/**
 * Email - Compact (layout id "headlines"): one line per article, title only, like a compact mail list.
 * No favicon at all, on phone or desktop — text density only, so it stays the clearly terser option next
 * to Compact (which always shows one). Phone: dot, title, time. A wide list pane also shows the source
 * name as a column (table-like on desktop). 44 px on touch; 28 to 36 px with a mouse, by density step.
 */
function HeadlineRow({ item, selected, checked, to, onOpen, onToggleStar, actions }: RowProps) {
  const unread = !item.read;
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host row-headline relative flex items-center gap-2 border-b border-line bg-bg px-4",
        selected ? "bg-selection" : "hover:bg-surface",
      )}
    >
      <CheckBadge checked={checked} />
      <UnreadDot unread={unread} />
      <span className="hidden w-40 shrink-0 truncate text-xs text-fg2 @[32rem]:inline">{item.source}</span>
      <h3 className={cn("min-w-0 flex-1 truncate text-sm", unread ? "font-bold text-fg" : "font-normal text-fg2")}>
        <Link
          to={to}
          onClick={() => onOpen(item)}
          className="after:absolute after:inset-0 focus-visible:after:outline-2 focus-visible:after:outline-accent"
          aria-label={rowLabel(item)}
        >
          <Hl text={item.title || "Untitled"} field="title" feedId={item.feed_id} />
        </Link>
      </h3>
      <PublishedTime item={item} className="w-10 shrink-0 text-right text-xs text-fg2 tabular-nums" />
      <div className="-mr-2 flex shrink-0 items-center">
        {item.starred ? <StarButton item={item} onToggleStar={onToggleStar} /> : null}
        <RowMenu item={item} actions={actions} revealOnHover />
      </div>
    </article>
  );
}

export const headlines: ListLayout = {
  id: "headlines",
  label: "Email - Compact",
  Row: HeadlineRow,
  estimateRow: () => 44,
  paneRem: 22,
};
