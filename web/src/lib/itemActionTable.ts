import { BellOff, CheckCheck, Copy, ExternalLink, FileText, Mail, MailOpen, MoreHorizontal, MoveDown, MoveUp, Pencil, Rss, RotateCcw, Share2, Star, type LucideIcon } from "lucide-react";
import type { Scope } from "@/api/types";

/**
 * The one table of what can be done to an article: its words, glyph and group. The row menu, the open article's More
 * menu, the toolbar and the swipe panels each pick the actions they offer from `surfaceActions` and draw them with
 * `actionLabel` and `actionIcon`, so a name or a glyph is changed here and nowhere else.
 */

export type ActionId =
  | "star"
  | "read"
  | "markAbove"
  | "markBelow"
  | "muteSimilar"
  | "restore"
  | "editRule"
  | "editFeed"
  | "openOriginal"
  | "copyLink"
  | "share"
  | "fulltext"
  | "more";

/** Menus draw a divider between groups. */
export type ActionGroup = "state" | "range" | "filter" | "feed" | "link";

/** The facts a label or glyph can depend on. */
export interface ActionState {
  /** The article is starred (star), read (read), or its full text is on (fulltext). */
  on?: boolean;
}

interface ActionDef {
  /** What the control says, which is also its accessible name. */
  label: (s: ActionState) => string;
  icon: (s: ActionState) => LucideIcon;
  group: ActionGroup;
  /** The word on a space too narrow for `label` (the swipe panel). Defaults to `label`. */
  short?: string;
}

export const ACTIONS: Record<ActionId, ActionDef> = {
  star: { label: (s) => (s.on ? "Unstar" : "Star"), icon: () => Star, group: "state" },
  // `on` is "this article is read": the action then is to mark it unread.
  read: { label: (s) => (s.on ? "Mark as unread" : "Mark as read"), icon: (s) => (s.on ? Mail : MailOpen), group: "state" },
  markAbove: { label: () => "Mark above as read", icon: () => MoveUp, group: "range" },
  markBelow: { label: () => "Mark below as read", icon: () => MoveDown, group: "range" },
  muteSimilar: { label: () => "Mute similar…", icon: () => BellOff, group: "filter" },
  restore: { label: () => "Restore", icon: () => RotateCcw, group: "filter" },
  editRule: { label: () => "Edit rule", icon: () => Pencil, group: "filter" },
  editFeed: { label: () => "Edit feed", icon: () => Rss, group: "feed" },
  openOriginal: { label: () => "Open original", icon: () => ExternalLink, group: "link" },
  copyLink: { label: () => "Copy link", icon: () => Copy, group: "link" },
  share: { label: () => "Share", icon: () => Share2, group: "link" },
  fulltext: { label: () => "Full text", icon: () => FileText, group: "state" },
  more: { label: () => "More actions", icon: () => MoreHorizontal, group: "state", short: "More" },
};

/** The one glyph for "more actions": every surface that opens a menu of them draws this, never a vertical variant. */
export const MoreIcon = MoreHorizontal;

export const actionLabel = (id: ActionId, s: ActionState = {}): string => ACTIONS[id].label(s);
export const actionShort = (id: ActionId): string => ACTIONS[id].short ?? ACTIONS[id].label({});
export const actionIcon = (id: ActionId, s: ActionState = {}): LucideIcon => ACTIONS[id].icon(s);

export type Surface = "rowMenu" | "articleMenu" | "swipeLeading" | "swipeTrailing";

/** What decides which actions a surface offers. */
export interface SurfaceContext {
  /** The browser has a share sheet. Without one Share would only copy the link, which Copy link already does. */
  canShare: boolean;
  /** Mark above and below mean something here (a page that is not in date order has no above or below). */
  ranges: boolean;
  /** A filter muted the article: it offers Restore (and Edit rule when the rule still has a name), not Mute similar. */
  muted: boolean;
  /** The muting rule can be edited. */
  hasRule: boolean;
}

/**
 * The actions a surface offers, in order. The order is the same on every surface that has the action: state first,
 * then reach, then the filter, then the feed, then links.
 */
export function surfaceActions(surface: Surface, c: SurfaceContext): ActionId[] {
  switch (surface) {
    case "swipeLeading":
      return ["read"];
    case "swipeTrailing":
      return ["star", "more"];
    case "articleMenu":
      return ["read", "muteSimilar", "editFeed", "openOriginal", "copyLink"];
    case "rowMenu": {
      const ids: ActionId[] = ["star"];
      if (c.muted) ids.push("restore", ...(c.hasRule ? (["editRule"] as const) : []));
      else ids.push("read", ...(c.ranges ? (["markAbove", "markBelow"] as const) : []), "muteSimilar");
      ids.push("editFeed", "openOriginal", "copyLink");
      if (c.canShare) ids.push("share");
      return ids;
    }
  }
}

// ---- Mark all as read ------------------------------------------------------------------------------------------

/** The icon of the Mark all control, with the other actions so it is drawn from one place too. */
export const MarkAllIcon = CheckCheck;

/** A search's Mark all: the reach is the results, however many there are. */
export const MARK_ALL_RESULTS = "Mark all results as read";

/**
 * The words for Mark all as read in the list `scope` is, naming how far it reaches. `unread` is the live unread count
 * of a feed, a folder or the library (null where the counts do not say, such as a reading-time filter or Starred).
 * It always asks first (the confirm dialog repeats this label), so a tap that reaches 214 articles says so.
 */
export function markAllLabel(scope: Scope, unread: number | null): string {
  if (scope.q) return MARK_ALL_RESULTS;
  if (scope.view === "unread" && !scope.feed && !scope.folder && !scope.length) return "Mark everything unread as read";
  if (unread !== null) return `Mark all ${unread} as read`;
  return "Mark all as read";
}
