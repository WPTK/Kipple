import { Link } from "react-router";
import type { Feed } from "@/api/types";
import { failureNoteText } from "@/lib/feedStatus";
import { useFeedTimes } from "./FeedRepair";

/**
 * A note at the top of a feed's or folder's list when a feed in it can't be fetched, so older articles are not mistaken
 * for current ones (issue #511). `failing` comes from `failureNoteFeeds`, which also decides whether the note shows at
 * all. It is the first thing inside the list's scrolling content, so it scrolls away and never takes reading room for
 * as long as the list is open.
 *
 * Not a live region: it is part of the list's page, and announcing the error on every navigation to the list would
 * repeat what the reader already knows. It is two clamped lines (always two lines high, so the retry time arriving from
 * Feed health does not move the list) and a link; the raw text of every feed's error is on
 * Feed health, which the link opens (a tooltip alone would not reach touch or keyboard).
 */
export function FeedFailureNote({ failing, inFolder }: { failing: readonly Feed[]; inFolder: boolean }) {
  const times = useFeedTimes(failing[0]?.id ?? "");
  if (failing.length === 0) return null;
  const text = failureNoteText(failing, inFolder, times.retry);
  return (
    <div data-testid="feed-failure-note" className="mx-3 flex flex-col gap-0.5 rounded-xl border border-line bg-surface px-3 py-2 text-sm text-fg2">
      <p title={text} className="line-clamp-2 min-h-10 break-words">
        {text}
      </p>
      <Link to="/health" className="hit inline-flex items-center self-start text-link underline underline-offset-2">
        Feed health
      </Link>
    </div>
  );
}
