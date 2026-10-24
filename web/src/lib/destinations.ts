import { BarChart3, BellOff, Inbox, Library, Newspaper, Rss, Search, Settings, Star, type LucideIcon } from "lucide-react";

/**
 * Every place the sidebar and the tab bar lead to, in one list: the name, the icon and the address are written once,
 * and `currentDestination` says which one the reader is in. The words are the glossary in docs/maintainers/ui-decisions.md.
 */
export type DestinationId = "unread" | "all" | "starred" | "muted" | "search" | "feeds" | "stats" | "settings";

export interface Destination {
  id: DestinationId;
  label: string;
  /** The shorter name on the Show pills of a list header. */
  pill: string;
  icon: LucideIcon;
  to: string;
}

export const DESTINATIONS: Record<DestinationId, Destination> = {
  unread: { id: "unread", label: "Unread", pill: "Unread", icon: Inbox, to: "/l/unread" },
  all: { id: "all", label: "All articles", pill: "All", icon: Library, to: "/l/all" },
  starred: { id: "starred", label: "Starred", pill: "Starred", icon: Star, to: "/l/starred" },
  muted: { id: "muted", label: "Muted", pill: "Muted", icon: BellOff, to: "/l/muted" },
  search: { id: "search", label: "Search", pill: "Search", icon: Search, to: "/search" },
  feeds: { id: "feeds", label: "Feeds", pill: "Feeds", icon: Rss, to: "/feeds" },
  stats: { id: "stats", label: "Stats", pill: "Stats", icon: BarChart3, to: "/stats" },
  settings: { id: "settings", label: "Settings", pill: "Settings", icon: Settings, to: "/settings" },
};

/** The four lists of the whole library, in the order the sidebar and the Show pills give them. */
export const LISTS: Destination[] = [DESTINATIONS.unread, DESTINATIONS.all, DESTINATIONS.starred, DESTINATIONS.muted];

/** The phone tab bar. Its first tab holds the four lists as pills, so it has its own name and icon. */
export const ARTICLES_TAB = { label: "Articles", icon: Newspaper, to: DESTINATIONS.unread.to } as const;

/** A list narrowed to one feed or folder (`?feed=`, `?folder=`): it is part of Feeds, not of the whole-library lists. */
export function isScopedList(search: string): boolean {
  const q = new URLSearchParams(search);
  return q.has("feed") || q.has("folder");
}

/** True for the destinations that are one of the four whole-library lists. */
export function isList(id: DestinationId | null): boolean {
  return id === "unread" || id === "all" || id === "starred" || id === "muted";
}

/**
 * The destination an address belongs to, or null for a screen that is none of them (an article, setup). The sidebar,
 * the tab bar and the Show pills all read this, so they cannot disagree: a feed's or a folder's list and Feed health
 * belong to Feeds, Your year to Stats.
 */
export function currentDestination(pathname: string, search = ""): DestinationId | null {
  const [, first = "", second = ""] = pathname.split("/");
  switch (first) {
    case "l":
      if (isScopedList(search)) return "feeds";
      return second === "all" || second === "starred" || second === "muted" ? second : "unread";
    case "feeds":
    case "health":
      return "feeds";
    case "search":
      return "search";
    case "stats":
      return "stats";
    case "settings":
      return "settings";
    default:
      return null;
  }
}
