import { useRef } from "react";
import { DropdownMenu } from "radix-ui";
import { BellOff, Check, Copy, ExternalLink, Mail, MailOpen, MoreHorizontal, MoveDown, MoveUp, Pencil, Rss, RotateCcw, Share2, Star } from "lucide-react";
import type { Card } from "@/api/types";
import { closeRowMenu, rowMenuStore } from "@/gestures/rowMenu";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import { useStore } from "@/lib/store";
import { canNativeShare } from "@/lib/share";
import type { RowMenuActions } from "./types";

export function UnreadDot({ unread, className }: { unread: boolean; className?: string }) {
  return <span aria-hidden="true" data-unread={unread} className={cn("kp-unread-dot size-2 shrink-0 rounded-full", unread ? "bg-unread" : "bg-transparent", className)} />;
}

export function SourceIcon({ src, className }: { src: string | null | undefined; className?: string }) {
  if (!src) return null;
  return <img src={src} alt="" width={16} height={16} loading="lazy" className={cn("size-4 shrink-0 rounded-sm", className)} />;
}

export function CheckBadge({ checked }: { checked: boolean }) {
  if (!checked) return null;
  return (
    <span aria-hidden="true" className="absolute top-1 left-1 z-10 grid size-5 place-items-center rounded-full bg-accent text-bg">
      <Check className="size-3.5" />
    </span>
  );
}

export function StarButton({ item, onToggleStar, className }: { item: Card; onToggleStar: (i: Card) => void; className?: string }) {
  return (
    <button
      type="button"
      onClick={() => onToggleStar(item)}
      aria-pressed={item.starred}
      aria-label={item.starred ? "Unstar" : "Star"}
      className={cn(
        "hit-row pointer-events-auto relative z-10 inline-flex items-center justify-center rounded-lg",
        item.starred ? "text-star" : "text-fg2 opacity-70 hover:opacity-100",
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
  className,
}: {
  item: Card;
  actions: RowMenuActions;
  revealOnHover?: boolean;
  className?: string;
}) {
  const openId = useStore(rowMenuStore);
  const open = openId === item.id;
  // Whether something had focus when the menu opened (a key or a click on the button) as opposed to a long press or a
  // swipe, which open it from the row with focus nowhere. Only the first gets focus back on close.
  const hadFocus = useRef(false);
  const canShare = canNativeShare();
  const muted = item.muted_by !== null && item.muted_by !== undefined;
  return (
    <DropdownMenu.Root open={open} onOpenChange={(o) => {
        if (o) {
          hadFocus.current = !!document.activeElement && document.activeElement !== document.body;
          rowMenuStore.set(item.id);
        } else closeRowMenu();
      }} modal={false}>
      <DropdownMenu.Trigger asChild>
        <button
          type="button"
          aria-label="More actions"
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
        <DropdownMenu.Content
          align="end"
          sideOffset={4}
          collisionPadding={8}
          className="z-50 min-w-56 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl"
          onCloseAutoFocus={(e) => {
            // Long-press and swipe open the menu without focus in the row; let focus stay where the user is. By the
            // time this runs the menu is gone, so the page's focus is already on the body: ask what it was at the start.
            if (!hadFocus.current) e.preventDefault();
            hadFocus.current = false;
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
              <DropdownMenu.Item className={menuItem} disabled={actions.noRange} onSelect={() => actions.markAbove(item)}>
                <MoveUp className="size-5" aria-hidden="true" />
                Mark above as read
              </DropdownMenu.Item>
              <DropdownMenu.Item className={menuItem} disabled={actions.noRange} onSelect={() => actions.markBelow(item)}>
                <MoveDown className="size-5" aria-hidden="true" />
                Mark below as read
              </DropdownMenu.Item>
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

/** Accessible name of a row's link: "Unread, <title>, <source>". */
export function rowLabel(item: Card): string {
  return `${item.read ? "" : "Unread, "}${item.title || "Untitled"}, ${item.source}`;
}

/** The publish time as a machine-readable <time> (ISO date) with the relative text. One place for every layout. */
export function PublishedTime({ item, className }: { item: Card; className?: string }) {
  return (
    <time dateTime={new Date(item.published_at * 1000).toISOString()} className={className}>
      {relativeTime(item.published_at)}
    </time>
  );
}

/** The "[dot] [icon] source · time" meta row shared by the cards, compact and magazine layouts. */
export function SourceTimeMeta({ item, icon, dot }: { item: Card; icon: string | null | undefined; dot?: React.ReactNode }) {
  return (
    <div className="flex items-center gap-1.5 text-xs text-fg2">
      {dot}
      <SourceIcon src={icon} />
      <span className="truncate">{item.source}</span>
      <span aria-hidden="true">·</span>
      <PublishedTime item={item} className="shrink-0" />
    </div>
  );
}
