import { useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { Link, Navigate, useMatch, useNavigate, useSearchParams } from "react-router";
import { DropdownMenu } from "radix-ui";
import { ArrowDownWideNarrow, ArrowUpNarrowWide, CheckCheck, ChevronLeft, ChevronRight, Keyboard, MoreVertical, RefreshCw, Settings, Timer, Undo2, X } from "lucide-react";
import { keys, scopeKey, useBootstrap, useFolderTree } from "@/api/queries";
import { PATH_SEP, feedOrder, folderTree, parentPath, subtreeFeeds } from "@/lib/folderTree";
import { useRefreshAll, useRefreshing } from "@/api/refresh";
import type { Card, ItemsPage, Scope, View } from "@/api/types";
import { useSearchHighlight } from "@/lib/useHighlights";
import { useListContext, useResolvedLayout } from "@/layouts";
import {
  DEFAULT_DEVICE_PREFS,
  LIST_WIDTH_MAX,
  LIST_WIDTH_MIN,
  ORDER_LABELS,
  inheritedList,
  overrideTarget,
  resolveList,
  setListOverride,
  updateDevicePrefs,
  useDevicePrefs,
  type OrderPref,
} from "@/lib/devicePrefs";
import { READING_LENGTH_LABELS } from "@/lib/readingLength";
import { announce } from "@/shell/toasts";
import { ResizeHandle } from "@/ui/ResizeHandle";
import { useWide } from "@/lib/useMedia";
import { ARTICLE_MIN, maxFor, useWidth } from "@/lib/useWidth";
import { articleTo, listTo, scopeFromList, scopeFromSearch } from "@/lib/routes";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { undoLast, undoStore } from "@/lib/undo";
import { openHelp } from "@/shell/HelpDialog";
import { Button, buttonVariants } from "@/ui/button";
import { cn } from "@/lib/cn";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { ArticlePane } from "./ArticlePane";
import { LayoutMenu } from "./LayoutMenu";
import { LengthMenu } from "./LengthMenu";
import { ReadingMenu } from "./AppearanceControls";
import { FINISH_SEARCH, ListPane, type ListControls } from "./ListPane";

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

const menuItem =
  "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[disabled]:opacity-50 data-[highlighted]:bg-selection";

export function ScopeHeader({ scope, controls }: { scope: Scope; controls?: ListControls }) {
  const { title, above } = useScopeTitle(scope);
  const refresh = useRefreshAll();
  const refreshing = useRefreshing();
  const prefs = useStore(prefsStore);
  const dp = useDevicePrefs();
  const navigate = useNavigate();
  const { canUndo } = useStore(undoStore);
  const wide = useWide();
  const { prev, next } = useNeighbours(scope);
  const ctx = useListContext(scope);
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
  const hint = (k: string) => (prefs.shortcuts ? <kbd className="ml-auto rounded border border-line px-1.5 font-mono text-xs text-fg2">{k}</kbd> : null);
  // `r` (refresh) is bound once for every screen, in the app shell.
  useHotkeys({ prevFeed: () => go(prev), nextFeed: () => go(next) }, { singleKeys: prefs.shortcuts });
  return (
    <header className="pt-safe shrink-0 border-b border-line bg-bg px-4 pb-2">
      {/* The title row is 25rem wide and left-aligned, so the controls sit in the same place in every layout (a grid
          layout has no reader pane, and used to push them to the far right). It grows past that only as far as a long
          title needs, and where the pane is too narrow for title and controls together the controls drop to a line
          of their own rather than squeeze the title to an ellipsis. */}
      <div className="flex w-fit min-w-[min(100%,25rem)] max-w-full flex-wrap items-center gap-x-0.5 pt-2">
        <h1 className="min-w-0 max-w-full grow truncate text-xl font-bold" tabIndex={-1} data-route-heading aria-label={above ? `${above}${PATH_SEP}${title}` : undefined}>
          {above ? <span className="block truncate text-xs font-normal text-fg2">{above}</span> : null}
          {title}
        </h1>
        <div className="ml-auto flex items-center gap-0.5">
        <Button
          variant="ghost"
          size="icon"
          aria-label="Oldest first"
          aria-pressed={oldest}
          onClick={toggleOrder}
        >
          {oldest ? <ArrowUpNarrowWide aria-hidden="true" /> : <ArrowDownWideNarrow aria-hidden="true" />}
        </Button>
        <LengthMenu scope={scope} />
        <LayoutMenu scope={scope} />
        <ReadingMenu />
        <Button
          variant="ghost"
          size="icon"
          aria-label={refreshing ? "Refreshing" : "Refresh all feeds"}
          onClick={() => refresh.mutate()}
          disabled={refresh.isPending}
        >
          <RefreshCw aria-hidden="true" className={cn(refreshing && "animate-spin")} />
        </Button>
        {wide ? (
          <Link to="/settings" aria-label="Settings" title="Settings" className={buttonVariants({ variant: "ghost", size: "icon" })}>
            <Settings aria-hidden="true" />
          </Link>
        ) : null}
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <Button variant="ghost" size="icon" aria-label="List actions">
              <MoreVertical aria-hidden="true" />
            </Button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-56 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
              <DropdownMenu.Item className={menuItem} disabled={!controls || scope.view === "muted" || !!scope.typing} onSelect={() => controls?.markAllRead()}>
                <CheckCheck className="size-5" aria-hidden="true" />
                <span className="flex flex-col">
                  Mark all as read
                  {scope.typing ? <span className="text-xs text-fg2">{FINISH_SEARCH}</span> : null}
                </span>
              </DropdownMenu.Item>
              <DropdownMenu.Item className={menuItem} disabled={!canUndo} onSelect={() => void undoLast()}>
                <Undo2 className="size-5" aria-hidden="true" />
                Undo
              </DropdownMenu.Item>
              {prev || next ? <DropdownMenu.Separator className="my-1 h-px bg-line" /> : null}
              {prev ? (
                <DropdownMenu.Item className={menuItem} onSelect={() => go(prev)}>
                  <ChevronLeft className="size-5" aria-hidden="true" />
                  Previous {noun}
                  {hint("[")}
                </DropdownMenu.Item>
              ) : null}
              {next ? (
                <DropdownMenu.Item className={menuItem} onSelect={() => go(next)}>
                  <ChevronRight className="size-5" aria-hidden="true" />
                  Next {noun}
                  {hint("]")}
                </DropdownMenu.Item>
              ) : null}
              <DropdownMenu.Separator className="my-1 h-px bg-line" />
              <DropdownMenu.Item className={menuItem} onSelect={openHelp}>
                <Keyboard className="size-5" aria-hidden="true" />
                Keyboard shortcuts
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
        </div>
      </div>
      <div className="mt-1 flex max-w-[25rem] items-center gap-1">
      <nav aria-label="Show" className="flex gap-1">
        {views.map((v) => {
          const active = v.view === scope.view;
          return (
            <Link
              key={v.view}
              to={listTo({ ...scope, view: v.view })}
              replace
              aria-current={active ? "page" : undefined}
              className={cn(
                "inline-flex min-h-11 items-center rounded-full px-4 text-sm font-medium",
                active ? "bg-accent text-bg" : "text-fg hover:bg-selection",
              )}
            >
              {v.label}
            </Link>
          );
        })}
      </nav>
        {prev || next ? (
          <div className="ml-auto flex" role="group" aria-label={`Switch ${noun}`}>
            <Button
              variant="ghost"
              size="icon"
              aria-label={`Previous ${noun}`}
              aria-keyshortcuts="["
              title={`Previous ${noun}${prefs.shortcuts ? " ([)" : ""}`}
              disabled={!prev}
              onClick={() => go(prev)}
            >
              <ChevronLeft aria-hidden="true" />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              aria-label={`Next ${noun}`}
              aria-keyshortcuts="]"
              title={`Next ${noun}${prefs.shortcuts ? " (])" : ""}`}
              disabled={!next}
              onClick={() => go(next)}
            >
              <ChevronRight aria-hidden="true" />
            </Button>
          </div>
        ) : null}
      </div>
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
        <p className="mt-1 max-w-[25rem] text-xs text-fg2">Articles your filters muted. Restore brings one back as unread. Muting keeps them here instead of deleting them.</p>
      ) : null}
    </header>
  );
}

function ReaderLayout({ scope, articleId, hasFrom }: { scope: Scope; articleId?: string; hasFrom: boolean }) {
  const wide = useWide();
  const navigate = useNavigate();
  const { layout } = useResolvedLayout(scope);
  const dp = useDevicePrefs();
  // On a wide screen an open article always has its list beside it, whatever the layout: switching layouts with an
  // article open keeps both on screen (a grid layout such as Cards becomes a single column in the pane). A grid
  // list with nothing open is the one case that is not a pane: it fills the width.
  const paneMode = wide && (!layout.grid || !!articleId);
  const listOnly = wide && layout.grid && !articleId;
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
  const onKeyMove = useMemo(
    () =>
      paneMode
        ? (item: Card) => navigate(articleTo(item.id, scope), { replace: !!articleId, state: { via: "key" } })
        : undefined,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [paneMode, articleId, listKey, navigate],
  );

  // In the reader pane the list stays live beside the article: its keys (mark all, above/below, select,
  // gg/G, Enter, c) keep working, and it drives j/k/m/s/o for the article, which is its selected row.
  const list = (
    <ListPane
      key={listKey}
      scope={scope}
      activeId={articleId}
      articleOpen={paneMode && !!articleId}
      header={(controls) => <ScopeHeader scope={scope} controls={controls} />}
      onKeyMove={onKeyMove}
    />
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
  const order = resolveList(dp, useListContext(base), "order").value;
  const scope = useMemo(() => readerScope(base, order), [base, order]);
  return <ReaderLayout scope={scope} articleId={item?.params.id} hasFrom={sp.has("from")} />;
}

/**
 * `/l?feed=` or `/l?folder=` (lib/routes openListTo): the list opens in its own view (the feed's, the nearest folder's
 * above it, else Unread). The address is replaced by the list's own, so back, reload and the view pills see a plain list.
 */
export function OpenList() {
  const [sp] = useSearchParams();
  const boot = useBootstrap();
  const dp = useDevicePrefs();
  const target = { feed: sp.get("feed") ?? undefined, folder: sp.get("feed") ? undefined : (sp.get("folder") ?? undefined) };
  const ctx = useListContext(target);
  // The folder chain comes from the bootstrap; while it loads the view could resolve differently.
  if (boot.isPending) return null;
  return <Navigate to={listTo({ view: resolveList(dp, ctx, "view").value, ...target })} replace />;
}
