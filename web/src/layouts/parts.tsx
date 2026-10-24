import { createContext, useContext, useState } from "react";
import { BellOff, Copy, ExternalLink, Mail, MailOpen, MoreHorizontal, MoveDown, MoveUp, Pencil, Rss, RotateCcw, Share2, Star } from "lucide-react";
import type { Card, Feed, Scope } from "@/api/types";
import { closeRowMenu, rowMenuStore } from "@/gestures/rowMenu";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import { useStore } from "@/lib/store";
import { DropdownMenu } from "@/ui/menu";
import { FeedIcon } from "@/ui/FeedIcon";
import { canNativeShare } from "@/lib/share";
import { useRowTabIndex } from "./rowTabStop";
import type { RowMenuActions } from "./types";

export function UnreadDot({ unread, className }: { unread: boolean; className?: string }) {
  return <span aria-hidden="true" data-unread={unread} className={cn("kp-unread-dot size-2 shrink-0 rounded-full", unread ? "bg-unread" : "bg-transparent", className)} />;
}

/**
 * The title's unread cue, one for every layout: bold at full strength while unread, regular in the secondary tone once
 * read. With UnreadDot it is what tells the two apart (the dot is the shape, the weight the second cue, so it is never
 * color alone).
 */
export const titleTone = (unread: boolean): string => (unread ? "font-bold text-fg" : "font-normal text-fg2");

/**
 * How a row that can be selected looks. Three states, three looks: hover is the surface tone, keyboard focus is the
 * focus ring on the row's link, and the selected row (the open article) is the selection tone with a bar on its left
 * edge, so a hovered row and a selected one are never mistaken for each other.
 */
export const rowState = (selected: boolean): string => (selected ? "bg-selection shadow-[inset_3px_0_0_var(--color-accent)]" : "hover:bg-surface");

/**
 * Whether the list is of one feed. Its rows then name no source and show no feed icon: the list's title already says
 * it. The list provides it once (OneFeed.Provider); every layout's source line reads it, so no layout is passed a flag.
 */
export const isOneFeed = (scope: Pick<Scope, "feed">): boolean => !!scope.feed;
const OneFeedContext = createContext(false);
export const OneFeed = OneFeedContext.Provider;
export const useOneFeed = (): boolean => useContext(OneFeedContext);

/** Whether a list row's lead picture could not be shown, and the handler that says so. */
export interface ImageFailure {
  failed: boolean;
  onError: () => void;
}

// Addresses that failed to load, kept across remounts: a list that comes back (from an article) lays a failed picture
// out without its box from the first render, so the scroll position it restores is not moved when the picture fails
// again. Capped; the oldest is forgotten first.
const FAILED_MAX = 500;
const failedImages = new Set<string>();

export function useImageFailure(src: string | null | undefined): ImageFailure {
  const [, bump] = useState(0);
  return {
    failed: !!src && failedImages.has(src),
    onError: () => {
      if (!src) return;
      failedImages.delete(src);
      failedImages.add(src);
      if (failedImages.size > FAILED_MAX) failedImages.delete(failedImages.values().next().value!);
      bump((n) => n + 1);
    },
  };
}
// How each lead picture was laid in its frame, kept across remounts like the failed ones: a list that comes back lays
// the picture out as it was, with no jump from one fit to the other. Capped; the oldest is forgotten first.
const fits = new Map<string, "cover" | "contain">();

/**
 * Whether a picture fills its frame (cropped) or sits whole in it (letterboxed). It fills when the crop keeps at least
 * three quarters of it in both directions: a picture close to the frame's shape. A portrait, a square or a panorama in a
 * landscape frame would lose most of itself, so it is shown whole on the frame's background instead. The frame never
 * changes size, so the row does not move when the picture's shape is learned.
 */
function fitOf(img: HTMLImageElement): "cover" | "contain" {
  const { naturalWidth: w, naturalHeight: h, clientWidth: bw, clientHeight: bh } = img;
  if (!w || !h || !bw || !bh) return "cover";
  const r = w / h / (bw / bh);
  return Math.min(r, 1 / r) >= 0.75 ? "cover" : "contain";
}

/**
 * A list row's lead picture. When it cannot be shown (the image proxy refuses the format, the file is gone) the
 * picture and its `frame` (the box that holds its place) are removed together, so no layout keeps an empty box.
 * The one place that rule lives; every layout draws its picture through here and reads the same `failure` (from
 * useImageFailure) to lay the rest of the row out as a row without a picture. (The article itself says "Image not
 * shown" in place of an image; a list row just drops it.) `shaped`: the picture is a lead in a wide frame, cut to
 * the frame only when its shape is close to it (fitOf); a thumbnail is always cut to its square.
 */
export function ListImage({ src, failure, className, frame, shaped }: { src: string; failure: ImageFailure; className?: string; frame?: { className?: string }; shaped?: boolean }) {
  const [, bump] = useState(0);
  if (failure.failed) return null;
  const fit = (shaped && fits.get(src)) || "cover";
  const img = (
    <img
      src={src}
      alt=""
      loading="lazy"
      decoding="async"
      className={cn(fit === "cover" ? "object-cover" : "object-contain", className)}
      onLoad={
        shaped
          ? (e) => {
              const next = fitOf(e.currentTarget);
              if (fits.get(src) === next) return;
              fits.delete(src);
              fits.set(src, next);
              if (fits.size > FAILED_MAX) fits.delete(fits.keys().next().value!);
              bump((n) => n + 1);
            }
          : undefined
      }
      onError={failure.onError}
    />
  );
  return frame ? <div className={frame.className}>{img}</div> : img;
}
export function StarButton({ item, onToggleStar, className }: { item: Card; onToggleStar: (i: Card) => void; className?: string }) {
  const tabIndex = useRowTabIndex();
  return (
    <button
      type="button"
      tabIndex={tabIndex}
      onClick={() => onToggleStar(item)}
      aria-pressed={item.starred}
      aria-label={rowControlName("Star", item)}
      className={cn(
        "hit-row pointer-events-auto relative z-10 inline-flex items-center justify-center rounded-lg",
        item.starred ? "text-star" : "text-fg2 hover:text-fg",
        className,
      )}
    >
      <Star className="size-5" fill={item.starred ? "currentColor" : "none"} aria-hidden="true" />
    </button>
  );
}

const menuItem =
  "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[disabled]:opacity-50 data-[highlighted]:bg-selection";

/** The "More" button and its menu: also the target of long-press and the swipe's More action. */
export function RowMenu({
  item,
  actions,
  revealOnHover = false,
  ranges = true,
  className,
}: {
  item: Card;
  actions: RowMenuActions;
  /** Laid over a picture or text: shown on hover or focus where there is a mouse (index.css, Row controls). */
  revealOnHover?: boolean;
  /** Offer Mark above and Mark below. A page that is not in date order (the Gazette) has no above or below. */
  ranges?: boolean;
  className?: string;
}) {
  const openId = useStore(rowMenuStore);
  const open = openId === item.id;
  // Whether something had focus when the menu opened (a key or a click on the button) as opposed to a long press or a
  // swipe, which open it from the row with focus nowhere. Only the first gets focus back on close.
  const [hadFocus, setHadFocus] = useState(false);
  const canShare = canNativeShare();
  const muted = item.muted_by !== null && item.muted_by !== undefined;
  const tabIndex = useRowTabIndex();
  return (
    <DropdownMenu.Root open={open} onOpenChange={(o) => {
        if (o) {
          setHadFocus(!!document.activeElement && document.activeElement !== document.body);
          rowMenuStore.set(item.id);
        } else closeRowMenu();
      }} modal={false}>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label={rowControlName("More actions", item)}
          tabIndex={tabIndex}
          className={cn(
            "hit-row pointer-events-auto relative z-10 inline-flex items-center justify-center rounded-lg text-fg2 hover:text-fg",
            revealOnHover && "row-reveal",
            className,
          )}
        >
          <MoreHorizontal className="size-5" aria-hidden="true" />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-56"
          // Read by focusOrigin (ui/kit): a dialog opened from this menu gives focus back only where this menu would.
          data-no-focus-return={hadFocus ? undefined : ""}
          onCloseAutoFocus={(e) => {
            // Long-press and swipe open the menu without focus in the row; let focus stay where the user is. By the
            // time this runs the menu is gone, so the page's focus is already on the body: ask what it was at the start.
            if (!hadFocus) e.preventDefault();
            setHadFocus(false);
          }}
        >
          <DropdownMenu.Item className={menuItem} onSelect={() => actions.toggleStar(item)}>
            <Star className="size-5" aria-hidden="true" />
            {item.starred ? "Unstar" : "Star"}
          </DropdownMenu.Item>
          {muted ? (
            <>
              <DropdownMenu.Item className={menuItem} onSelect={() => actions.restore(item)}>
                <RotateCcw className="size-5" aria-hidden="true" />
                Restore
              </DropdownMenu.Item>
              {item.muted_by_name !== null ? (
                <DropdownMenu.Item className={menuItem} onSelect={() => actions.editRule(item)}>
                  <Pencil className="size-5" aria-hidden="true" />
                  Edit rule
                </DropdownMenu.Item>
              ) : null}
            </>
          ) : (
            <>
              <DropdownMenu.Item className={menuItem} onSelect={() => actions.toggleRead(item)}>
                {item.read ? <Mail className="size-5" aria-hidden="true" /> : <MailOpen className="size-5" aria-hidden="true" />}
                {item.read ? "Mark as unread" : "Mark as read"}
              </DropdownMenu.Item>
              {ranges ? (
                <>
                  <DropdownMenu.Item className={menuItem} disabled={actions.noRange} onSelect={() => actions.markAbove(item)}>
                    <MoveUp className="size-5" aria-hidden="true" />
                    Mark above as read
                  </DropdownMenu.Item>
                  <DropdownMenu.Item className={menuItem} disabled={actions.noRange} onSelect={() => actions.markBelow(item)}>
                    <MoveDown className="size-5" aria-hidden="true" />
                    Mark below as read
                  </DropdownMenu.Item>
                </>
              ) : null}
              <DropdownMenu.Item className={menuItem} onSelect={() => actions.muteSimilar(item)}>
                <BellOff className="size-5" aria-hidden="true" />
                Mute similar…
              </DropdownMenu.Item>
            </>
          )}
          <DropdownMenu.Separator className="my-1 h-px bg-line" />
          <DropdownMenu.Item className={menuItem} onSelect={() => actions.manageFeed(item)}>
            <Rss className="size-5" aria-hidden="true" />
            Manage this feed
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={() => actions.openOriginal(item)}>
            <ExternalLink className="size-5" aria-hidden="true" />
            Open original
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={() => actions.copyLink(item)}>
            <Copy className="size-5" aria-hidden="true" />
            Copy link
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={() => actions.share(item)}>
            <Share2 className="size-5" aria-hidden="true" />
            {canShare ? "Share" : "Share (copies the link)"}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

/**
 * The word an unread row adds to its link's accessible name. It is the value of data-row-unread, and index.css draws it
 * as generated text after the title (`content: ", " attr(data-row-unread)`), so the word lives only here: a link named
 * by its content has no aria-label to compare with the visible text (issue #513, web/uat/row-name.mjs).
 */
const UNREAD_WORD = "unread";

/** What every layout's row link takes: the row-link class (the whole row is its hit area), any layout classes, and the unread flag. */
export function rowLinkProps(item: Pick<Card, "read">, className = ""): { className: string; "data-row-unread": string | undefined } {
  return { className: className ? `row-link ${className}` : "row-link", "data-row-unread": item.read ? undefined : UNREAD_WORD };
}

/** A row control's name: the action, then the article it acts on, so a list of buttons or a voice command can tell rows apart. */
export const rowControlName = (action: string, item: Card): string => `${action}: ${item.title || "Untitled"}`;

/** The publish time as a machine-readable <time> (ISO date) with the relative text. One place for every layout. */
export function PublishedTime({ item, className }: { item: Pick<Card, "published_at">; className?: string }) {
  return (
    <time dateTime={new Date(item.published_at * 1000).toISOString()} className={className}>
      {relativeTime(item.published_at)}
    </time>
  );
}

/**
 * The "[dot] [icon] source · time" meta row shared by the cards, compact, inbox and editorial layouts. In a list of one
 * feed (useOneFeed) the source and its icon are left out.
 */
export function SourceTimeMeta({ item, feed, dot }: { item: Card; feed: Feed | undefined; dot?: React.ReactNode }) {
  const oneFeed = useOneFeed();
  return (
    <div className="flex items-center gap-1.5 text-xs text-fg2">
      {dot}
      {oneFeed ? null : (
        <>
          <FeedIcon src={feed?.icon} title={item.source} />
          <span className="truncate">{item.source}</span>
          <span aria-hidden="true">·</span>
        </>
      )}
      <PublishedTime item={item} className="shrink-0" />
    </div>
  );
}