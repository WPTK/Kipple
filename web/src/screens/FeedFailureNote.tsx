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
 * repeat what the reader already knows. The sentence is two clamped lines (always two lines high); the raw text of
 * every feed's error is on Feed health, which the link opens (a tooltip alone would not reach touch or keyboard).
 *
 * Only a note about one feed says when it is tried again, so only that note asks Feed health for the time. The time is
 * a line of its own under the sentence, outside the clamp so a long name cannot push it away, and its height is kept
 * while the time loads or is not known, so the list does not move when it arrives.
 */
export function FeedFailureNote({ failing, inFolder }: { failing: readonly Feed[]; inFolder: boolean }) {
  const [one] = failing;
  if (!one) return null;
  return failing.length === 1 ? <TimedNote feed={one} inFolder={inFolder} /> : <Note text={failureNoteText(failing, inFolder)} />;
}

function TimedNote({ feed, inFolder }: { feed: Feed; inFolder: boolean }) {
  const times = useFeedTimes(feed.id);
  return <Note text={failureNoteText([feed], inFolder)} retry={times.retry} />;
}

/** `retry` is only passed by a note about one feed: undefined has no retry line at all, "" keeps the line's height. */
function Note({ text, retry }: { text: string; retry?: string }) {
  const when = retry ? `${retry}.` : "";
  return (
    <div data-testid="feed-failure-note" className="mx-3 flex flex-col gap-0.5 rounded-xl border border-line bg-surface px-3 py-2 text-sm text-fg2">
      <p title={[text, when].filter(Boolean).join(" ")} className="line-clamp-2 min-h-10 break-words">
        {text}
      </p>
      {retry === undefined ? null : (
        <p data-testid="feed-failure-retry" className="min-h-5">
          {when}
        </p>
      )}
      <Link to="/health" className="hit inline-flex items-center self-start text-link underline underline-offset-2">
        Feed health
      </Link>
    </div>
  );
}
