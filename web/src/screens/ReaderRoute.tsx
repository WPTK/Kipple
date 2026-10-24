import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { Link, Navigate, useMatch, useNavigate, useSearchParams } from "react-router";
import { ArrowDownWideNarrow, ArrowUpNarrowWide, ChevronLeft, ChevronRight, RefreshCw, Settings, Timer, Undo2, X } from "lucide-react";
import { MarkAllIcon, MoreIcon, markAllLabel, markAllReach } from "@/lib/itemActionTable";
import { useMarkAllConfirm } from "./MarkAllConfirm";
import { keys, scopeKey, useBootstrap, useFolderTree } from "@/api/queries";
import { PATH_SEP, feedOrder, folderTree, parentPath, subtreeFeeds } from "@/lib/folderTree";
import { useRefreshAll, useRefreshing } from "@/api/refresh";
import type { Bootstrap, ItemsPage, Scope, View } from "@/api/types";
import { useSearchHighlight } from "@/lib/useHighlights";
import { pageOf, useListContext, useResolvedLayout, useSetListOverride } from "@/layouts";
import {
  DEFAULT_DEVICE_PREFS,
  LIST_WIDTH_MAX,
  LIST_WIDTH_MIN,
  ORDER_LABELS,
  inheritedList,
  overrideTarget,
  resolveList,
  followsOrder,
  resolveOrder,
  updateDevicePrefs,
  useDevicePrefs,
  type OrderPref,
} from "@/lib/devicePrefs";
import { READING_LENGTH_LABELS } from "@/lib/readingLength";
import { announce } from "@/shell/toasts";
import { ResizeHandle } from "@/ui/ResizeHandle";
import { NARROW_QUERY, useMedia, useShort, useWide } from "@/lib/useMedia";
import { ARTICLE_MIN, maxFor, useWidth } from "@/lib/useWidth";
import { listTo, scopeFromList, scopeFromSearch } from "@/lib/routes";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { NOTHING_TO_UNDO, undoLast, undoStore } from "@/lib/undo";
import { Button, buttonVariants } from "@/ui/button";
import { inputCls } from "@/ui/kit";
import { cn } from "@/lib/cn";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { ArticlePane } from "./ArticlePane";
import { LayoutMenu } from "./LayoutMenu";
import { LengthMenu } from "./LengthMenu";
import { ReadingMenu } from "./AppearanceControls";
import { FINISH_SEARCH, ListPane, StatusBlock, type ListControls } from "./ListPane";
import { DropdownMenu } from "@/ui/menu";

const VIEWS: { view: View; label: string }[] = [
  { view: "unread", label: "Unread" },
  { view: "all", label: "All" },
  { view: "starred", label: "Starred" },
];

/** The list's title, and for a subfolder the path of the folders above it ("Tech › Apple" above "Mac"). */
function useScopeTitle(scope: Scope): { title: string; above: string } {
  const boot = useBootstrap();
  const tree = useFolderTree();
  if (scope.feed) return { title: boot.data?.feeds.find((f) => f.id === scope.feed)?.title ?? "Feed", above: "" };
  if (scope.folder) return { title: tree.byId.get(scope.folder)?.name ?? "Folder", above: tree.byId.has(scope.folder) ? parentPath(tree, scope.folder) : "" };
  return { title: scope.view === "all" ? "All articles" : scope.view === "starred" ? "Starred" : scope.view === "muted" ? "Muted" : "Unread", above: "" };
}

/** Feeds (or folders, on a folder list) in sidebar order, and the neighbours of the current one. */
function useNeighbours(scope: Scope): { prev?: Scope; next?: Scope } {
  const boot = useBootstrap();
  return useMemo(() => {
    const d = boot.data;
    if (!d) return {};
    const kind = scope.feed ? "feed" : scope.folder ? "folder" : null;
    if (!kind) return {};
    // The sidebar's order: its feeds, and only the folders it shows (those with a feed somewhere inside).
    const feeds = visibleFeeds(d.feeds);
    const tree = folderTree(d.folders);
    const own = new Map<string, string[]>();
    for (const f of feeds) {
      const list = own.get(f.folder_id);
      if (list) list.push(f.id);
      else own.set(f.folder_id, [f.id]);
    }
    const feedsOf = (id: string) => own.get(id) ?? [];
    const inside = kind === "folder" ? subtreeFeeds(tree, feedsOf) : null;
    const order = inside ? tree.preorder.filter((id) => (inside.get(id)?.length ?? 0) > 0) : feedOrder(tree, feedsOf);
    const at = order.indexOf((kind === "feed" ? scope.feed : scope.folder) as string);
    // The neighbour keeps this list's view and reading-time filter.
    const to = (id: string | undefined): Scope | undefined => (id ? { view: scope.view, [kind]: id, ...(scope.length ? { length: scope.length } : {}) } : undefined);
    return at < 0 ? {} : { prev: to(order[at - 1]), next: to(order[at + 1]) };
  }, [boot.data, scope.feed, scope.folder, scope.view, scope.length]);
}

/**
 * Unread articles in a list, from the live counts: the whole library, a feed or a folder. Null where the counts do not
 * say (a search, a reading-time filter, Starred or Muted), so Mark all as read stays on there.
 */
export function scopeUnread(scope: Scope, boot: Bootstrap | undefined): number | null {
  if (!boot || scope.q || scope.length || scope.view === "starred" || scope.view === "muted") return null;
  if (scope.feed) return boot.feeds.find((f) => f.id === scope.feed)?.unread ?? null;
  if (scope.folder) return boot.folders.find((f) => f.id === scope.folder)?.unread ?? null;
  return boot.counts.unread;
}

/** Why Mark all as read is off in a list with nothing unread. */
export const NOTHING_UNREAD = "Nothing unread here";

const menuItem =
  "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[disabled]:opacity-50 data-[highlighted]:bg-selection";

export function ScopeHeader({ scope, controls, beside }: { scope: Scope; controls?: ListControls; beside?: boolean }) {
  const { title, above } = useScopeTitle(scope);
  // Beside an open article (wide) the article's title is the page's h1, so the list's name is the h2 above it.
  const Heading = beside ? "h2" : "h1";
  const refresh = useRefreshAll();
  const refreshing = useRefreshing();
  const dp = useDevicePrefs();
  const navigate = useNavigate();
  const { canUndo } = useStore(undoStore);
  const wide = useWide();
  const { prev, next } = useNeighbours(scope);
  const { layout, ctx } = useResolvedLayout(scope);
  const setListOverride = useSetListOverride();
  const oldest = scope.order === "oldest";
  // The order is the list's resolved one (readerScope). On a feed or folder list the toggle sets that list's own order,
  // and dropping back to what it would inherit clears its override; elsewhere it sets the device default.
  const toggleOrder = () => {
    const order: OrderPref = oldest ? "newest" : "oldest";
    const target = overrideTarget(ctx);
    if (!target) updateDevicePrefs({ order });
    else setListOverride(target.kind, target.id, "order", order === inheritedList(dp, ctx, "order").value ? null : order);
    announce(`${ORDER_LABELS[order]}${target ? ` in this ${target.kind}` : ""}`);
  };
  const go = (s: Scope | undefined) => s && navigate(listTo(s));
  const noun = scope.feed ? "feed" : "folder";
  const boot = useBootstrap();
  const mutedCount = boot.data?.counts.muted ?? 0;
  // The Muted pill appears when there is something muted, or when you are in it.
  const views = scope.view === "muted" || mutedCount > 0 ? [...VIEWS, { view: "muted" as View, label: "Muted" }] : VIEWS;
  const unreadHere = scopeUnread(scope, boot.data);
  const nothingUnread = unreadHere === 0;
  const markAllText = markAllLabel(scope, unreadHere);
  const markAll = useMarkAllConfirm();
  const refreshBusy = refreshing || refresh.isPending;
  // A short phone screen (landscape) has no height to spare for a second header row: the Show pills and the feed
  // switch sit on the title row, after the title (#420).
  const short = useShort();
  const compact = !wide && short;
  // Where even that is too wide for the title (a feed list on a 568 px screen has the title, four pills, the feed switch
  // and up to five controls), the pills are one select: the title truncates before anything is dropped.
  const narrow = useMedia(NARROW_QUERY);
  const asSelect = compact && narrow;
  const pills = asSelect ? (
    <select
      aria-label="Show"
      value={scope.view}
      onChange={(e) => navigate(listTo({ ...scope, view: e.target.value as View }), { replace: true })}
      className={cn(inputCls, "min-h-11 w-auto shrink-0 py-0 text-base font-medium")}
    >
      {views.map((v) => (
        <option key={v.view} value={v.view}>
          {v.label}
        </option>
      ))}
    </select>
  ) : (
    <nav aria-label="Show" className={cn("flex gap-1", compact ? "shrink-0" : "min-w-0 flex-wrap")}>
      {views.map((v) => {
        const active = v.view === scope.view;
        return (
          <Link
            key={v.view}
            to={listTo({ ...scope, view: v.view })}
            replace
            aria-current={active ? "page" : undefined}
            className={cn(
              "inline-flex min-h-11 items-center rounded-full text-sm font-medium",
              compact ? "px-3" : "px-4",
              active ? "bg-accent text-bg" : "text-fg hover:bg-selection",
            )}
          >
            {v.label}
          </Link>
        );
      })}
    </nav>
  );
  const switcher =
    prev || next ? (
      <div className={cn("flex", compact ? "shrink-0" : "ml-auto")} role="group" aria-label={`Switch ${noun}`}>
        <Button variant="ghost" size="icon" aria-label={`Previous ${noun}`} title={`Previous ${noun}`} disabled={!prev} onClick={() => go(prev)}>
          <ChevronLeft aria-hidden="true" />
        </Button>
        <Button variant="ghost" size="icon" aria-label={`Next ${noun}`} title={`Next ${noun}`} disabled={!next} onClick={() => go(next)}>
          <ChevronRight aria-hidden="true" />
        </Button>
      </div>
    ) : null;
  return (
    <header className={cn("pt-safe shrink-0 border-b border-line bg-bg pl-[max(1rem,env(safe-area-inset-left))] pr-[max(1rem,env(safe-area-inset-right))]", compact ? "pb-1" : "pb-2")}>
      {/* The title row is 25rem wide and left-aligned, so the controls sit in the same place in every layout (a grid
          layout has no reader pane, and used to push them to the far right). The title keeps at least 6.5rem and truncates
          beyond that, so at normal text size on a phone the header is one row in every list; only where the pane is
          too narrow for 6.5rem and the controls together (a narrow list pane, a large text size) do the controls drop to
          a line of their own. */}
      <div className={cn("flex max-w-full flex-wrap items-center gap-x-0.5", compact ? "w-full pt-1" : "w-fit min-w-[min(100%,25rem)] pt-2")}>
        <Heading
          className={cn("truncate text-xl font-bold", asSelect ? "mr-2 min-w-[3rem] flex-1" : compact ? "mr-2 min-w-[6.5rem] max-w-[40%]" : "min-w-[6.5rem] max-w-full flex-1")}
          tabIndex={-1}
          data-route-heading
          aria-label={above ? `${above}${PATH_SEP}${title}` : undefined}
        >
          {above ? <span className="block truncate text-xs font-normal text-fg2">{above}</span> : null}
          {title}
        </Heading>
        {compact ? (
          <>
            {pills}
            {switcher}
          </>
        ) : null}
        <div className="ml-auto flex shrink-0 items-center gap-0.5">
        {/* A layout that is always newest first has no order to toggle (the list options menu says why). On a phone
            the order lives only in the Layout menu, which leaves the title room in a one-row header. */}
        {wide && followsOrder(layout.id) ? (
          <Button
            variant="ghost"
            size="icon"
            aria-label="Oldest first"
            aria-pressed={oldest}
            onClick={toggleOrder}
          >
            {oldest ? <ArrowUpNarrowWide aria-hidden="true" /> : <ArrowDownWideNarrow aria-hidden="true" />}
          </Button>
        ) : null}
        <LengthMenu scope={scope} />
        <LayoutMenu scope={scope} />
        <ReadingMenu />
        {/* On a phone Refresh is a pull on the list and an item in the menu after this one, which leaves four controls
            at most: the header is no wider than the title and what the list is. */}
        {wide ? (
          <Button
            variant="ghost"
            size="icon"
            aria-label={refreshing ? "Refreshing" : "Refresh all feeds"}
            onClick={() => refresh.mutate()}
            aria-disabled={refresh.isPending || undefined}
          >
            <RefreshCw aria-hidden="true" className={cn(refreshing && "animate-spin")} />
          </Button>
        ) : null}
        {wide ? (
          <Link to="/settings" aria-label="Settings" title="Settings" className={buttonVariants({ variant: "ghost", size: "icon" })}>
            <Settings aria-hidden="true" />
          </Link>
        ) : null}
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <Button variant="ghost" size="icon" aria-label="List actions">
              {/* With no Refresh button here, the trigger shows that a refresh is running. */}
              {refreshing && !wide ? <RefreshCw aria-hidden="true" className="animate-spin" /> : <MoreIcon aria-hidden="true" />}
            </Button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-56">
              {wide ? null : (
                // aria-disabled, not disabled: while a refresh runs the item stays in the arrow-key order and is read out as
                // "Refreshing, dimmed"; choosing it does nothing.
                <DropdownMenu.Item
                  className={menuItem}
                  aria-disabled={refreshBusy || undefined}
                  data-disabled={refreshBusy ? "" : undefined}
                  onSelect={(e) => (refreshBusy ? e.preventDefault() : refresh.mutate())}
                >
                  <RefreshCw className={cn("size-5", refreshing && "animate-spin")} aria-hidden="true" />
                  {refreshing ? "Refreshing" : "Refresh all feeds"}
                </DropdownMenu.Item>
              )}
              <DropdownMenu.Item
                className={menuItem}
                disabled={!controls || scope.view === "muted" || !!scope.typing || nothingUnread}
                onSelect={() => markAll.ask(markAllText, markAllReach(scope, scope.feed || scope.folder ? title : undefined), () => controls?.markAllRead())}
              >
                <MarkAllIcon className="size-5" aria-hidden="true" />
                <span className="flex flex-col">
                  {markAllText}
                  {scope.typing ? <span className="text-xs text-fg2">{FINISH_SEARCH}</span> : nothingUnread ? <span className="text-xs text-fg2">{NOTHING_UNREAD}</span> : null}
                </span>
              </DropdownMenu.Item>
              {/* Off says why: the undo history is this page's (a reload starts it empty, docs/design.md 7.11). */}
              <DropdownMenu.Item className={menuItem} disabled={!canUndo} onSelect={() => void undoLast()}>
                <Undo2 className="size-5" aria-hidden="true" />
                <span className="flex flex-col">
                  Undo
                  {canUndo ? null : <span className="text-xs text-fg2">{NOTHING_TO_UNDO}</span>}
                </span>
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
        </div>
      </div>
      {compact ? null : (
        <div className="mt-1 flex max-w-[25rem] items-center gap-1">
          {pills}
          {switcher}
        </div>
      )}
      {scope.length ? (
        <div className="mt-1 flex max-w-[25rem]">
          <Link
            to={listTo({ ...scope, length: undefined })}
            replace
            aria-label={`Reading time ${READING_LENGTH_LABELS[scope.length]}. Show any length`}
            className="inline-flex min-h-11 items-center gap-1.5 rounded-full border border-accent px-3 text-sm text-fg hover:bg-selection"
          >
            <Timer className="size-4 text-accent" aria-hidden="true" />
            {READING_LENGTH_LABELS[scope.length]}
            <X className="size-4 text-fg2" aria-hidden="true" />
          </Link>
        </div>
      ) : null}
      {scope.view === "muted" ? (
        <p className="mt-1 max-w-[25rem] text-xs text-fg2">Articles your filters muted. Restore brings one back as it was before it was muted. Muting keeps them here instead of deleting them.</p>
      ) : null}
      {markAll.dialog}
    </header>
  );
}

function ReaderLayout({ scope, articleId, hasFrom }: { scope: Scope; articleId?: string; hasFrom: boolean }) {
  const wide = useWide();
  // With an article open the article names the page (it sets its own title).
  const scopeTitle = useScopeTitle(scope).title;
  useDocumentTitle(articleId ? undefined : scopeTitle);
  const { layout } = useResolvedLayout(scope);
  const dp = useDevicePrefs();
  // On a wide screen an open article always has its list beside it, whatever the layout: switching layouts with an
  // article open keeps both on screen (a grid layout such as Cards becomes a single column in the pane). A grid
  // list or a page layout (the Gazette) with nothing open is the one case that is not a pane: it fills the width.
  const fills = !!layout.grid || !!pageOf(layout, scope);
  const listOnly = wide && fills && !articleId;
  const listKey = scopeKey(scope);
  // An article opened from a search draws the words of that search (on a phone no list is mounted to do it).
  const qc = useQueryClient();
  // Reactive (the list's page may load after this renders) and remembered: once the list's cache entry is collected
  // (a phone shows no list) the last known answer stays, so the words drawn do not change under the reader.
  const cachedFallback = useSyncExternalStore(
    (cb) => qc.getQueryCache().subscribe(cb),
    () => (scope.q ? (qc.getQueryData<InfiniteData<ItemsPage>>(keys.items(scope))?.pages[0]?.fallback ?? null) : null),
  );
  const [kept, setKept] = useState<{ key: string; v: boolean } | null>(null);
  if (cachedFallback !== null && (kept?.key !== listKey || kept.v !== cachedFallback)) setKept({ key: listKey, v: cachedFallback });
  const fallback = scope.q ? (cachedFallback ?? (kept?.key === listKey ? kept.v : false)) : false;
  useSearchHighlight(scope.q, { fallback, typing: scope.typing });
  // The header belongs to the list column, not to one list: another order, view, length or feed remounts the list
  // (keyed by its scope) but not the header, so the control that was pressed keeps focus. The list reports what Mark
  // all acts on, and null when it goes.
  const [controls, setControls] = useState<ListControls | null>(null);
  const list = (
    <div className="flex h-full min-h-0 flex-col">
      <ScopeHeader scope={scope} controls={controls ?? undefined} beside={wide && !!articleId} />
      <div className="min-h-0 flex-1">
        <ListPane key={listKey} scope={scope} activeId={articleId} onControls={setControls} />
      </div>
    </div>
  );

  if (!wide) return articleId ? <ArticlePane key={articleId} id={articleId} scope={scope} hasFrom={hasFrom} pane={false} /> : list;

  // The list column's width is the device's choice (drag the handle or use the arrow keys); until then the
  // layout's own width. It never takes more than leaves the article ARTICLE_MIN px, and the handle reports the
  // width that is really on screen.
  return (
    <WidePane
      listOnly={listOnly}
      layoutRem={layout.paneRem}
      listWidth={dp.listWidth}
      list={list}
      article={
        articleId ? (
          <ArticlePane id={articleId} scope={scope} hasFrom={hasFrom} pane />
        ) : (
          <div className="flex h-full items-center justify-center p-6 text-center text-fg2">
            <p>Select an article to read it here.</p>
          </div>
        )
      }
    />
  );
}

function WidePane({
  listOnly,
  layoutRem,
  listWidth,
  list,
  article,
}: {
  listOnly: boolean | undefined;
  layoutRem: number;
  listWidth: number | null;
  list: React.ReactNode;
  article: React.ReactNode;
}) {
  const box = useRef<HTMLDivElement>(null);
  const col = useRef<HTMLDivElement>(null);
  const total = useWidth(box);
  // The root font size only changes with the text-size preference, so it is read then, not on every render.
  const textSize = useStore(prefsStore).textSize;
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const remPx = useMemo(() => (typeof document === "undefined" ? 16 : parseFloat(getComputedStyle(document.documentElement).fontSize) || 16), [textSize]);
  const limit = maxFor(total, ARTICLE_MIN, LIST_WIDTH_MIN, LIST_WIDTH_MAX);
  const wanted = listWidth ?? Math.round(layoutRem * remPx);
  const shown = Math.min(limit, Math.max(LIST_WIDTH_MIN, wanted));
  return (
    <div ref={box} className="flex h-full min-h-0">
      {/* The list is always the first child here, so it is never remounted when an article opens or closes. */}
      <div ref={col} className={listOnly ? "relative min-w-0 flex-1" : "relative shrink-0 border-r border-line"} style={listOnly ? undefined : { width: shown }}>
        {list}
        {listOnly ? null : (
          <ResizeHandle
            label="Resize article list"
            value={shown}
            min={LIST_WIDTH_MIN}
            max={limit}
            // The column follows the pointer directly; the device preference is written once, on release.
            onPreview={(px) => {
              if (col.current) col.current.style.width = `${px ?? shown}px`;
            }}
            onChange={(listWidth) => updateDevicePrefs({ listWidth })}
            onReset={() => updateDevicePrefs({ listWidth: DEFAULT_DEVICE_PREFS.listWidth })}
          />
        )}
      </div>
      {listOnly ? null : <div className="min-w-0 flex-1">{article}</div>}
    </div>
  );
}

/** The list of the reader screen before its order: `/l/:view?feed=&folder=&len=`, or an article's list from `?from=`. */
function baseScope(view: string | undefined, sp: URLSearchParams, isArticle: boolean): Scope {
  return isArticle ? scopeFromSearch(sp) : scopeFromList(view, sp);
}

/**
 * The scope of the reader screen, one builder for the list route and the article route. The sort order is always the
 * list's resolved order (its own, a folder's above it, else the device's; a `from` written under the other order does
 * not stick). A search keeps the ordering it was run with (relevance, newest, oldest) and its typing flag: its list in
 * the cache is keyed by them.
 */
export function readerScope(base: Scope, order: OrderPref): Scope {
  if (base.q) return base;
  const { order: _drop, ...rest } = base;
  void _drop;
  return order === "oldest" ? { ...rest, order: "oldest" } : rest;
}

/**
 * One persistent layout for `/l/:view` and `/i/:id`: opening or closing an article never remounts the
 * list, so hidden rows, ticks, scroll and focus survive on a wide screen.
 */
export function ReaderRoute() {
  const item = useMatch("/i/:id");
  const list = useMatch("/l/:view");
  const [sp] = useSearchParams();
  const dp = useDevicePrefs();
  const isArticle = !!item;
  const view = list?.params.view;
  const spKey = sp.toString();
  const base = useMemo(() => baseScope(view, new URLSearchParams(spKey), isArticle), [view, spKey, isArticle]);
  const order = resolveOrder(dp, useListContext(base));
  const scope = useMemo(() => readerScope(base, order), [base, order]);
  const boot = useBootstrap();
  const tree = useFolderTree();
  // A feed or folder that is not in the bootstrap on hand may only be new (added from another client or tab), so it is
  // declared gone only after the bootstrap was fetched again for it. `checked` is the target that fetch settled for.
  const missing = !isArticle && boot.data ? (scope.feed && !visibleFeeds(boot.data.feeds).some((f) => f.id === scope.feed) ? { kind: "feed", id: scope.feed } : scope.folder && !tree.byId.has(scope.folder) ? { kind: "folder", id: scope.folder } : null) : null;
  const missingKey = missing ? `${missing.kind}:${missing.id}` : null;
  const [checked, setChecked] = useState<{ key: string; round: number } | null>(null);
  const [round, setRound] = useState(0);
  const refetchBoot = boot.refetch;
  useEffect(() => {
    if (!missingKey) return;
    let live = true;
    void refetchBoot().finally(() => live && setChecked({ key: missingKey, round }));
    return () => {
      live = false;
    };
  }, [missingKey, round, refetchBoot]);
  // A list whose view is not one of ours is not a list: go to Unread (and fix the address) rather than show Unread
  // under a made-up one.
  if (view !== undefined && !VIEWS.some((v) => v.view === view) && view !== "muted") return <Navigate to={listTo({ view: "unread" })} replace />;
  if (missing && missingKey) {
    if (checked?.key !== missingKey || checked.round !== round) return <StatusBlock role="status" title="Checking" body={`Looking for this ${missing.kind}.`} />;
    // A bootstrap that could not be fetched again proves nothing about the feed.
    const unsure = boot.isRefetchError;
    return (
      <StatusBlock
        role="status"
        title={unsure ? `Couldn't check this ${missing.kind}` : `This ${missing.kind} no longer exists`}
        body={unsure ? "Kipple couldn't reach the server to look for it." : "It may have been deleted, or the address is out of date."}
      >
        <div className="flex flex-wrap items-center justify-center gap-2">
          <Link to={listTo({ view: "unread" })} replace className={buttonVariants({})}>
            Go to Unread
          </Link>
          <Button variant="ghost" onClick={() => setRound((n) => n + 1)}>
            Check again
          </Button>
        </div>
      </StatusBlock>
    );
  }
  return <ReaderLayout scope={scope} articleId={item?.params.id} hasFrom={sp.has("from")} />;
}

/**
 * `/l?feed=` or `/l?folder=` (lib/routes openListTo): the list opens in its own view (the feed's, the nearest folder's
 * above it, else Unread). The address is replaced by the list's own, so back, reload and the view pills see a plain list.
 */
export function OpenList() {
  const [sp] = useSearchParams();
  const dp = useDevicePrefs();
  const target = { feed: sp.get("feed") ?? undefined, folder: sp.get("feed") ? undefined : (sp.get("folder") ?? undefined) };
  const ctx = useListContext(target);
  // The folder chain comes from the bootstrap; the app shell shows no route until it has bootstrap data, so it is here.
  return <Navigate to={listTo({ view: resolveList(dp, ctx, "view").value, ...target })} replace />;
}
