import { useEffect, useMemo, useRef } from "react";
import { useLocation, useNavigate } from "react-router";
import { ChevronDown, ChevronLeft, ChevronRight, ChevronUp, ExternalLink, FileText, Mail, MailOpen, Star } from "lucide-react";
import { flattenItems, useFulltext, useItem, useItems, useOpenItem, useToggleStar } from "@/api/queries";
import { useSwipeBack } from "@/gestures/useSwipeBack";
import { prefersReducedMotion } from "@/gestures/tracking";
import { enhanceEmbeds, handleArticleClick } from "@/lib/articleDom";
import { useItemActions } from "@/lib/itemActions";
import { articleTo, listTo } from "@/lib/routes";
import { sanitizeArticleHtml } from "@/lib/safeHtml";
import { fullDate } from "@/lib/format";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { Button } from "@/ui/button";
import { cn } from "@/lib/cn";
import { announce, toast } from "@/shell/toasts";
import { StatusBlock, focusListRow } from "./ListPane";
import type { Scope } from "@/api/types";
import { ReadingMenu } from "./AppearanceControls";
import { ListenBar } from "./ListenBar";

interface Props {
  id: string;
  /** The list this article belongs to (the route builds it once, honouring the sort-order preference). */
  scope: Scope;
  /** The URL carried `?from=`: only then is the list loaded for previous/next in the full-screen view. */
  hasFrom: boolean;
  /** Wide screens show the article beside the list: no back button, navigation replaces. */
  pane: boolean;
}

export function ArticlePane({ id, scope, hasFrom, pane }: Props) {
  const navigate = useNavigate();
  const location = useLocation();
  const prefs = useStore(prefsStore);

  const item = useItem(id);
  const list = useItems(scope, hasFrom || pane);
  const open = useOpenItem();
  const star = useToggleStar();
  const act = useItemActions();
  const fulltext = useFulltext();

  const ids = useMemo(() => flattenItems(list.data).map((i) => i.id), [list.data]);
  const index = ids.indexOf(id);
  const prevId = index > 0 ? ids[index - 1] : undefined;
  const nextId = index >= 0 ? ids[index + 1] : undefined;
  const canPage = index >= 0;

  // Mark read on open, once per article (design 7.1: POST /open, never a stat elsewhere).
  const opened = useRef<string | null>(null);
  useEffect(() => {
    if (!item.data || opened.current === id) return;
    opened.current = id;
    const via = (location.state as { via?: "tap" | "key" | "nav" } | null)?.via ?? "tap";
    open.mutate({ id, via });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, item.data?.id]);

  // Effective full-text with nothing stored yet: ask the server to extract (design 7.5).
  const ft = item.data?.fulltext;
  const asked = useRef<string | null>(null);
  useEffect(() => {
    if (ft && ft.effective === 1 && !ft.available && !ft.error && asked.current !== id) {
      asked.current = id;
      fulltext.mutate({ id });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, ft?.effective, ft?.available, ft?.error]);

  // Keyed on the markup alone: a read or star patch must not re-run the sanitizer over the whole body.
  const content = item.data?.content_html;
  const html = useMemo(() => (content === undefined ? "" : sanitizeArticleHtml(content)), [content]);

  const go = (target: string | undefined, via: "key" | "nav") => {
    if (!target) return;
    // Prev/next replaces the history entry so back is always one step to the list.
    navigate(articleTo(target, scope), { replace: true, state: { via } });
  };
  const next = (via: "key" | "nav") => {
    if (nextId) return go(nextId, via);
    if (canPage && list.hasNextPage) {
      void list.fetchNextPage().then((r) => {
        const all = flattenItems(r.data).map((i) => i.id);
        const n = all[all.indexOf(id) + 1];
        if (n) go(n, via);
        else toast("End of list");
      });
      return;
    }
    if (canPage) toast("End of list");
  };
  const prev = (via: "key" | "nav") => go(prevId, via);

  const back = () => {
    const idx = (window.history.state as { idx?: number } | null)?.idx ?? 0;
    if (idx > 0) navigate(-1);
    else navigate(listTo(scope), { replace: true });
  };

  const openOriginal = () => {
    if (item.data?.url) window.open(item.data.url, "_blank", "noopener,noreferrer");
  };
  const toggleStar = () => {
    if (!item.data) return;
    const starred = !item.data.starred;
    star.mutate({ id, starred });
    announce(starred ? "Starred" : "Unstarred");
  };
  const toggleRead = () => {
    if (!item.data) return;
    void act.toggleRead(item.data, "key");
  };
  const toggleFulltext = () => {
    if (!item.data) return;
    fulltext.mutate({ id, mode: item.data.fulltext.effective === 1 ? 0 : 1 });
  };

  // Precedence in the wide pane: the open article is the list's selected row, so the list's keys drive
  // j/k/m/s/o/v (and stay live for mark all, above/below, select, gg/G, Enter). The article keeps only
  // what the list has no notion of: f (full text) and u/Esc, which returns focus to the list. If the
  // article is not among the loaded rows (a deep link), the list has nothing to drive and the article
  // takes the whole set. In the full-screen view the list is not on screen, so the article owns all keys.
  const ownsItemKeys = !pane || !canPage;
  useHotkeys(
    {
      ...(ownsItemKeys
        ? { next: () => next("key"), prev: () => prev("key"), original: openOriginal, star: toggleStar, toggleRead }
        : {}),
      fulltext: toggleFulltext,
      up: () => (pane ? focusListRow(id) : back()),
    },
    { singleKeys: prefs.shortcuts },
  );

  // Focus the article when it opens (narrow) so screen readers land on it.
  const headingRef = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (item.data && !pane) headingRef.current?.focus({ preventScroll: true });
  }, [item.data?.id, pane]); // eslint-disable-line react-hooks/exhaustive-deps

  const scroller = useRef<HTMLDivElement>(null);
  useEffect(() => {
    scroller.current?.scrollTo({ top: 0 });
  }, [id]);

  // Right-swipe from the body (not the left 24 px) pops to the list you came from.
  const frame = useRef<HTMLDivElement>(null);
  useSwipeBack(frame, { enabled: !pane && !!item.data, onBack: back });

  // Embed placeholders get a real Play button once the HTML is in the DOM.
  const bodyRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (bodyRef.current) enhanceEmbeds(bodyRef.current);
  }, [html, item.data?.trimmed]);

  if (item.isPending) {
    return (
      <div className="p-6" aria-busy="true" role="status">
        <span className="sr-only-live">Loading article</span>
        <div aria-hidden="true" className="mx-auto max-w-[46rem] space-y-3">
          <div className="h-7 w-4/5 rounded bg-surface" />
          <div className="h-4 w-1/3 rounded bg-surface" />
          <div className="h-4 w-full rounded bg-surface" />
          <div className="h-4 w-full rounded bg-surface" />
          <div className="h-4 w-2/3 rounded bg-surface" />
        </div>
      </div>
    );
  }
  if (item.isError || !item.data) {
    return (
      <div className="flex h-full flex-col">
        {!pane && <TopBar onBack={back} />}
        <StatusBlock role="alert" title="Couldn't open this article" body="The article couldn't be loaded. You can read the original instead.">
          <Button onClick={() => void item.refetch()}>Try again</Button>
        </StatusBlock>
      </div>
    );
  }

  const a = item.data;
  const ftOn = a.fulltext.effective === 1;

  return (
    <div ref={frame} className="flex h-full min-h-0 flex-col bg-bg">
      {!pane && <TopBar onBack={back} />}
      {pane && (
        <Toolbar
          top
          a={a}
          ftOn={ftOn}
          ftBusy={fulltext.isPending}
          canPrev={!!prevId}
          canNext={canPage}
          onPrev={() => prev("nav")}
          onNext={() => next("nav")}
          onStar={toggleStar}
          onRead={toggleRead}
          onFulltext={toggleFulltext}
          onOriginal={openOriginal}
        />
      )}
      <div ref={scroller} className="swipe-back-area min-h-0 flex-1 overflow-y-auto overscroll-y-contain">
        <article className="px-4 pt-4 pb-10 md:px-6" aria-labelledby="article-title">
          <header className="mx-auto mb-5 max-w-[min(var(--kp-measure),46rem)]">
            <p className="text-sm text-fg2">
              <a href={a.feed.site_url || undefined} target="_blank" rel="noopener noreferrer" className="hover:underline">
                {a.source || a.feed.title}
              </a>
            </p>
            <h1 id="article-title" ref={headingRef} tabIndex={-1} className="mt-1 text-2xl leading-tight font-bold outline-none [font-family:var(--kp-reading-font)]">
              {a.title || "Untitled"}
            </h1>
            <p className="mt-2 text-sm text-fg2">
              {[a.author, fullDate(a.published_at), a.reading_minutes ? `${a.reading_minutes} min read` : null]
                .filter(Boolean)
                .join(" · ")}
            </p>
          </header>
          {a.fulltext.error && ftOn ? (
            <p role="status" className="mx-auto mb-4 flex max-w-[min(var(--kp-measure),46rem)] items-center gap-3 rounded-lg border border-line bg-surface px-3 py-2 text-sm">
              <span className="flex-1">Couldn't load full text. Showing the version from the feed instead.</span>
              <Button size="default" onClick={() => fulltext.mutate({ id, refresh: true })}>
                Try again
              </Button>
            </p>
          ) : null}
          {a.trimmed ? null : <ListenBar bodyRef={bodyRef} articleId={id} />}
          {a.trimmed ? (
            <StatusBlock role="status" title="This article was removed" body="Kipple keeps only the newest articles for this feed. Open the original to read it." />
          ) : (
            <div
              ref={bodyRef}
              className="article-body"
              data-testid="article-body"
              onClick={(e) => {
                if (bodyRef.current) handleArticleClick(e, bodyRef.current, scroller.current, !prefersReducedMotion());
              }}
              dangerouslySetInnerHTML={{ __html: html }}
            />
          )}
        </article>
      </div>
      {!pane && (
        <Toolbar
          a={a}
          ftOn={ftOn}
          ftBusy={fulltext.isPending}
          canPrev={!!prevId}
          canNext={canPage}
          onPrev={() => prev("nav")}
          onNext={() => next("nav")}
          onStar={toggleStar}
          onRead={toggleRead}
          onFulltext={toggleFulltext}
          onOriginal={openOriginal}
        />
      )}
    </div>
  );
}

function TopBar({ onBack }: { onBack: () => void }) {
  return (
    <header className="pt-safe flex shrink-0 items-center border-b border-line bg-bg px-1">
      <Button variant="ghost" onClick={onBack} aria-label="Back to list" className="px-2">
        <ChevronLeft aria-hidden="true" />
        <span>Back</span>
      </Button>
    </header>
  );
}

interface ToolbarProps {
  a: NonNullable<ReturnType<typeof useItem>["data"]>;
  ftOn: boolean;
  ftBusy: boolean;
  canPrev: boolean;
  canNext: boolean;
  top?: boolean;
  onPrev: () => void;
  onNext: () => void;
  onStar: () => void;
  onRead: () => void;
  onFulltext: () => void;
  onOriginal: () => void;
}

function Toolbar(p: ToolbarProps) {
  return (
    <div
      role="toolbar"
      aria-label="Article actions"
      className={cn(
        "flex shrink-0 items-center justify-around gap-1 bg-bg px-2",
        p.top ? "border-b border-line py-1" : "pb-safe border-t border-line py-1",
      )}
    >
      <Button variant="ghost" size="icon" onClick={p.onPrev} disabled={!p.canPrev} aria-label="Previous article">
        {p.top ? <ChevronUp aria-hidden="true" /> : <ChevronLeft aria-hidden="true" />}
      </Button>
      <Button variant="ghost" size="icon" onClick={p.onNext} disabled={!p.canNext} aria-label="Next article">
        {p.top ? <ChevronDown aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
      </Button>
      <Button
        variant="ghost"
        size="icon"
        onClick={p.onStar}
        aria-pressed={p.a.starred}
        aria-label={p.a.starred ? "Unstar" : "Star"}
        className={p.a.starred ? "text-star" : undefined}
      >
        <Star aria-hidden="true" fill={p.a.starred ? "currentColor" : "none"} />
      </Button>
      <Button variant="ghost" size="icon" onClick={p.onRead} aria-label={p.a.read ? "Mark as unread" : "Mark as read"}>
        {p.a.read ? <Mail aria-hidden="true" /> : <MailOpen aria-hidden="true" />}
      </Button>
      <Button
        variant="ghost"
        size="icon"
        onClick={p.onFulltext}
        disabled={p.ftBusy}
        aria-pressed={p.ftOn}
        aria-label="Full text"
        className={p.ftOn ? "bg-selection" : undefined}
      >
        <FileText aria-hidden="true" />
      </Button>
      <Button variant="ghost" size="icon" onClick={p.onOriginal} aria-label="Open original">
        <ExternalLink aria-hidden="true" />
      </Button>
      <ReadingMenu />
    </div>
  );
}

