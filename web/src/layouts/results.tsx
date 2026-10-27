import { InboxRow, inbox } from "./inbox";
import { Bone } from "@/ui/kit";
import type { RowLayout, RowProps } from "./types";

/**
 * A search's results: fixed text rows whatever layout the device reads in. Title, source and time, one line of the
 * match, no picture. Row anatomy is Inbox's, so a result reads like an unread-list row and a search never looks
 * like a different app.
 */
function ResultRow(p: RowProps) {
  return <InboxRow {...p} showThumb={false} snippetLines={1} />;
}

/** Loading stand-in for a result: title, source line, one line of the match, no picture. */
function ResultSkeleton() {
  return (
    <div aria-hidden="true" className="flex min-h-[var(--row-min)] flex-col gap-[var(--row-gap)] border-b border-line py-[var(--row-py)] pr-4 pl-5">
      <Bone className="h-4 w-11/12" />
      <Bone className="h-3 w-1/3" />
      <Bone className="h-3 w-3/4" />
    </div>
  );
}

export const results: RowLayout = { ...inbox, label: "Search results", Row: ResultRow, Skeleton: ResultSkeleton, estimateRow: () => 84 };