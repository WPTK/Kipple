import { createContext, useContext } from "react";

/**
 * The article list is one Tab stop (a roving tab stop): only the current row's controls are in the Tab order, the
 * other rows' are reached with the arrow keys (ListPane). A list that loads more rows as focus moves down
 * would otherwise be a Tab order with no end, and nothing after it (the reader pane) could be reached. The list sets
 * this per row; anything outside a list (the default) stays in the Tab order.
 */
export const RowTabStop = createContext(true);

/** The tabIndex of a control in a list row: left alone in the current row, -1 in the others. */
export function useRowTabIndex(): -1 | undefined {
  return useContext(RowTabStop) ? undefined : -1;
}
