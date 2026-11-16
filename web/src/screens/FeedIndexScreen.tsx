import { Fragment, memo, useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowUpDown, TriangleAlert } from "lucide-react";
import { invalidateFeeds } from "@/api/admin";
import { LATEST_PER_FEED, keys, useBootstrap, useFeedsLatest } from "@/api/queries";
import type { Feed, Folder, Headline } from "@/api/types";
import { cn } from "@/lib/cn";
import { DigestRow } from "@/layouts/digest";
import { LIST_COLUMN } from "@/layouts";
import { focusRow, pageOrder, rowKeyMove, storyIntoView } from "@/layouts/rowKeys";
import { ROW_KEYSHORTCUTS, stopId } from "@/layouts/rowTabStop";
import { useDevicePrefs } from "@/lib/devicePrefs";
import { failureLine, feedName, showsFailure } from "@/lib/feedStatus";
import { folderPath, folderTree } from "@/lib/folderTree";
import { useItemActions } from "@/lib/itemActions";
import { MarkAllIcon, MoreIcon, markAllLabel, markAllReach } from "@/lib/itemActionTable";
import { RevealMore, useReveal } from "@/lib/reveal";
import { useReturnPlace } from "@/lib/returnPlace";
import { openListTo, articleTo } from "@/lib/routes";
import { byFeed } from "@/lib/sections";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useLatestRef } from "@/lib/useLatestRef";
import { useWide } from "@/lib/useMedia";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { groupFeeds, orderSections } from "@/layouts/digestPlan";
import { BackButton } from "@/ui/BackButton";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { Icon } from "@/ui/Icon";
import { Notice, Skeleton, inputCls } from "@/ui/kit";
import { UnreadCount, countText } from "@/ui/UnreadCount";
import { DropdownMenu } from "@/ui/menu";
import { SectionOrderChoice } from "./LayoutMenu";
import { useFeedTimes } from "./FeedRepair";
import { useMarkAllConfirm } from "./MarkAllConfirm";

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
  /** Ask to mark the feed's unread articles read (stable between renders, so the block stays as it is). */
  onMarkRead: (feed: Feed, unread: number) => void;
}

const FeedBlock = memo(function FeedBlock({ feed, unread, cards, tabStopId, onMarkRead }: BlockProps) {
  const name = feedName(feed);
  const sid = stopId("feed", feed.id);
  return (
    <section data-feed-block={feed.id} className="pt-4">
      <div className="flex items-center pr-2">
      <h2 className="flex min-w-0 flex-1 items-center gap-2 px-4 pb-1 text-sm font-semibold">
        <span data-stop-id={sid} className="min-w-0">
          <Link
            to={openListTo({ feed: feed.id })}
            data-reveal-key={feed.id}
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
          {unread > 0 ? <UnreadCount n={unread} /> : "All read"}
        </span>
      </h2>
      {/* Always drawn where there is something to mark, touch screens included: it is the row's More button. It is a Tab stop only in the block that holds the page's one. */}
      {unread > 0 ? (
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <Button variant="ghost" size="icon" tabIndex={tabStopId === null ? -1 : undefined} aria-label={`More actions for ${name}`}>
              <MoreIcon aria-hidden="true" />
            </Button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-48">
              <DropdownMenu.Item onSelect={() => onMarkRead(feed, unread)}>
                <Icon of={MarkAllIcon} />
                Mark feed as read
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
      ) : null}
      </div>
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
  const qc = useQueryClient();
  const act = useItemActions();
  const markAll = useMarkAllConfirm();
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

  // Mark a feed's unread articles read, after the question every bulk mark asks; the Undo toast is the bulk mark's own.
  const markFeedRead = (feed: Feed, unread: number) => {
    const scope = { view: "unread", feed: feed.id } as const;
    markAll.ask(markAllLabel(scope, unread), markAllReach(scope, feedName(feed)), () => {
      void act.markAll(scope, undefined, []).then(() => {
        invalidateFeeds(qc);
        void qc.invalidateQueries({ queryKey: keys.feedsLatest });
      });
    });
  };
  const markFeedReadRef = useLatestRef(markFeedRead);
  const onMarkRead = useCallback((feed: Feed, unread: number) => markFeedReadRef.current(feed, unread), [markFeedReadRef]);

  // In the sidebar's order the blocks sit in their folders: a folder's name is a label above its first feed, and the
  // jump list at the top goes to it. In the quietest-first order a folder's feeds are not together, so neither is drawn.
  const grouped = dp.digestOrder === "sidebar";
  const ftree = useMemo(() => (folders ? folderTree(folders) : null), [folders]);
  const groups = useMemo(() => {
    if (!grouped || !ftree) return [];
    const out: { folder: Folder; first: string }[] = [];
    for (const b of blocks) {
      const folder = ftree.byId.get(b.feed.folder_id);
      if (folder && out.at(-1)?.folder.id !== folder.id) out.push({ folder, first: b.feed.id });
    }
    return out;
  }, [grouped, ftree, blocks]);
  const groupAt = useMemo(() => new Map(groups.map((g) => [g.first, g.folder])), [groups]);
  // The folder asked for in the jump list: drawn (with every block before it) and then scrolled to once it is on screen.
  const [jump, setJump] = useState<string | null>(null);
  useEffect(() => {
    if (!jump) return;
    const el = scroller.current?.querySelector<HTMLElement>(`[data-folder-group="${CSS.escape(jump)}"]`);
    if (!el) return;
    el.scrollIntoView?.({ block: "start" });
    el.focus({ preventScroll: true });
    setJump(null);
  }, [jump, scroller]);

  // The Tab stop is the first thing on the screen until focus moves; a stop that is no longer drawn gives it back.
  const owner = useMemo(() => {
    const own = new Map<string, string>();
    for (const b of blocks) {
      own.set(stopId("feed", b.feed.id), b.feed.id);
      for (const c of data?.get(b.feed.id) ?? []) own.set(c.id, b.feed.id);
    }
    return own;
  }, [blocks, data]);
  // A library of thousands of feeds draws its blocks a step at a time, as the sidebar and the Feeds screen draw their rows (issue #463).
  const blockKeys = useMemo(() => blocks.map((b) => b.feed.id), [blocks]);
  const reveal = useReveal(blockKeys);
  // The block of the row last visited is drawn with every block before it. Asked for while rendering, not in an
  // effect: on the way back useReturnPlace looks for that row once, in its layout effect, so the row has to be in
  // the first commit that has the data.
  const focusedFeed = focused ? owner.get(focused) : undefined;
  const [held, setHeld] = useState<string | undefined>(undefined);
  if (held !== focusedFeed) {
    setHeld(focusedFeed);
    if (focusedFeed) reveal.ensure(focusedFeed);
  }
  const first = blocks[0] ? stopId("feed", blocks[0].feed.id) : undefined;
  // A stop whose block is not drawn (its hold has ended) gives the Tab stop back to the first row.
  const tabStopId = focused && focusedFeed && reveal.shown(focusedFeed) ? focused : first;
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
      <>
        {groups.length > 1 ? (
          <div className={cn("px-4 pt-3", LIST_COLUMN)}>
            <select
              aria-label="Jump to a folder"
              value=""
              onChange={(e) => {
                const g = groups.find((x) => x.folder.id === e.target.value);
                if (!g) return;
                reveal.ensure(g.first);
                // The Tab stop goes to the folder's first block, so Tab from the label reaches its heading link and menu.
                setFocused(stopId("feed", g.first));
                setJump(g.folder.id);
              }}
              className={cn(inputCls, "w-auto max-w-full")}
            >
              <option value="">Jump to a folder</option>
              {groups.map((g) => (
                <option key={g.folder.id} value={g.folder.id}>
                  {ftree ? folderPath(ftree, g.folder.id) : g.folder.name}
                  {g.folder.unread > 0 ? ` (${countText(g.folder.unread)})` : ""}
                </option>
              ))}
            </select>
          </div>
        ) : null}
        <div data-feed-index="" className={cn("pb-10", LIST_COLUMN)} onKeyDownCapture={onKey} onFocus={onFocus}>
          {blocks.map((b) => {
            if (!reveal.shown(b.feed.id)) return null;
            const folder = groupAt.get(b.feed.id);
            return (
              <Fragment key={b.feed.id}>
                {folder && ftree ? (
                  <p data-folder-group={folder.id} tabIndex={-1} className="mt-4 flex items-center gap-2 border-b border-line px-4 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase outline-offset-2">
                    <span className="min-w-0 truncate">{folderPath(ftree, folder.id)}</span>
                    <UnreadCount n={folder.unread} />
                  </p>
                ) : null}
                <FeedBlock feed={b.feed} unread={b.unread} cards={data.get(b.feed.id) ?? NONE} tabStopId={b.feed.id === tabFeed ? (tabStopId ?? null) : null} onMarkRead={onMarkRead} />
              </Fragment>
            );
          })}
          <RevealMore reveal={reveal} noun="feeds" />
          {reveal.left === 0 ? <p className="px-4 pt-6 text-center text-sm text-fg2">That&apos;s every feed.</p> : null}
        </div>
      </>
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
      <div ref={scroller} data-reveal-root="" data-testid="feed-index-scroll" className="min-h-0 flex-1 overflow-y-auto bg-bg text-fg">
        {body}
      </div>
      {markAll.dialog}
    </div>
  );
}

/** A feed the answer leaves out (nothing unmuted to show). One array, so the block's props stay equal between renders. */
const NONE: readonly Headline[] = [];
