import { InboxRow, inbox } from "./inbox";
import type { ListLayout, RowProps } from "./types";

/**
 * A search's results: fixed text rows whatever layout the device reads in. Title, source and time, one line of the
 * match, no picture. Row anatomy is Inbox's, so a result reads like an unread-list row and a search never looks
 * like a different app.
 */
function ResultRow(p: RowProps) {
  return <InboxRow {...p} showThumb={false} snippetLines={1} />;
}

export const results: ListLayout = { ...inbox, label: "Search results", Row: ResultRow, estimateRow: () => 84 };