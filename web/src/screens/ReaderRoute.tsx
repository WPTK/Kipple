import { useMemo } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { RefreshCw } from "lucide-react";
import { scopeKey, useBootstrap } from "@/api/queries";
import { useRefreshAll, useRefreshing } from "@/api/refresh";
import type { Card, Scope, View } from "@/api/types";
import { useWide } from "@/lib/useMedia";
import { articleTo, listTo, scopeFromList, scopeFromSearch } from "@/lib/routes";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { Button } from "@/ui/button";
import { cn } from "@/lib/cn";
import { ArticlePane } from "./ArticlePane";
import { ListPane } from "./ListPane";

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

export function ScopeHeader({ scope }: { scope: Scope }) {
  const title = useScopeTitle(scope);
  const refresh = useRefreshAll();
  const refreshing = useRefreshing();
  const prefs = useStore(prefsStore);
  useHotkeys({ refresh: () => refresh.mutate() }, { singleKeys: prefs.shortcuts });
  return (
    <header className="pt-safe shrink-0 border-b border-line bg-bg px-4 pb-2">
      <div className="flex items-center gap-2 pt-2">
        <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
          {title}
        </h1>
        <Button
          variant="ghost"
          size="icon"
          aria-label={refreshing ? "Refreshing" : "Refresh all feeds"}
          onClick={() => refresh.mutate()}
          disabled={refresh.isPending}
        >
          <RefreshCw aria-hidden="true" className={cn(refreshing && "animate-spin")} />
        </Button>
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
  const listKey = scopeKey(scope);
  const onKeyMove = useMemo(
    () =>
      wide
        ? (item: Card) => navigate(articleTo(item.id, scope), { replace: !!articleId, state: { via: "key" } })
        : undefined,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [wide, articleId, listKey, navigate],
  );

  const list = (
    <ListPane
      key={listKey}
      scope={scope}
      activeId={articleId}
      header={<ScopeHeader scope={scope} />}
      onKeyMove={onKeyMove}
      keysEnabled={!articleId || !wide}
    />
  );

  if (!wide) return articleId ? <ArticlePane key={articleId} id={articleId} pane={false} /> : list;

  return (
    <div className="flex h-full min-h-0">
      <div className="w-[26rem] shrink-0 border-r border-line">{list}</div>
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
  const scope = useMemo(() => scopeFromList(view, sp), [view, sp]);
  return <ReaderLayout scope={scope} />;
}

export function ItemRoute() {
  const { id } = useParams();
  const [sp] = useSearchParams();
  const scope = useMemo(() => scopeFromSearch(sp), [sp]);
  return <ReaderLayout scope={scope} articleId={id} />;
}
