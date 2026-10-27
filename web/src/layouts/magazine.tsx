import { Link } from "react-router";
import { cn } from "@/lib/cn";
import { Bone } from "@/ui/kit";
import { ListImage, useImageFailure, RowMenu, SourceTimeMeta, StarButton, UnreadDot, rowLinkProps, rowState, titleTone } from "./parts";
import type { RowLayout, RowProps } from "./types";
import { Hl } from "@/lib/useHighlights";
import { useRowTabIndex, ROW_KEYSHORTCUTS } from "./rowTabStop";

/**
 * Editorial: image-forward. A large 16:9 lead image over the source line, a big title and a longer excerpt,
 * with plenty of whitespace; in a wide list the image sits beside the text. Compare Inbox, which is text
 * first (sender, subject, snippet, time) with only a small optional thumbnail. Unread is a dot AND bold AND
 * full-strength text, never color alone; read rows dim to the secondary color. (The layout id stays
 * "magazine" so saved choices keep working; only the name shown to people changed.)
 */
function EditorialRow({ item, feed, selected, to, onOpen, onToggleStar, actions }: RowProps) {
  const tabIndex = useRowTabIndex();
  const unread = !item.read;
  const failure = useImageFailure(item.image);
  return (
    <article
      data-item-id={item.id}
      data-selected={selected || undefined}
      className={cn(
        "row-host @container relative flex flex-col gap-3 border-b border-line bg-bg px-4 py-[calc(var(--row-py)+0.5rem)]",
        rowState(selected),
      )}
    >
      <div className="flex flex-col gap-3 @[34rem]:flex-row @[34rem]:gap-5">
        {item.image ? (
          <ListImage
            src={item.image}
            failure={failure}
            shaped
            className={cn("aspect-video w-full rounded-xl bg-surface @[34rem]:w-2/5 @[34rem]:self-start", item.read && "opacity-60")}
          />
        ) : null}
        <div className={cn("flex min-w-0 flex-1 flex-col gap-[calc(var(--row-gap)*2)]", !item.image && "min-h-[8rem]")}>
          <SourceTimeMeta item={item} feed={feed} dot={<UnreadDot unread={unread} />} />
          <h3 className={cn("clamp-title text-xl leading-tight", titleTone(unread))}>
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
          {item.excerpt ? <p className="clamp-snippet text-base leading-normal text-fg2"><Hl text={item.excerpt} field="content" feedId={item.feed_id} /></p> : null}
        </div>
      </div>
      <div className="relative z-10 -mr-2 -mb-2 flex items-center justify-end">
        <StarButton item={item} onToggleStar={onToggleStar} />
        <RowMenu item={item} actions={actions} />
      </div>
    </article>
  );
}

/** Editorial's row is two columns (image beside the text) once the row is this wide: the @[34rem] container query. */
const EDITORIAL_SIDE_REM = 34;

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

/** Loading stand-in for an Editorial row: the picture across the top, then source, title and two lines. */
function EditorialSkeleton() {
  return (
    <div aria-hidden="true" className="flex flex-col gap-3 border-b border-line px-4 py-4">
      <Bone className="aspect-video w-full rounded-lg" />
      <Bone className="h-3 w-1/3" />
      <Bone className="h-5 w-11/12" />
      <Bone className="h-4 w-3/4" />
    </div>
  );
}

export const magazine: RowLayout = {
  id: "magazine",
  label: "Editorial",
  Row: EditorialRow,
  Skeleton: EditorialSkeleton,
  estimateRow: (item, ctx) => editorialRowHeight(!!item.image, ctx?.width ?? 0, ctx?.rem ?? 16),
  paneRem: 28,
};
