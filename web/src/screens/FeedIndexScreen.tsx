import { useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { ArrowUpDown, TriangleAlert } from "lucide-react";
import { LATEST_PER_FEED, useBootstrap, useFeedsLatest } from "@/api/queries";
import type { Card, Feed } from "@/api/types";
import { DigestRow } from "@/layouts/digest";
import { focusRow, pageOrder, rowKeyMove, storyIntoView } from "@/layouts/rowKeys";
import { ROW_KEYSHORTCUTS, stopId } from "@/layouts/rowTabStop";
import { useDevicePrefs } from "@/lib/devicePrefs";
import { failureLine, feedName } from "@/lib/feedStatus";
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

// The Feed index (docs/design.md 7.14): one block for every feed, in the order of the sidebar or the quietest feeds
// first (the Digest's order setting), each with the feed's name, how many articles are unread and its newest headlines,
// read ones included. A failing feed says so, in the words its own list uses. The whole screen is one Tab stop; the
// arrow keys move between the feed names and the headlines, as they do in the Digest.

const noop = () => {};

interface BlockProps {
  feed: Feed;
  unread: number;
  /** The feed's newest articles, newest first. */
  cards: readonly Card[];
  tabStopId: string | undefined;
}

function FeedBlock({ feed, unread, cards, tabStopId }: BlockProps) {
  const name = feedName(feed);
  const sid = stopId("feed", feed.id);
  const failure = feed.last_error ? failureLine(feed) : "";
  return (
    <section data-feed-block={feed.id} className="pt-4">
      <h2 className="flex items-baseline gap-2 px-4 pb-1 text-sm font-semibold">
        <span data-stop-id={sid} className="min-w-0">
          <Link
            to={openListTo({ feed: feed.id })}
            tabIndex={sid === tabStopId ? undefined : -1}
            aria-keyshortcuts={ROW_KEYSHORTCUTS}
            title={feed.address_hint ? `${name} (${feed.address_hint})` : undefined}
            className="hit inline-flex max-w-full items-center truncate font-semibold tracking-wide text-accent uppercase underline-offset-2 hover:underline"
          >
            {name}
          </Link>
        </span>
        <span data-testid="feed-index-unread" className="shrink-0 text-xs font-normal text-fg2">
          {unread > 0 ? `${unread} unread` : "All read"}
        </span>
      </h2>
      {failure ? (
        <p data-testid="feed-index-failure" className="flex items-start gap-2 px-4 pb-1 text-sm text-fg2">
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-danger" />
          {failure}
        </p>
      ) : null}
      {cards.length === 0 ? (
        <p className="px-4 py-2 text-sm text-fg2">No articles yet.</p>
      ) : (
        cards.map((item) => (
          <DigestRow
            key={item.id}
            item={item}
            // The article opens inside its feed's own list, so back and next/previous stay with that feed.
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
}

export function FeedIndexScreen() {
  useDocumentTitle("Feed index");
  const wide = useWide();
  const navigate = useNavigate();
  const boot = useBootstrap();
  const latest = useFeedsLatest(LATEST_PER_FEED);
  const dp = useDevicePrefs();
  const scroller = useRef<HTMLDivElement>(null);
  const [focused, setFocused] = useState<string | undefined>(undefined);

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
  const ids = useMemo(() => blocks.flatMap((b) => [stopId("feed", b.feed.id), ...(latest.data?.get(b.feed.id)?.map((c) => c.id) ?? [])]), [blocks, latest.data]);
  const tabStopId = focused && ids.includes(focused) ? focused : ids[0];

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
  if (boot.isPending || (latest.isPending && !latest.isError)) body = <Skeleton rows={6} label="Loading the feed index" />;
  else if (boot.isError || latest.isError) {
    body = (
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
    );
  } else if (blocks.length === 0) {
    body = <EmptyState title="No feeds yet" why="Add a feed and its newest articles will be listed here." action={<Button onClick={() => navigate("/feeds", { state: { open: "add" } })}>Add a feed</Button>} />;
  } else {
    body = (
      <div data-feed-index="" className="pb-10" onKeyDownCapture={onKey} onFocus={onFocus}>
        {blocks.map((b) => (
          <FeedBlock key={b.feed.id} feed={b.feed} unread={b.unread} cards={latest.data?.get(b.feed.id) ?? []} tabStopId={tabStopId} />
        ))}
        <p className="px-4 pt-6 text-center text-sm text-fg2">That&apos;s every feed.</p>
      </div>
    );
  }

  return (
    <div className="ui-font flex h-full min-h-0 flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        <div className="flex items-center gap-1 pt-2">
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
