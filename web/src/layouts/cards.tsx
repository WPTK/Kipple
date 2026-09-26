import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { CheckBadge, RowMenu, SourceTimeMeta, StarButton, UnreadDot, rowLabel } from "./parts";
import type { ListLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";

/**
 * Cards: a 16:9 lead image over source, title and a three-line excerpt, with the
 * actions in a footer. The list lays cards out in 1, 2 or 3 columns by its own
 * width, and opening one shows the article full width (no reader pane).
 */
function CardRow({ item, feed, selected, checked, to, onOpen, onToggleStar, actions }: RowProps) {
  const unread = !item.read;
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host relative flex h-full flex-col overflow-hidden rounded-xl border border-line bg-bg",
        selected && "bg-selection outline-2 outline-accent",
      )}
    >
      <CheckBadge checked={checked} />
      {item.image ? (
        <img
          src={item.image}
          alt=""
          loading="lazy"
          decoding="async"
          className={cn("aspect-video w-full bg-surface object-cover", item.read && "opacity-60")}
          onError={(e) => {
            e.currentTarget.style.display = "none";
          }}
        />
      ) : null}
      <div className="flex min-w-0 flex-1 flex-col gap-1.5 p-3">
        <SourceTimeMeta item={item} icon={feed?.icon} dot={<UnreadDot unread={unread} />} />
        <h3 className={cn("clamp-title text-base leading-snug", unread ? "font-bold text-fg" : "font-normal text-fg2")}>
          <Link
            to={to}
            onClick={() => onOpen(item)}
            className="after:absolute after:inset-0 focus-visible:after:outline-2 focus-visible:after:outline-accent"
            aria-label={rowLabel(item)}
          >
            <Hl text={item.title || "Untitled"} field="title" feedId={item.feed_id} />
          </Link>
        </h3>
        {item.excerpt ? <p className="line-clamp-3 text-sm leading-snug text-fg2"><Hl text={item.excerpt} field="content" feedId={item.feed_id} /></p> : null}
      </div>
      <div className="relative z-10 flex items-center justify-end px-1 pb-1">
        <StarButton item={item} onToggleStar={onToggleStar} />
        <RowMenu item={item} actions={actions} />
      </div>
    </article>
  );
}

export const cards: ListLayout = {
  id: "cards",
  label: "Cards",
  Row: CardRow,
  estimateRow: (item) => (item.image ? 340 : 200),
  grid: true,
  paneRem: 26,
};
