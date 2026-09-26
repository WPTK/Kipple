import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import { CheckBadge, RowMenu, SourceIcon, StarButton, UnreadDot, rowLabel } from "./parts";
import type { ListLayout, RowProps } from "./types";

/**
 * Editorial: image-forward. A large 16:9 lead image over the source line, a big title and a longer excerpt,
 * with plenty of whitespace; in a wide list the image sits beside the text. Compare Inbox, which is text
 * first (sender, subject, snippet, time) with only a small optional thumbnail. Unread is a dot AND bold AND
 * full-strength text, never color alone; read rows dim to the secondary color. (The layout id stays
 * "magazine" so saved choices keep working; only the name shown to people changed.)
 */
function EditorialRow({ item, feed, selected, checked, to, onOpen, onToggleStar, actions }: RowProps) {
  const unread = !item.read;
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host @container relative flex flex-col gap-3 border-b border-line bg-bg px-4 py-[calc(var(--row-py)+0.5rem)]",
        selected ? "bg-selection" : "hover:bg-surface",
      )}
    >
      <CheckBadge checked={checked} />
      <div className="flex flex-col gap-3 @[34rem]:flex-row @[34rem]:gap-5">
        {item.image ? (
          <img
            src={item.image}
            alt=""
            loading="lazy"
            decoding="async"
            className={cn(
              "aspect-video w-full rounded-xl bg-surface object-cover @[34rem]:w-2/5 @[34rem]:self-start",
              item.read && "opacity-60",
            )}
            onError={(e) => {
              // Broken image: collapse to the text-only variant, no broken icon.
              e.currentTarget.style.display = "none";
            }}
          />
        ) : null}
        <div className="flex min-w-0 flex-1 flex-col gap-[calc(var(--row-gap)*2)]">
          <div className="flex items-center gap-1.5 text-xs text-fg2">
            <UnreadDot unread={unread} />
            <SourceIcon src={feed?.icon} />
            <span className="truncate">{item.source}</span>
            <span aria-hidden="true">·</span>
            <time dateTime={new Date(item.published_at * 1000).toISOString()} className="shrink-0">
              {relativeTime(item.published_at)}
            </time>
          </div>
          <h3 className={cn("clamp-title text-xl leading-tight", unread ? "font-bold text-fg" : "font-normal text-fg2")}>
            <Link
              to={to}
              onClick={() => onOpen(item)}
              className="after:absolute after:inset-0 focus-visible:after:outline-2 focus-visible:after:outline-accent"
              aria-label={rowLabel(item)}
            >
              {item.title || "Untitled"}
            </Link>
          </h3>
          {item.excerpt ? <p className="clamp-snippet text-base leading-normal text-fg2">{item.excerpt}</p> : null}
        </div>
      </div>
      <div className="relative z-10 -mr-2 -mb-2 flex items-center justify-end">
        <StarButton item={item} onToggleStar={onToggleStar} />
        <RowMenu item={item} actions={actions} revealOnHover />
      </div>
    </article>
  );
}

/** Editorial's row is two columns (image beside the text) once the row is this wide: the @[34rem] container query. */
export const EDITORIAL_SIDE_REM = 34;

/**
 * Row height guess from the list width. Stacked: a 16:9 image across the row plus the text (about 400 px at phone
 * width). Side by side: the image is 2/5 of the row and the text is the taller of the two, about 200 to 260 px.
 */
export function editorialRowHeight(hasImage: boolean, width: number, rem = 16): number {
  if (!hasImage) return 190;
  const w = width > 0 ? width : 375;
  const inner = Math.max(w - 32, 0);
  if (w >= EDITORIAL_SIDE_REM * rem) return Math.round(Math.max((inner * 0.4 * 9) / 16, 150) + 70);
  return Math.round((inner * 9) / 16 + 207);
}

export const magazine: ListLayout = {
  id: "magazine",
  label: "Editorial",
  Row: EditorialRow,
  estimateRow: (item, ctx) => editorialRowHeight(!!item.image, ctx?.width ?? 0, ctx?.rem ?? 16),
  paneRem: 28,
};
