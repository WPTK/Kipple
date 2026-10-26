import type { ComponentType } from "react";
import type { To } from "react-router";
import type { Card, Feed, Scope } from "@/api/types";
import type { LayoutId } from "@/lib/devicePrefs";
import type { DigestSection } from "./digestPlan";

/**
 * A list layout is a row component plus sizing hints, or a page that draws the whole list (Page). The list screen
 * owns data, virtualization, selection, gestures and keyboard; a row layout only draws one item.
 */
export interface RowMenuActions {
  toggleRead: (item: Card) => void;
  toggleStar: (item: Card) => void;
  markAbove: (item: Card) => void;
  markBelow: (item: Card) => void;
  openOriginal: (item: Card) => void;
  copyLink: (item: Card) => void;
  share: (item: Card) => void;
  /** Start a filter from this article ("Mute similar..."). */
  muteSimilar: (item: Card) => void;
  /** A muted article: bring it back (marks it unread, which un-mutes it). */
  restore: (item: Card) => void;
  /** A muted article: open the filter that muted it. */
  editRule: (item: Card) => void;
  /** Open the feed editor for this article's feed (how it is fetched, its interval, disable or delete it). */
  manageFeed: (item: Card) => void;
  /** Mark above and below are unavailable: a relevance-sorted search has no above or below. */
  noRange?: boolean;
}

export interface RowProps {
  item: Card;
  /** Feed for the icon; undefined for archived or unknown feeds. */
  feed: Feed | undefined;
  /** Highlighted as the selected row, or open in the reader pane. */
  selected: boolean;
  /** Where the row's primary link goes (the article route). */
  to: To;
  onOpen: (item: Card) => void;
  onToggleStar: (item: Card) => void;
  actions: RowMenuActions;
  /** Inbox only: show the trailing thumbnail when the item has one. */
  showThumb?: boolean;
}

/** One section of a Digest page as the page draws it (digestPlan's section, plus what loading it has come to). */
export interface DigestSectionView extends DigestSection {
  /** The section's first load failed (null when it did not, or when it has cards to show whatever failed after). */
  error: unknown;
  /** The cards pressed for are being loaded. */
  loadingMore: boolean;
  /** Any request for this section is out (its first page, a retry, a next page). */
  busy: boolean;
  /** The last attempt at more cards failed (the cards shown stay). */
  moreFailed: boolean;
}

/** What the Digest page is given besides the list: its sections, and what the page can ask of them. */
export interface DigestView {
  /** The sections to draw, in order; sections not asked for yet are left out (they load as the end of the page nears). */
  sections: DigestSectionView[];
  /** Cards each section shows before "N more" (the device's Rows per section). */
  rows: number;
  /** Rows have a picture (the device's Images). */
  images: boolean;
  /** A section's heading says how many articles it holds ("3 new"): the New page. */
  counts: boolean;
  /** The "N more" button names its section ("7 more in Tech"); else the heading above it does ("7 more"). */
  named: boolean;
  /** "N more" was pressed in a section: show the next batch. */
  expand: (key: string) => void;
  /** Ask again for a section whose load failed. */
  retry: (key: string) => void;
}

/** What a page layout is given: the whole loaded list, not one row. */
export interface PageProps {
  /** Every loaded article of the list in the server's order. Articles marked read stay (they fade in place). */
  items: readonly Card[];
  /** The list being shown (never a search). */
  scope: Scope;
  /** The server has more articles than are loaded. */
  more: boolean;
  /** The list's measured width in px (0 before it is measured). */
  width: number;
  /** The article selected (arrow keys) or open in the reader pane. */
  selectedId: string | undefined;
  /** The story that last had focus, the list's one Tab stop (rowTabStop.ts); else the layout picks the selected one. */
  tabStopId?: string;
  onOpen: (item: Card) => void;
  /** What a story's menu does (the list's own, as for a row). */
  actions: RowMenuActions;
  /** Set by a list that is cut into sections of its own (the Digest); `items` are then the cards its sections show. */
  digest?: DigestView;
}

export interface ListLayout {
  id: LayoutId;
  label: string;
  /**
   * A layout that arranges the whole list at once. The list screen then hands it every loaded article instead of
   * virtualizing rows, and keeps the data, keys, loading and empty states. ArrowUp/ArrowDown follow the page's DOM order (pageOrder), which a
   * page layout must keep equal to its reading order. A search shows the results rows instead: a page is planned from a
   * newest-first list, and a search is ordered by relevance or by the reader's choice.
   */
  Page?: ComponentType<PageProps>;
  /**
   * Set by a page that is cut into sections, each a list of its own (digestSource.ts): by folder, feed or reading
   * time, as the device chose. Without it the page is one list.
   */
  sectioned?: true;
  /** One list row. A page layout draws its list whole and has none. */
  Row?: ComponentType<RowProps>;
  /**
   * Initial size guess in px for the virtualizer; rows are measured after render. A page layout has none. `ctx` is the list's measured
   * width and root font size (0 width before it is measured), for layouts whose rows change shape with width.
   */
  estimateRow?: (item: Card, ctx?: { width: number; rem: number }) => number;
  /** A grid of cards: several items per virtual row, and the article opens full width (no reader pane). */
  grid?: boolean;
  /** Width of the list column beside the reader pane, rem. */
  paneRem: number;
}

/** A layout that draws the list as rows (every layout but the Gazette and the Digest). */
export type RowLayout = ListLayout & Required<Pick<ListLayout, "Row" | "estimateRow">>;
