import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { BoneLines } from "@/ui/kit";
import { ListImage, useImageFailure, RowActions, SourceTimeMeta, UnreadDot, rowLinkProps, titleTone } from "./parts";
import type { RowLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";
import { useRowTabIndex, ROW_KEYSHORTCUTS } from "./rowTabStop";

/**
 * Cards: a real card shell (raised surface, border, shadow, an inset lead image with its
 * own rounded corners) so it reads as a card even in the single-column layout narrow
 * viewports fall back to, where it would otherwise be indistinguishable from Editorial's
 * full-bleed treatment. Source, title and a three-line excerpt sit in the card's padded
 * body, with the star and More at the end of the source line. A card alone in its line (one column) has a 2:1 picture,
 * one in a grid 16:9; where it swipes and the only pointer is a finger, the star and More leave the card (see RowActions).
 * The list lays cards out in 1, 2 or 3 columns by its own width, and opening one shows the article full width (no
 * reader pane).
 */
function CardRow({ item, feed, selected, to, onOpen, onToggleStar, actions, swipe }: RowProps) {
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
        <ListImage src={item.image} failure={failure} shaped frame={{ className: "p-2 pb-0" }} className={cn("w-full rounded-lg bg-bg", swipe ? "aspect-[2/1]" : "aspect-video", item.read && "opacity-60")} />
      ) : null}
      <div className="flex min-w-0 flex-1 flex-col gap-1.5 p-3">
        <SourceTimeMeta item={item} feed={feed} dot={<UnreadDot unread={unread} />} end={<RowActions item={item} onToggleStar={onToggleStar} actions={actions} swipe={swipe} />} />
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
    </article>
  );
}

/**
 * Loading stand-in for a card with a picture: the shell, the picture's frame (2:1 on a phone, where the list is one
 * column, like the card; 16:9 in a grid), then bars as tall as the source, title and excerpt lines. The star and More
 * are on the source line, so there is no footer.
 */
function CardSkeleton() {
  return (
    <div aria-hidden="true" className="flex h-full flex-col overflow-hidden rounded-2xl border border-line bg-surface">
      <div className="p-2 pb-0">
        <div className="aspect-video w-full animate-pulse rounded-lg bg-bg max-sm:aspect-[2/1]" />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-1.5 p-3">
        <BoneLines lines={1} className="text-xs" boneClassName="bg-bg" />
        <BoneLines lines={2} max="--title-lines" className="text-base leading-snug" boneClassName="bg-bg" />
        {/* The card's excerpt is three lines at every density (CardRow's line-clamp-3), not the density's snippet lines. */}
        <BoneLines lines={3} className="text-sm leading-snug" boneClassName="bg-bg" />
      </div>
    </div>
  );
}

export const cards: RowLayout = {
  id: "cards",
  label: "Cards",
  Row: CardRow,
  Skeleton: CardSkeleton,
  estimateRow: (item) => (item.image ? 330 : 150),
  grid: true,
  paneRem: 26,
};
