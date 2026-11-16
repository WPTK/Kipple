import { InboxRow, inbox } from "./inbox";
import { BoneLines } from "@/ui/kit";
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
      <BoneLines lines={2} className="text-sm leading-snug" />
      <BoneLines lines={1} className="text-xs" />
      <BoneLines lines={1} className="text-sm leading-snug" />
    </div>
  );
}

export const results: RowLayout = { ...inbox, label: "Search results", Row: ResultRow, Skeleton: ResultSkeleton, estimateRow: () => 84 };