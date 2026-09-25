import { Link } from "react-router";
import { Star } from "lucide-react";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import type { ListLayout, RowProps } from "./types";

/**
 * Magazine: source line (dot, icon, name, time) over the title and a short
 * excerpt, thumbnail on the right. Unread is a dot AND bold AND full-strength
 * text, never color alone; read rows dim to the secondary color.
 * Row height is max(--row-min, content), never fixed (see index.css).
 */
function MagazineRow({ item, feed, selected, to, onOpen, onToggleStar }: RowProps) {
  const unread = !item.read;
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "relative flex min-h-[var(--row-min)] gap-3 border-b border-line px-4 py-[var(--row-py)]",
        "@container",
        selected ? "bg-selection" : "hover:bg-surface",
      )}
    >
      <div className="flex min-w-0 flex-1 flex-col gap-[var(--row-gap)]">
        <div className="flex items-center gap-1.5 text-xs text-fg2">
          <span
            aria-hidden="true"
            className={cn("size-2 shrink-0 rounded-full", unread ? "bg-unread" : "bg-transparent")}
          />
          {feed?.icon ? (
            <img src={feed.icon} alt="" width={16} height={16} loading="lazy" className="size-4 shrink-0 rounded-sm" />
          ) : null}
          <span className="truncate">{item.source}</span>
          <span aria-hidden="true">·</span>
          <time dateTime={new Date(item.published_at * 1000).toISOString()} className="shrink-0">
            {relativeTime(item.published_at)}
          </time>
        </div>
        <h3 className={cn("clamp-title text-base leading-snug", unread ? "font-bold text-fg" : "font-normal text-fg2")}>
          <Link
            to={to}
            onClick={() => onOpen(item)}
            className="after:absolute after:inset-0 focus-visible:after:outline-2 focus-visible:after:outline-accent"
            aria-label={`${unread ? "Unread, " : ""}${item.title || "Untitled"}, ${item.source}`}
          >
            {item.title || "Untitled"}
          </Link>
        </h3>
        {item.excerpt ? <p className="clamp-snippet text-sm leading-snug text-fg2">{item.excerpt}</p> : null}
      </div>
      <div className="pointer-events-none relative z-10 flex shrink-0 flex-col items-end justify-between gap-1">
        {item.image ? (
          <img
            src={item.image}
            alt=""
            loading="lazy"
            decoding="async"
            className={cn(
              "size-[var(--thumb)] rounded-lg bg-surface object-cover",
              item.read && "opacity-60",
              // Below 360 px of row width the thumb yields to the text (max 25%).
              "@max-[22rem]:size-[min(var(--thumb),25cqw)]",
            )}
            onError={(e) => {
              // Broken image: collapse to the text-only variant, no broken icon.
              e.currentTarget.style.display = "none";
            }}
          />
        ) : null}
        <button
          type="button"
          onClick={() => onToggleStar(item)}
          aria-pressed={item.starred}
          aria-label={item.starred ? "Unstar" : "Star"}
          className={cn(
            "hit pointer-events-auto -mr-2 inline-flex items-center justify-center rounded-lg",
            item.starred ? "text-star" : "text-fg2 opacity-70 hover:opacity-100",
          )}
        >
          <Star className="size-5" fill={item.starred ? "currentColor" : "none"} aria-hidden="true" />
        </button>
      </div>
    </article>
  );
}

export const magazine: ListLayout = {
  id: "magazine",
  label: "Magazine",
  Row: MagazineRow,
  estimateRow: (item) => (item.image ? 128 : 112),
};
