import { memo, useMemo } from "react";
import { Link, useNavigate } from "react-router";
import { ArrowUpDown, TriangleAlert } from "lucide-react";
import { LATEST_PER_FEED, useBootstrap, useFeedsLatest } from "@/api/queries";
import type { Feed, Headline } from "@/api/types";
import { cn } from "@/lib/cn";
import { DigestRow } from "@/layouts/digest";
import { LIST_COLUMN } from "@/layouts";
import { focusRow, pageOrder, rowKeyMove, storyIntoView } from "@/layouts/rowKeys";
import { ROW_KEYSHORTCUTS, stopId } from "@/layouts/rowTabStop";
import { useDevicePrefs } from "@/lib/devicePrefs";
import { failureLine, feedName, showsFailure } from "@/lib/feedStatus";
import { useReturnPlace } from "@/lib/returnPlace";
import { openListTo, articleTo } from "@/lib/routes";
import { byFeed } from "@/lib/sections";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useWide } from "@/lib/useMedia";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { groupFeeds, orderSections } from "@/layouts/digestPlan";
import { BackButton } from "@/ui/BackButton";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { Notice, Skeleton } from "@/ui/kit";
import { DropdownMenu } from "@/ui/menu";
import { SectionOrderChoice } from "./LayoutMenu";
import { useFeedTimes } from "./FeedRepair";

// The Feed index (docs/design.md 7.14): one block for every feed, in the order of the sidebar or the quietest feeds
// first (the Digest's order setting), each with the feed's name, how many articles are unread and its newest headlines,
// read ones included. A failing feed says so, in the words its own list uses. The whole screen is one Tab stop; the
// arrow keys move between the feed names and the headlines, as they do in the Digest.

const noop = () => {};

/** The note under a failing feed's name: what its own list says, and when Kipple tries again. */
function Failure({ feed }: { feed: Feed }) {
  const times = useFeedTimes(feed.id);
  return (
    <p data-testid="feed-index-failure" className="flex items-start gap-2 px-4 pb-1 text-sm text-fg2">
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-danger" />
      {failureLine(feed, times.retry)}
    </p>
  );
}

interface BlockProps {
  feed: Feed;
  unread: number;
  /** The feed's newest headlines, newest first. */
  cards: readonly Headline[];
  /** The page's Tab stop when it is in this block, else null: a key press redraws the two blocks it moves between. */
  tabStopId: string | null;
}

const FeedBlock = memo(function FeedBlock({ feed, unread, cards, tabStopId }: BlockProps) {
  const name = feedName(feed);
  const sid = stopId("feed", feed.id);
  return (
    <section data-feed-block={feed.id} className="pt-4">
      <h2 className="flex items-baseline gap-2 px-4 pb-1 text-sm font-semibold">
        <span data-stop-id={sid} className="min-w-0">
          <Link
            to={openListTo({ feed: feed.id })}
            tabIndex={sid === tabStopId ? undefined : -1}
            aria-keyshortcuts={ROW_KEYSHORTCUTS}
            title={feed.address_hint ? `${name} (${feed.address_hint})` : undefined}
            className="hit inline-flex max-w-full items-center font-semibold tracking-wide text-accent uppercase underline-offset-2 hover:underline"
          >
            {/* The ellipsis goes on the text's own box: text-overflow does nothing on the flex box that centres it in the hit area. */}
            <span className="truncate">{name}</span>
          </Link>
        </span>
        <span data-testid="feed-index-unread" className="shrink-0 text-xs font-normal text-fg2">
          {unread > 0 ? `${unread} unread` : "All read"}
        </span>
      </h2>
      {/* In "all" with nothing pending this is just !!last_error; it goes through showsFailure to stay in step with the lists' own rule. */}
      {showsFailure("all", 0, feed) ? <Failure feed={feed} /> : null}
      {cards.length === 0 ? (
        <p className="px-4 py-2 text-sm text-fg2">No articles to show.</p>
      ) : (
        cards.map((item) => (
          <DigestRow
            key={item.id}
            item={item}
            // The article opens inside its feed's own list, so next and previous stay with that feed.
            to={articleTo(item.id, { view: "all", feed: feed.id })}
            onOpen={noop}
            images={false}
            source={false}
            selected={false}
            tabStop={item.id === tabStopId}
          />
        ))
      )}
    </section>
  );
});

export function FeedIndexScreen() {
  useDocumentTitle("Feed index");
  const wide = useWide();
  const navigate = useNavigate();
  const boot = useBootstrap();
  const latest = useFeedsLatest(LATEST_PER_FEED);
  const dp = useDevicePrefs();
  const data = boot.data && latest.data ? latest.data : undefined;
  // Coming back from an article puts the page where it was, with the row last visited focused.
  const { scroller, stop: focused, setStop: setFocused } = useReturnPlace<HTMLDivElement>("feed-index", !!data);

  const feeds = useMemo(() => visibleFeeds(boot.data?.feeds), [boot.data?.feeds]);
  const folders = boot.data?.folders;
  // The planner is the Digest's: one section for each feed, ordered by the device's setting.
  const blocks = useMemo(() => {
    if (!folders) return [];
    const byId = new Map(feeds.map((f) => [f.id, f]));
    return orderSections(groupFeeds(feeds, folders, byFeed(feeds), "unread"), dp.digestOrder).flatMap((d) => {
      const feed = byId.get(d.feedIds[0]!);
      return feed ? [{ feed, unread: d.count ?? feed.unread }] : [];
    });
  }, [feeds, folders, dp.digestOrder]);

  // The Tab stop is the first thing on the screen until focus moves; a stop that is no longer drawn gives it back.
  const owner = useMemo(() => {
    const own = new Map<string, string>();
    for (const b of blocks) {
      own.set(stopId("feed", b.feed.id), b.feed.id);
      for (const c of data?.get(b.feed.id) ?? []) own.set(c.id, b.feed.id);
    }
    return own;
  }, [blocks, data]);
  const first = blocks[0] ? stopId("feed", blocks[0].feed.id) : undefined;
  const tabStopId = focused && owner.has(focused) ? focused : first;
  const tabFeed = tabStopId ? owner.get(tabStopId) : undefined;

  const onKey = (e: React.KeyboardEvent) => {
    const moved = rowKeyMove(
      e,
      () => pageOrder(scroller.current),
      (row) => Math.max(1, Math.floor((scroller.current?.clientHeight ?? 0) / Math.max(1, row.offsetHeight))),
    );
    const next = moved?.to;
    if (!next) return;
    setFocused(next);
    storyIntoView(scroller.current, next);
    requestAnimationFrame(() => requestAnimationFrame(() => focusRow(scroller.current, next)));
  };
  const onFocus = (e: React.FocusEvent) => {
    const el = e.target instanceof HTMLElement ? e.target.closest<HTMLElement>("[data-item-id],[data-stop-id]") : null;
    const id = el?.dataset.itemId ?? el?.dataset.stopId;
    if (id) setFocused(id);
  };

  let body;
  if (!data) {
    // An error only replaces the page when there is nothing to show: a refetch that fails keeps what is on screen.
    const failedLoad = (boot.isError && !boot.data) || (latest.isError && !latest.data);
    body = failedLoad ? (
      <div className="p-4">
        <Notice tone="error">
          Couldn&apos;t load the feed index. Check your connection.{" "}
          <Button
            variant="link"
            onClick={() => {
              if (boot.isError) void boot.refetch();
              if (latest.isError) void latest.refetch();
            }}
          >
            Try again
          </Button>
        </Notice>
      </div>
    ) : (
      <Skeleton rows={6} label="Loading the feed index" />
    );
  } else if (blocks.length === 0) {
    body = <EmptyState title="No feeds yet" action={<Button onClick={() => navigate("/feeds", { state: { open: "add" } })}>Add a feed</Button>} />;
  } else {
    body = (
      <div data-feed-index="" className={cn("pb-10", LIST_COLUMN)} onKeyDownCapture={onKey} onFocus={onFocus}>
        {blocks.map((b) => (
          <FeedBlock key={b.feed.id} feed={b.feed} unread={b.unread} cards={data.get(b.feed.id) ?? NONE} tabStopId={b.feed.id === tabFeed ? (tabStopId ?? null) : null} />
        ))}
        <p className="px-4 pt-6 text-center text-sm text-fg2">That&apos;s every feed.</p>
      </div>
    );
  }

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        <div className={cn("flex items-center gap-1 pt-2", LIST_COLUMN)}>
          {!wide ? <BackButton to="/feeds" ariaLabel="Back to Feeds" /> : null}
          <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
            Feed index
          </h1>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <Button variant="ghost" size="icon" aria-label="Order">
                <ArrowUpDown aria-hidden="true" />
              </Button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-56">
                <SectionOrderChoice name="feeds" />
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
      </header>
      <div ref={scroller} data-testid="feed-index-scroll" className="min-h-0 flex-1 overflow-y-auto bg-bg text-fg">
        {body}
      </div>
    </div>
  );
}

/** A feed the answer leaves out (nothing unmuted to show). One array, so the block's props stay equal between renders. */
const NONE: readonly Headline[] = [];
