import { DropdownMenu } from "radix-ui";
import { Check, Copy, ExternalLink, Mail, MailOpen, MoreHorizontal, MoveDown, MoveUp, Share2, Star } from "lucide-react";
import type { Card } from "@/api/types";
import { closeRowMenu, rowMenuStore } from "@/gestures/rowMenu";
import { cn } from "@/lib/cn";
import { useStore } from "@/lib/store";
import type { RowMenuActions } from "./types";

export function UnreadDot({ unread, className }: { unread: boolean; className?: string }) {
  return <span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", unread ? "bg-unread" : "bg-transparent", className)} />;
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
  const canShare = typeof navigator !== "undefined" && "share" in navigator;
  return (
    <DropdownMenu.Root open={open} onOpenChange={(o) => (o ? rowMenuStore.set(item.id) : closeRowMenu())} modal={false}>
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
            // Long-press and swipe open the menu without focus in the row; let focus stay where the user is.
            if (!document.activeElement || document.activeElement === document.body) e.preventDefault();
          }}
        >
          <DropdownMenu.Item className={menuItem} onSelect={() => actions.toggleStar(item)}>
            <Star className="size-5" aria-hidden="true" />
            {item.starred ? "Unstar" : "Star"}
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} onSelect={() => actions.toggleRead(item)}>
            {item.read ? <Mail className="size-5" aria-hidden="true" /> : <MailOpen className="size-5" aria-hidden="true" />}
            {item.read ? "Mark unread" : "Mark read"}
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} disabled={!actions.canRange} onSelect={() => actions.markAbove(item)}>
            <MoveUp className="size-5" aria-hidden="true" />
            Mark above as read
          </DropdownMenu.Item>
          <DropdownMenu.Item className={menuItem} disabled={!actions.canRange} onSelect={() => actions.markBelow(item)}>
            <MoveDown className="size-5" aria-hidden="true" />
            Mark below as read
          </DropdownMenu.Item>
          <DropdownMenu.Separator className="my-1 h-px bg-line" />
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
