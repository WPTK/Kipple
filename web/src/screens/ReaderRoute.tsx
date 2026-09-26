import { useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { Link, useMatch, useNavigate, useSearchParams } from "react-router";
import { DropdownMenu } from "radix-ui";
import { ArrowDownWideNarrow, ArrowUpNarrowWide, CheckCheck, ChevronLeft, ChevronRight, Keyboard, MoreVertical, RefreshCw, Settings, Undo2 } from "lucide-react";
import { keys, scopeKey, useBootstrap } from "@/api/queries";
import { useRefreshAll, useRefreshing } from "@/api/refresh";
import type { Card, ItemsPage, Scope, View } from "@/api/types";
import { useSearchHighlight } from "@/lib/useHighlights";
import { useResolvedLayout } from "@/layouts";
import { DEFAULT_DEVICE_PREFS, LIST_WIDTH_MAX, LIST_WIDTH_MIN, updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
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
import { ArticlePane } from "./ArticlePane";
import { LayoutMenu } from "./LayoutMenu";
import { ReadingMenu } from "./AppearanceControls";
import { FINISH_SEARCH, ListPane, type ListControls } from "./ListPane";

const VIEWS: { view: View; label: string }[] = [
  { view: "unread", label: "Unread" },
  { view: "all", label: "All" },
  { view: "starred", label: "Starred" },
];

function useScopeTitle(scope: Scope): string {
  const boot = useBootstrap();
  if (scope.feed) return boot.data?.feeds.find((f) => f.id === scope.feed)?.title ?? "Feed";
  if (scope.folder) return boot.data?.folders.find((f) => f.id === scope.folder)?.name ?? "Folder";
  return scope.view === "all" ? "All articles" : scope.view === "starred" ? "Starred" : scope.view === "muted" ? "Muted" : "Unread";
}

/** Feeds (or folders, on a folder list) in sidebar order, and the neighbours of the current one. */
function useNeighbours(scope: Scope): { prev?: Scope; next?: Scope } {
  const boot = useBootstrap();
  return useMemo(() => {
    const d = boot.data;
    if (!d) return {};
    const kind = scope.feed ? "feed" : scope.folder ? "folder" : null;
    if (!kind) return {};
    const order = kind === "feed" ? d.folders.flatMap((fo) => d.feeds.filter((f) => f.folder_id === fo.id).map((f) => f.id)) : d.folders.map((f) => f.id);
    const at = order.indexOf((kind === "feed" ? scope.feed : scope.folder) as string);
    const to = (id: string | undefined): Scope | undefined => (id ? { view: scope.view, [kind]: id } : undefined);
    return at < 0 ? {} : { prev: to(order[at - 1]), next: to(order[at + 1]) };
  }, [boot.data, scope.feed, scope.folder, scope.view]);
}

const menuItem =
  "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[disabled]:opacity-50 data-[highlighted]:bg-selection";

export function ScopeHeader({ scope, controls }: { scope: Scope; controls?: ListControls }) {
  const title = useScopeTitle(scope);
  const refresh = useRefreshAll();
  const refreshing = useRefreshing();
  const prefs = useStore(prefsStore);
  const dp = useDevicePrefs();
  const navigate = useNavigate();
  const { canUndo } = useStore(undoStore);
  const wide = useWide();
  const { prev, next } = useNeighbours(scope);
  const oldest = dp.order === "oldest";
  // Feed and folder scopes do not carry the order; it is a device preference.
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
      {/* Never wider than 25rem and always left-aligned, so the controls sit in the same place in every layout
          (a grid layout has no reader pane, and used to push them to the far right). */}
      <div className="max-w-[25rem]">
      <div className="flex items-center gap-0.5 pt-2">
        <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
          {title}
        </h1>
        <Button
          variant="ghost"
          size="icon"
          aria-label="Oldest first"
          aria-pressed={oldest}
          onClick={() => updateDevicePrefs({ order: oldest ? "newest" : "oldest" })}
        >
          {oldest ? <ArrowUpNarrowWide aria-hidden="true" /> : <ArrowDownWideNarrow aria-hidden="true" />}
        </Button>
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
      <div className="mt-1 flex items-center gap-1">
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
      {scope.view === "muted" ? (
        <p className="mt-1 text-xs text-fg2">Articles your filters muted. Restore brings one back as unread. Muting keeps them here instead of deleting them.</p>
      ) : null}
      </div>
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

/**
 * The scope of the reader screen, one builder for the list route and the article route: the list
 * comes from `/l/:view?feed=&folder=`, an article's list from `?from=`; the sort order is always the
 * device preference (a `from` written under the other order does not stick).
 */
export function readerScope(view: string | undefined, sp: URLSearchParams, isArticle: boolean, order: "newest" | "oldest"): Scope {
  const base = isArticle ? scopeFromSearch(sp) : scopeFromList(view, sp);
  // A search keeps the ordering it was run with (relevance, newest, oldest) and its typing flag: its list in the
  // cache is keyed by them. Every other list follows the device's order.
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
  const { order } = useDevicePrefs();
  const isArticle = !!item;
  const view = list?.params.view;
  const spKey = sp.toString();
  const scope = useMemo(
    () => readerScope(view, new URLSearchParams(spKey), isArticle, order),
    [view, spKey, isArticle, order],
  );
  return <ReaderLayout scope={scope} articleId={item?.params.id} hasFrom={sp.has("from")} />;
}
