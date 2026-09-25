import { useMemo } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { DropdownMenu } from "radix-ui";
import { ArrowDownWideNarrow, ArrowUpNarrowWide, CheckCheck, ChevronLeft, ChevronRight, Keyboard, MoreVertical, RefreshCw, Undo2 } from "lucide-react";
import { scopeKey, useBootstrap } from "@/api/queries";
import { useRefreshAll, useRefreshing } from "@/api/refresh";
import type { Card, Scope, View } from "@/api/types";
import { useResolvedLayout } from "@/layouts";
import { updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { useWide } from "@/lib/useMedia";
import { articleTo, listTo, scopeFromList, scopeFromSearch } from "@/lib/routes";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { undoLast, undoStore } from "@/lib/undo";
import { openHelp } from "@/shell/HelpDialog";
import { Button } from "@/ui/button";
import { cn } from "@/lib/cn";
import { ArticlePane } from "./ArticlePane";
import { LayoutMenu } from "./LayoutMenu";
import { ListPane, type ListControls } from "./ListPane";

const VIEWS: { view: View; label: string }[] = [
  { view: "unread", label: "Unread" },
  { view: "all", label: "All" },
  { view: "starred", label: "Starred" },
];

function useScopeTitle(scope: Scope): string {
  const boot = useBootstrap();
  if (scope.feed) return boot.data?.feeds.find((f) => f.id === scope.feed)?.title ?? "Feed";
  if (scope.folder) return boot.data?.folders.find((f) => f.id === scope.folder)?.name ?? "Folder";
  return scope.view === "all" ? "All articles" : scope.view === "starred" ? "Starred" : "Unread";
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
  const { prev, next } = useNeighbours(scope);
  const oldest = dp.order === "oldest";
  // Feed and folder scopes do not carry the order; it is a device preference.
  const go = (s: Scope | undefined) => s && navigate(listTo(s));
  useHotkeys(
    { refresh: () => refresh.mutate(), prevFeed: () => go(prev), nextFeed: () => go(next) },
    { singleKeys: prefs.shortcuts },
  );
  return (
    <header className="pt-safe shrink-0 border-b border-line bg-bg px-4 pb-2">
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
        <Button
          variant="ghost"
          size="icon"
          aria-label={refreshing ? "Refreshing" : "Refresh all feeds"}
          onClick={() => refresh.mutate()}
          disabled={refresh.isPending}
        >
          <RefreshCw aria-hidden="true" className={cn(refreshing && "animate-spin")} />
        </Button>
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <Button variant="ghost" size="icon" aria-label="List actions">
              <MoreVertical aria-hidden="true" />
            </Button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-56 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
              <DropdownMenu.Item className={menuItem} disabled={!controls} onSelect={() => controls?.markAllRead()}>
                <CheckCheck className="size-5" aria-hidden="true" />
                Mark all as read
              </DropdownMenu.Item>
              <DropdownMenu.Item className={menuItem} disabled={!canUndo} onSelect={() => void undoLast()}>
                <Undo2 className="size-5" aria-hidden="true" />
                Undo
              </DropdownMenu.Item>
              {prev || next ? <DropdownMenu.Separator className="my-1 h-px bg-line" /> : null}
              {prev ? (
                <DropdownMenu.Item className={menuItem} onSelect={() => go(prev)}>
                  <ChevronLeft className="size-5" aria-hidden="true" />
                  Previous {scope.feed ? "feed" : "folder"}
                </DropdownMenu.Item>
              ) : null}
              {next ? (
                <DropdownMenu.Item className={menuItem} onSelect={() => go(next)}>
                  <ChevronRight className="size-5" aria-hidden="true" />
                  Next {scope.feed ? "feed" : "folder"}
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
      <nav aria-label="Show" className="mt-1 flex gap-1">
        {VIEWS.map((v) => {
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
    </header>
  );
}

function ReaderLayout({ scope, articleId }: { scope: Scope; articleId?: string }) {
  const wide = useWide();
  const navigate = useNavigate();
  const { layout } = useResolvedLayout(scope);
  // Cards is a grid: no reader pane, the article opens full width.
  const paneMode = wide && !layout.grid;
  const listKey = scopeKey(scope);
  const onKeyMove = useMemo(
    () =>
      paneMode
        ? (item: Card) => navigate(articleTo(item.id, scope), { replace: !!articleId, state: { via: "key" } })
        : undefined,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [paneMode, articleId, listKey, navigate],
  );

  const list = (
    <ListPane
      key={listKey}
      scope={scope}
      activeId={articleId}
      header={(controls) => <ScopeHeader scope={scope} controls={controls} />}
      onKeyMove={onKeyMove}
      keysEnabled={!articleId || !paneMode}
    />
  );

  if (!paneMode) return articleId ? <ArticlePane key={articleId} id={articleId} pane={false} /> : list;

  return (
    <div className="flex h-full min-h-0">
      <div className="shrink-0 border-r border-line" style={{ width: `${layout.paneRem}rem` }}>
        {list}
      </div>
      <div className="min-w-0 flex-1">
        {articleId ? (
          <ArticlePane id={articleId} pane />
        ) : (
          <div className="flex h-full items-center justify-center p-6 text-center text-fg2">
            <p>Select an article to read it here.</p>
          </div>
        )}
      </div>
    </div>
  );
}

export function ListRoute() {
  const { view } = useParams();
  const [sp] = useSearchParams();
  const { order } = useDevicePrefs();
  const scope = useMemo(() => {
    const s = scopeFromList(view, sp);
    return order === "oldest" ? { ...s, order: "oldest" as const } : s;
  }, [view, sp, order]);
  return <ReaderLayout scope={scope} />;
}

export function ItemRoute() {
  const { id } = useParams();
  const [sp] = useSearchParams();
  const scope = useMemo(() => scopeFromSearch(sp), [sp]);
  return <ReaderLayout scope={scope} articleId={id} />;
}
