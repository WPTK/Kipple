import type { ComponentType } from "react";
import type { To } from "react-router";
import type { Card, Feed } from "@/api/types";
import type { LayoutId } from "@/lib/devicePrefs";

/**
 * A list layout is a row component plus sizing hints. The list screen owns
 * data, virtualization, selection, gestures and keyboard; a layout only draws one item.
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
  /** Highlighted by keyboard selection, or open in the reader pane. */
  selected: boolean;
  /** Ticked with `x` for a batch action. */
  checked: boolean;
  /** Where the row's primary link goes (the article route). */
  to: To;
  onOpen: (item: Card) => void;
  onToggleStar: (item: Card) => void;
  actions: RowMenuActions;
  /** Inbox only: show the trailing thumbnail when the item has one. */
  showThumb?: boolean;
}

export interface ListLayout {
  id: LayoutId;
  label: string;
  Row: ComponentType<RowProps>;
  /**
   * Initial size guess in px for the virtualizer; rows are measured after render. `ctx` is the list's measured
   * width and root font size (0 width before it is measured), for layouts whose rows change shape with width.
   */
  estimateRow: (item: Card, ctx?: { width: number; rem: number }) => number;
  /** A grid of cards: several items per virtual row, and the article opens full width (no reader pane). */
  grid?: boolean;
  /** Width of the list column beside the reader pane, rem. */
  paneRem: number;
}
