import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { Bone } from "@/ui/kit";
import { ListImage, useImageFailure, RowMenu, SourceTimeMeta, StarButton, UnreadDot, rowLinkProps, titleTone } from "./parts";
import type { RowLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";
import { useRowTabIndex, ROW_KEYSHORTCUTS } from "./rowTabStop";

/**
 * Cards: a real card shell (raised surface, border, shadow, an inset lead image with its
 * own rounded corners) so it reads as a card even in the single-column layout narrow
 * viewports fall back to, where it would otherwise be indistinguishable from Editorial's
 * full-bleed treatment. Source, title and a three-line excerpt sit in the card's padded
 * body, with the actions in a footer. The list lays cards out in 1, 2 or 3 columns by its
 * own width, and opening one shows the article full width (no reader pane).
 */
function CardRow({ item, feed, selected, to, onOpen, onToggleStar, actions }: RowProps) {
  const tabIndex = useRowTabIndex();
  const unread = !item.read;
  const failure = useImageFailure(item.image);
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host relative flex h-full flex-col overflow-hidden rounded-2xl [--row-radius:var(--radius-2xl)] border border-line bg-surface shadow-md",
        selected && "bg-selection outline-2 outline-accent",
      )}
    >
      {item.image ? (
        <ListImage src={item.image} failure={failure} shaped frame={{ className: "p-2 pb-0" }} className={cn("aspect-video w-full rounded-lg bg-bg", item.read && "opacity-60")} />
      ) : null}
      <div className={cn("flex min-w-0 flex-1 flex-col gap-1.5 p-3", !item.image && "min-h-[8.5rem]")}>
        <SourceTimeMeta item={item} feed={feed} dot={<UnreadDot unread={unread} />} />
        <h3 className={cn("clamp-title text-base leading-snug", titleTone(unread))}>
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
        {item.excerpt ? <p className="line-clamp-3 text-sm leading-snug text-fg2"><Hl text={item.excerpt} field="content" feedId={item.feed_id} /></p> : null}
      </div>
      <div className="relative z-10 flex items-center justify-end px-1 pb-1">
        <StarButton item={item} onToggleStar={onToggleStar} />
        <RowMenu item={item} actions={actions} />
      </div>
    </article>
  );
}

/** Loading stand-in for a card: the shell, the picture, source, title and excerpt lines. */
function CardSkeleton() {
  return (
    <div aria-hidden="true" className="flex flex-col gap-2 overflow-hidden rounded-2xl border border-line bg-surface p-2 pb-4">
      <div className="aspect-video w-full animate-pulse rounded-lg bg-bg" />
      <div className="flex flex-col gap-2 px-1 pt-1">
        <Bone className="h-3 w-1/3 bg-bg" />
        <Bone className="h-4 w-11/12 bg-bg" />
        <Bone className="h-3 w-3/4 bg-bg" />
      </div>
    </div>
  );
}

export const cards: RowLayout = {
  id: "cards",
  label: "Cards",
  Row: CardRow,
  Skeleton: CardSkeleton,
  estimateRow: (item) => (item.image ? 340 : 200),
  grid: true,
  paneRem: 26,
};
