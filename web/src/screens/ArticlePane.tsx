import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties } from "react";
import { useLocation, useNavigate } from "react-router";
import { DropdownMenu } from "radix-ui";
import { BellOff, ChevronDown, ChevronLeft, ChevronRight, ChevronUp, ExternalLink, FileText, Mail, MailOpen, MoreHorizontal, Rss, Share2, Star } from "lucide-react";
import { ApiError } from "@/api/client";
import { flattenItems, useFulltext, useItem, useItems, useOpenItem, useToggleStar } from "@/api/queries";
import { useSwipeBack } from "@/gestures/useSwipeBack";
import { prefersReducedMotion } from "@/gestures/tracking";
import { enhanceEmbeds, handleArticleClick } from "@/lib/articleDom";
import { useItemActions } from "@/lib/itemActions";
import { articleTo, listTo } from "@/lib/routes";
import { sanitizeArticleHtml } from "@/lib/safeHtml";
import { resolveLinkTarget } from "@/lib/links";
import { safeHttpUrl } from "@/lib/safeUrl";
import { ARTICLE_WIDTH_REM, useDevicePrefs } from "@/lib/devicePrefs";
import { fullDate } from "@/lib/format";
import { useHotkeys } from "@/lib/keys";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { Button } from "@/ui/button";
import { openOriginalAndRecord, shareAndRecord, useReadingStats, useStatsEnabled } from "@/lib/statsSender";
import { openFilterEditor, similarSeed } from "@/lib/similar";
import { openFeedEditor } from "@/lib/feedEditor";
import { clearMarks, wrapMarks } from "@/lib/highlight";
import { Hl, useGroups } from "@/lib/useHighlights";
import { cn } from "@/lib/cn";
import { announce, toast } from "@/shell/toasts";
import { StatusBlock, focusListRow, rememberListPlace } from "./ListPane";
import type { Scope } from "@/api/types";
import { ReadingMenu } from "./AppearanceControls";
import { ListenBar } from "./ListenBar";

/** The article column's max width for the "Article width" setting (Medium is the density's own measure). */
export function columnWidth(w: keyof typeof ARTICLE_WIDTH_REM): string {
  const rem = ARTICLE_WIDTH_REM[w];
  return w === "medium" ? "min(var(--kp-measure), 46rem)" : rem === null ? "100%" : `${rem}rem`;
}

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
  const dp = useDevicePrefs();

  const item = useItem(id);
  const list = useItems(scope, hasFrom || pane);
  const open = useOpenItem();
  const star = useToggleStar();
  const act = useItemActions();
  const fulltext = useFulltext();

  // Paging needs the list this article came from. Without one (a shared or bookmarked link) there is none, however
  // much of the fallback Unread list happens to be cached, so buttons and keys agree: no Next or Previous.
  const paging = pane || hasFrom;
  const ids = useMemo(() => (paging ? flattenItems(list.data).map((i) => i.id) : []), [list.data, paging]);
  // The article's own detail fetch can fail (offline, a server error) with no data at all, but the list this
  // article was opened from often already has this item's card cached, url included: enough to still offer
  // "read the original" even though the article body itself could not be loaded.
  const cachedCard = useMemo(() => flattenItems(list.data).find((i) => i.id === id), [list.data, id]);
  const browserOnline = useSyncExternalStore(subscribeOnline, () => navigator.onLine !== false);
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
  const linkTarget = resolveLinkTarget(dp.linkTarget);
  const html = useMemo(() => (content === undefined ? "" : sanitizeArticleHtml(content, linkTarget)), [content, linkTarget]);

  const go = (target: string | undefined, via: "key" | "nav") => {
    if (!target) return;
    // Prev/next replaces the history entry so back is always one step to the list.
    if (!pane) rememberListPlace(scope, target); // a list beside the article follows it itself
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
    if (item.data) openOriginalAndRecord({ id, url: item.data.url });
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
  const share = () => {
    if (!item.data) return;
    void shareAndRecord({ id, title: item.data.title, url: item.data.url });
  };
  const muteSimilar = () => {
    if (item.data) openFilterEditor({ mode: "create", seed: similarSeed(item.data, item.data.feed.title) });
  };
  const manageFeed = () => {
    if (item.data) openFeedEditor(item.data.feed_id);
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

  // The scroller and the pane frame are kept as elements too (callback refs into state): the error screen has
  // neither, and "Try again" brings new ones, so the reading session must follow the element, not a stale ref.
  const scroller = useRef<HTMLDivElement | null>(null);
  const [scrollerEl, setScrollerEl] = useState<HTMLDivElement | null>(null);
  const scrollerRef = useCallback((el: HTMLDivElement | null) => {
    scroller.current = el;
    setScrollerEl(el);
  }, []);
  useEffect(() => {
    scroller.current?.scrollTo({ top: 0 });
  }, [id]);

  // Right-swipe from the body (not the left 24 px) pops to the list you came from. The element, not a ref: after
  // "Try again" the frame is a new element and the gesture must move to it.
  const [frameEl, setFrameEl] = useState<HTMLDivElement | null>(null);
  const frameRef = useCallback((el: HTMLDivElement | null) => setFrameEl(el), []);
  useSwipeBack(frameEl, { enabled: !pane && !!item.data, onBack: back });

  // Reading stats (design 8, rule 5): one session per open, keyed by the open's answer. Only this article's own
  // open counts: a result left over from the previous article in the wide pane has another id. Activity counts only
  // inside this pane (never over the list beside it).
  const statsOn = useStatsEnabled();
  useReadingStats({
    itemId: id,
    sessionKey: open.data && open.variables?.id === id ? open.data.session_key : "",
    enabled: statsOn,
    scroller: scrollerEl,
    pane: frameEl,
  });

  // Embed placeholders get a real Play button once the HTML is in the DOM.
  const bodyRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (bodyRef.current) enhanceEmbeds(bodyRef.current);
  }, [html, item.data?.trimmed]);

  // Highlight filters: after the article was sanitized and put in the DOM, split its text nodes around the words
  // (never HTML built from a term). The cleanup puts the text back, so turning the setting off or editing a rule
  // never leaves stale marks.
  const bodyGroups = useGroups("content", item.data?.feed_id);
  useEffect(() => {
    const root = bodyRef.current;
    if (!root || bodyGroups.length === 0) return;
    wrapMarks(root, bodyGroups);
    return () => clearMarks(root);
  }, [html, bodyGroups, item.data?.trimmed]);

  if (item.isPending) {
    // The way back is there from the first frame: on a phone (an installed app has no browser Back) a slow load
    // must never be a screen with no exit.
    return (
      <div className="flex h-full flex-col">
        {!pane && <TopBar onBack={back} />}
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
      </div>
    );
  }
  if (item.isError || !item.data) {
    // A 404 is final: retention trimmed the article, or it was deleted, or the id was never one. Retrying cannot help,
    // so that answer says so and offers the list; only other failures (network, 5xx) offer Try again.
    const gone = item.error instanceof ApiError && item.error.status === 404;
    // The original is on the web: with the browser offline it cannot open either, so it is offered only online (the
    // server alone being unreachable, #78, still leaves it).
    const originalUrl = !gone && browserOnline ? safeHttpUrl(cachedCard?.url) : undefined;
    return (
      <div className="flex h-full flex-col">
        {!pane && <TopBar onBack={back} />}
        <div className="flex min-h-0 flex-1 items-center justify-center">
          <StatusBlock
            role="alert"
            title={gone ? "This article is no longer available" : "Couldn't open this article"}
            body={
              gone
                ? "It may have been removed by a feed's retention limit or deleted."
                : originalUrl
                  ? "The article couldn't be loaded. You can read the original instead."
                  : "The article couldn't be loaded."
            }
          >
            <div className="flex flex-wrap items-center justify-center gap-2">
              {gone ? pane ? null : <Button onClick={back}>Back to the list</Button> : <Button onClick={() => void item.refetch()}>Try again</Button>}
              {originalUrl ? <Button variant="ghost" onClick={() => openOriginalAndRecord({ id, url: originalUrl })}>Read the original</Button> : null}
            </div>
          </StatusBlock>
        </div>
      </div>
    );
  }

  const a = item.data;
  const ftOn = a.fulltext.effective === 1;
  // The wide pane is not remounted per article (that would drop focus from the toolbar on Next), so the
  // mutation outlives the article it was started for: busy only means busy for this one.
  const ftBusy = fulltext.isPending && fulltext.variables?.id === id;

  return (
    <div ref={frameRef} className="flex h-full min-h-0 flex-col bg-bg">
      {!pane && <TopBar onBack={back} />}
      {pane && (
        <Toolbar
          top
          a={a}
          ftOn={ftOn}
          ftBusy={ftBusy}
          paging={paging}
          canPrev={!!prevId}
          canNext={canPage}
          onPrev={() => prev("nav")}
          onNext={() => next("nav")}
          onStar={toggleStar}
          onRead={toggleRead}
          onFulltext={toggleFulltext}
          onOriginal={openOriginal}
          onShare={share}
          onMuteSimilar={muteSimilar}
          onManageFeed={manageFeed}
        />
      )}
      <div ref={scrollerRef} className="swipe-back-area min-h-0 flex-1 overflow-y-auto overscroll-y-contain">
        <article className="px-4 pt-4 pb-10 md:px-6" aria-labelledby="article-title" style={{ "--kp-col": columnWidth(dp.articleWidth) } as CSSProperties}>
          <header className="mx-auto mb-5 max-w-[min(var(--kp-col),100%)]">
            <p className="text-sm text-fg2">
              <span
                data-testid="read-state"
                className={cn("mr-2 rounded-full border px-2 py-0.5 text-xs font-medium", a.read ? "border-line text-fg2" : "border-accent text-fg")}
              >
                {a.read ? "Read" : "Unread"}
              </span>
              <a href={safeHttpUrl(a.feed.site_url)} target={linkTarget === "new" ? "_blank" : undefined} rel="noopener noreferrer" className="hover:underline">
                {a.source || a.feed.title}
              </a>
            </p>
            <h1 id="article-title" ref={headingRef} tabIndex={-1} className="mt-1 text-2xl leading-tight font-bold outline-none [font-family:var(--kp-reading-font)]">
              <Hl text={a.title || "Untitled"} field="title" feedId={a.feed_id} />
            </h1>
            <p className="mt-2 text-sm text-fg2">
              {a.author ? (
                <>
                  <Hl text={a.author} field="author" feedId={a.feed_id} />
                  {" · "}
                </>
              ) : null}
              {[fullDate(a.published_at), a.reading_minutes ? `${a.reading_minutes} min read` : null].filter(Boolean).join(" · ")}
            </p>
          </header>
          {a.muted_by !== null && a.muted_by !== undefined ? (
            <div role="status" data-testid="muted-banner" className="mx-auto mb-4 flex max-w-[min(var(--kp-col),100%)] flex-wrap items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1 text-sm">
              <span className="min-w-0 flex-1">
                {a.muted_by_name ? (
                  <>
                    Muted by <strong>{a.muted_by_name}</strong>.
                  </>
                ) : (
                  "Muted by a filter that was deleted."
                )}
              </span>
              <Button onClick={() => void act.restoreMuted([a])}>Restore</Button>
              {a.muted_by_name ? <Button variant="ghost" onClick={() => openFilterEditor({ mode: "edit", id: a.muted_by as string })}>Edit rule</Button> : null}
            </div>
          ) : null}
          {a.fulltext.error && ftOn ? (
            <p role="status" className="mx-auto mb-4 flex max-w-[min(var(--kp-col),100%)] items-center gap-3 rounded-lg border border-line bg-surface px-3 py-2 text-sm">
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
          ftBusy={ftBusy}
          paging={paging}
          canPrev={!!prevId}
          canNext={canPage}
          onPrev={() => prev("nav")}
          onNext={() => next("nav")}
          onStar={toggleStar}
          onRead={toggleRead}
          onFulltext={toggleFulltext}
          onOriginal={openOriginal}
          onShare={share}
          onMuteSimilar={muteSimilar}
          onManageFeed={manageFeed}
        />
      )}
    </div>
  );
}

/** The browser's own online state (navigator.onLine), not the app's: a request failing sets the app offline too. */
function subscribeOnline(cb: () => void): () => void {
  window.addEventListener("online", cb);
  window.addEventListener("offline", cb);
  return () => {
    window.removeEventListener("online", cb);
    window.removeEventListener("offline", cb);
  };
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
  /** There is a list to page through (an article opened with no list, such as a shared link, has none: no Next or Previous). */
  paging: boolean;
  canPrev: boolean;
  canNext: boolean;
  top?: boolean;
  onPrev: () => void;
  onNext: () => void;
  onStar: () => void;
  onRead: () => void;
  onFulltext: () => void;
  onOriginal: () => void;
  onShare: () => void;
  onMuteSimilar: () => void;
  onManageFeed: () => void;
}

const moreItem = "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[highlighted]:bg-selection";

function Toolbar(p: ToolbarProps) {
  return (
    <div
      role="toolbar"
      aria-label="Article actions"
      className={cn(
        "flex shrink-0 items-center justify-around overflow-x-auto bg-bg px-1",
        p.top ? "border-b border-line py-1" : "pb-safe border-t border-line py-1",
      )}
    >
      {p.paging ? (
        <>
          <Button variant="ghost" size="icon" onClick={p.onPrev} disabled={!p.canPrev} aria-label="Previous article">
            {p.top ? <ChevronUp aria-hidden="true" /> : <ChevronLeft aria-hidden="true" />}
          </Button>
          <Button variant="ghost" size="icon" onClick={p.onNext} disabled={!p.canNext} aria-label="Next article">
            {p.top ? <ChevronDown aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
          </Button>
        </>
      ) : null}
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
      <Button
        variant="ghost"
        size="icon"
        onClick={p.onRead}
        aria-label={p.a.read ? "Mark as unread" : "Mark as read"}
        title={p.a.read ? "Mark as unread" : "Mark as read"}
      >
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
      {/* The rarely used actions (Share included) share one 44 px target, so seven targets (308 px) fit the narrowest phone and a narrow pane with Aa. */}
      <DropdownMenu.Root>
        <DropdownMenu.Trigger asChild>
          <Button variant="ghost" size="icon" aria-label="More actions" title="More actions">
            <MoreHorizontal aria-hidden="true" />
          </Button>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-56 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
            <DropdownMenu.Item className={moreItem} onSelect={p.onManageFeed}>
              <Rss className="size-5" aria-hidden="true" />
              Manage this feed
            </DropdownMenu.Item>
            <DropdownMenu.Item className={moreItem} onSelect={p.onOriginal}>
              <ExternalLink className="size-5" aria-hidden="true" />
              Open original
            </DropdownMenu.Item>
            <DropdownMenu.Item className={moreItem} onSelect={p.onShare}>
              <Share2 className="size-5" aria-hidden="true" />
              Share
            </DropdownMenu.Item>
            <DropdownMenu.Item className={moreItem} onSelect={p.onMuteSimilar}>
              <BellOff className="size-5" aria-hidden="true" />
              Mute similar…
            </DropdownMenu.Item>
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
      <ReadingMenu />
    </div>
  );
}

