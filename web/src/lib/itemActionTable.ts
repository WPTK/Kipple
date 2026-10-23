import { CheckCheck, Mail, MailOpen, MoreHorizontal, Star, type LucideIcon } from "lucide-react";
import type { Scope } from "@/api/types";

/**
 * The table of what can be done to an article: its words and glyph. A surface draws an action with `actionLabel` and
 * `ActionIcon`, so a name or a glyph is changed here and nowhere else. Today the swipe panel and the list header draw
 * from it; the row menu and the open article's menus join as their files adopt it (issue #613 lists what is left).
 */

export type ActionId = "star" | "read" | "more";

/** The facts a label or glyph can depend on. */
export interface ActionState {
  /** The article is starred (star) or read (read). */
  on?: boolean;
}

interface ActionDef {
  /** What the control says, which is also its accessible name. */
  label: (s: ActionState) => string;
  icon: (s: ActionState) => LucideIcon;
  /** The word on a space too narrow for `label` (the swipe panel's More). Defaults to `label`. */
  short?: string;
}

export const ACTIONS: Record<ActionId, ActionDef> = {
  star: { label: (s) => (s.on ? "Unstar" : "Star"), icon: () => Star },
  // `on` is "this article is read": the action then is to mark it unread.
  read: { label: (s) => (s.on ? "Mark as unread" : "Mark as read"), icon: (s) => (s.on ? Mail : MailOpen) },
  more: { label: () => "More actions", icon: () => MoreHorizontal, short: "More" },
};

/** The glyph of a control that opens a menu of more actions. */
export const MoreIcon = MoreHorizontal;

export const actionLabel = (id: ActionId, s: ActionState = {}): string => ACTIONS[id].label(s);
export const actionShort = (id: ActionId): string => ACTIONS[id].short ?? ACTIONS[id].label({});
export const actionIcon = (id: ActionId, s: ActionState = {}): LucideIcon => ACTIONS[id].icon(s);

// ---- Mark all as read ------------------------------------------------------------------------------------------

export const MarkAllIcon = CheckCheck;

/** A search's Mark all: the reach is the results, however many there are. */
export const MARK_ALL_RESULTS = "Mark all results as read";

/**
 * The words for Mark all as read in the list `scope` is, naming how far it reaches. `unread` is the live unread count
 * of a feed, a folder or the library (null where the counts do not say, such as a reading-time filter or Starred, and
 * the plain words where there is nothing to count). The list's menu asks first and the dialog repeats this label.
 */
export function markAllLabel(scope: Scope, unread: number | null): string {
  if (scope.q) return MARK_ALL_RESULTS;
  if (scope.view === "unread" && !scope.feed && !scope.folder && !scope.length) return "Mark everything unread as read";
  if (unread !== null && unread > 0) return `Mark all ${unread} as read`;
  return "Mark all as read";
}
