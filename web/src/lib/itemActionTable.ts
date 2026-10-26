import { CheckCheck, Mail, MailOpen, MoreHorizontal, Star, type LucideIcon } from "lucide-react";
import type { Scope } from "@/api/types";
import { READING_LENGTH_LABELS } from "@/lib/readingLength";

/**
 * The table of what can be done to an article: its words and glyph. A surface draws an action with `actionLabel` and
 * `ActionIcon`, so a name or a glyph is changed here and nowhere else. Today the swipe panel, the list header, Search and
 * Feed health draw from it; the row menu and the open article's menus join as their files adopt it (issue #613 lists
 * what is left).
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

const ACTIONS: Record<ActionId, ActionDef> = {
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

/** Mark above / Mark below in a row menu: the rows on one side of the article, in the list's own order. */
export const markRangeLabel = (side: "above" | "below"): string => `Mark ${side} as read`;

/** The auto-read catch-up: the articles older than the limit. `count` is already formatted. */
export const markOlderLabel = (count: string): string => `Mark ${count} older articles as read`;

/** A search's Mark all: the reach is the results, however many there are. */
const MARK_ALL_RESULTS = "Mark all results as read";

/**
 * The words for Mark all as read in the list `scope` is, naming how far it reaches. `unread` is the live unread count
 * of a feed, a folder or the library, or the unread results of a search once all are loaded (null where the counts do not say, such as an article length filter or Starred, and
 * the plain words where there is nothing to count). The list's menu asks first and the dialog repeats this label.
 */
export function markAllLabel(scope: Scope, unread: number | null): string {
  // A search names the unread results it will mark, once they are all loaded (so the count is the whole of them).
  if (scope.q) return unread !== null && unread > 1 ? `Mark all ${unread} unread results as read` : MARK_ALL_RESULTS;
  if (unread === 0) return "Mark all as read";
  // The New page marks what is new, not everything unread: its count is the visit's.
  if (scope.after) return unread !== null && unread > 0 ? `Mark all ${unread} as read` : "Mark all as read";
  if (scope.view === "unread" && !scope.feed && !scope.folder && !scope.length) return "Mark everything unread as read";
  if (unread !== null && unread > 0) return `Mark all ${unread} as read`;
  return "Mark all as read";
}

/**
 * What the Mark all dialog says it marks: the unread articles of the list `scope` is. The Starred view and a
 * article length filter both narrow the reach, so they are named. `place` is the feed or folder title (else "this list").
 */
export function markAllReach(scope: Scope, place = "this list"): string {
  if (scope.after) return "unread articles that arrived since you last looked";
  const kind = scope.view === "starred" ? "unread starred articles" : "unread articles";
  return `${kind}${scope.length ? ` (${READING_LENGTH_LABELS[scope.length]})` : ""} in ${place}`;
}
