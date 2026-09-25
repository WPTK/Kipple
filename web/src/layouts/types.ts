import type { ComponentType } from "react";
import type { To } from "react-router";
import type { Card, Feed } from "@/api/types";
import type { LayoutId } from "@/lib/prefs";

/**
 * A list layout is a row component plus sizing hints. The list screen owns
 * data, virtualization, selection and keyboard; a layout only draws one item.
 * Cards, Compact, Inbox and Headlines slot in here in a later step.
 */
export interface RowProps {
  item: Card;
  /** Feed for the icon; undefined for archived or unknown feeds. */
  feed: Feed | undefined;
  /** Highlighted by keyboard selection, or open in the reader pane. */
  selected: boolean;
  /** Where the row's primary link goes (the article route). */
  to: To;
  onOpen: (item: Card) => void;
  onToggleStar: (item: Card) => void;
}

export interface ListLayout {
  id: LayoutId;
  label: string;
  Row: ComponentType<RowProps>;
  /** Initial size guess in px for the virtualizer; rows are measured after render. */
  estimateRow: (item: Card) => number;
}
