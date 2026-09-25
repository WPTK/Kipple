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
  /** Mark above/below is off for relevance-sorted search. */
  canRange: boolean;
  openOriginal: (item: Card) => void;
  copyLink: (item: Card) => void;
  share: (item: Card) => void;
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
  /** Initial size guess in px for the virtualizer; rows are measured after render. */
  estimateRow: (item: Card) => number;
  /** A grid of cards: several items per virtual row, and the article opens full width (no reader pane). */
  grid?: boolean;
  /** Width of the list column beside the reader pane, rem. */
  paneRem: number;
}
