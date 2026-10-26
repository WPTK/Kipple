import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { browserOnlineStore } from "@/lib/offlineState";
import { useStore } from "@/lib/store";
import { Link, useLocation, useNavigate } from "react-router";
import { actionLabel } from "@/lib/itemActionTable";
import { BellOff, ChevronDown, ChevronLeft, ChevronUp, ExternalLink, FileCheck, FileText, Mail, MailOpen, MoreHorizontal, Rss, Share2, Star } from "lucide-react";
import { ApiError, failed as failedToast } from "@/api/client";
import { useFulltext, useItem, useOpenItem, useToggleStar } from "@/api/queries";
import { useSwipeBack } from "@/gestures/useSwipeBack";
import { prefersReducedMotion } from "@/gestures/tracking";
import { enhanceEmbeds, enhanceImages, enhanceTables, handleArticleClick, hasNothingToRead, watchImages, watchScrollRegions } from "@/lib/articleDom";
import { useListOrder } from "@/layouts/listOrder";
import { useItemActions } from "@/lib/itemActions";
import { retireUndoToastFor } from "@/lib/undo";
import { articleTo, listTo, openListTo } from "@/lib/routes";
import { sanitizeArticleHtml } from "@/lib/safeHtml";
import { resolveLinkTarget } from "@/lib/links";
import { safeHttpUrl } from "@/lib/safeUrl";
import { ARTICLE_WIDTH_REM, useDevicePrefs } from "@/lib/devicePrefs";
import { fullDate } from "@/lib/format";
import { useLatestRef } from "@/lib/useLatestRef";
import { Button } from "@/ui/button";
import { openOriginalAndRecord, shareAndRecord, useReadingStats, useStatsEnabled } from "@/lib/statsSender";
import { openFilterEditor, similarSeed } from "@/lib/similar";
import { openFeedEditor } from "@/lib/feedEditor";
import { clearMarks, wrapMarks } from "@/lib/highlight";
import { Hl, useGroups } from "@/lib/useHighlights";
import { cn } from "@/lib/cn";
import { announce, toast } from "@/shell/toasts";
import { EmptyState } from "@/ui/EmptyState";
import { NotFoundBlock } from "@/ui/NotFoundBlock";
import { ListImage, useImageFailure } from "@/layouts/parts";
import { endCopy, focusListRow, rememberListPlace } from "./ListPane";
import type { Card, Scope } from "@/api/types";
import { ReadingMenu } from "./AppearanceControls";
import { ListenBar } from "./ListenBar";
import { DropdownMenu } from "@/ui/menu";

/** The article column's max width for the "Article width" setting; Medium is undefined: the stylesheet gives it the density's own measure (.kp-article). */
function columnWidth(w: keyof typeof ARTICLE_WIDTH_REM): string | undefined {
  const rem = ARTICLE_WIDTH_REM[w];
  return w === "medium" ? undefined : rem === null ? "100%" : `${rem}rem`;
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
  const dp = useDevicePrefs();

  const item = useItem(id);
  const open = useOpenItem();
  const star = useToggleStar();
  const act = useItemActions();
  const fulltext = useFulltext();

  // Paging needs the list this article came from. Without one (a shared or bookmarked link) there is none, however
  // much of the fallback Unread list happens to be cached, so buttons and keys agree: no Next or Previous.
  const paging = pane || hasFrom;
  // In the Gazette that is the page's reading order (#315), taken from the list's cache so it works with no list mounted.
  const list = useListOrder(scope, id, paging);
  const { ids } = list;
  // The article's own detail fetch can fail (offline, a server error) with no data at all, but the list this
  // article was opened from often already has this item's card cached, url included: enough to still offer
  // "read the original" even though the article body itself could not be loaded.
  const cachedCard = useMemo(() => list.cards.find((i) => i.id === id), [list.cards, id]);
  const browserOnline = useStore(browserOnlineStore);
  const index = ids.indexOf(id);
  // The list order as it was while this article was in it. An article can leave its list while it is open (saving
  // "Mute similar..." from it drops it from every list but Muted), and Next and Previous then go on from where it was.
  const [seen, setSeen] = useState<{ id: string; ids: string[] } | null>(null);
  if (index >= 0 && (seen?.id !== id || seen.ids !== ids)) setSeen({ id, ids });
  const order = index < 0 && seen?.id === id ? seen.ids : ids;
  const at = order.indexOf(id);
  const prevId = at > 0 ? order[at - 1] : undefined;
  const nextId = at >= 0 ? order[at + 1] : undefined;
  const canPage = at >= 0;
  // Next is off only at the real end: no next article loaded and nothing more on the server.
  const canNext = canPage && (!!nextId || list.more);
  // The footer names the next article; with none loaded yet its Next loads the next page.
  const nextCard = nextId ? list.cards.find((c) => c.id === nextId) : undefined;
  const nextLabel = !nextId ? "Next: more articles" : `Next: ${nextCard ? nextCard.title || "Untitled" : "the next article"}`;

  // Mark read on open, once per article (design 7.1: POST /open, never a stat elsewhere).
  const opened = useRef<string | null>(null);
  useEffect(() => {
    if (!item.data || opened.current === id) return;
    opened.current = id;
    const via = (location.state as { via?: "tap" | "nav" } | null)?.via ?? "tap";
    open.mutate({ id, via });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, item.data?.id]);

  // The article as shown: none while it loads or after it failed (a failed refetch keeps stale data, still an error).
  const failed = !item.isPending && (item.isError || !item.data);
  const a = item.isPending || failed ? undefined : item.data;

  // Beside the list, Next and Previous leave focus on the button that was pressed, so the article that arrived is
  // announced (full screen, its title takes focus and is read instead). One that failed announces its own error.
  const told = useRef<string | null>(null);
  useEffect(() => {
    if (!pane || !a || told.current === id) return;
    told.current = id;
    if ((location.state as { via?: string } | null)?.via === "nav") announce(a.title || "Untitled");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, a?.id, pane]);

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

  const go = (target: string | undefined) => {
    if (!target) return;
    // A toast for this article (marked read from its row menu beside it) would name no article once the next one is open.
    retireUndoToastFor([id]);
    // Prev/next replaces the history entry so back is always one step to the list.
    if (!pane) rememberListPlace(scope, target); // a list beside the article follows it itself
    navigate(articleTo(target, scope), { replace: true, state: { via: "nav" } });
  };
  const next = () => {
    if (nextId) return go(nextId);
    if (canPage && list.more) {
      // A Gazette lays out whole chunks only, so a page that brings no new story asks again while there is more.
      const known = new Set(order);
      const more = async (): Promise<string | undefined> => {
        const r = await list.fetchMore();
        const all = r.ids;
        // An article that left the list goes on to the first one the new page brought.
        const n = all.includes(id) ? all[all.indexOf(id) + 1] : all.find((x) => !known.has(x));
        return n ?? (r.more ? more() : undefined);
      };
      // The end is said aloud, not in a toast: the switched-off Next and the footer already show it.
      void more().then(
        (n) => (n ? go(n) : announce(endCopy(scope))),
        (e) => toast(failedToast("load more articles", e), "error"),
      );
      return;
    }
    if (canPage) announce(endCopy(scope));
  };
  const prev = () => go(prevId);

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
    // Said once the change is made (or kept for later), not before: a failed star is not announced as done.
    star.mutate({ id, starred }, { onSuccess: () => announce(starred ? "Starred" : "Unstarred") });
  };
  const toggleRead = () => {
    if (!item.data) return;
    void act.toggleRead(item.data, "key", undefined, false);
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

  // Escape leaves the article: beside the list focus returns to the list row, full screen goes back. Radix closes a
  // menu or dialog on Escape first (it prevents the default), so an Escape that did that does not also leave.
  const leaveRef = useLatestRef(() => (pane ? focusListRow(id) : back()));
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return;
      if (e.target instanceof HTMLElement && (e.target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName))) return;
      if (document.querySelector('[role="dialog"],[role="menu"]')) return;
      e.preventDefault();
      leaveRef.current();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [leaveRef]);

  // Focus the article when it opens (narrow) so screen readers land on it.
  // The tab mirrors what the screen shows as its heading: the article's title, or the loading and failure screens'.
  const gone = item.error instanceof ApiError && item.error.status === 404;
  useDocumentTitle(
    item.isPending
      ? "Loading article"
      : item.isError || !item.data
        ? gone
          ? "This article is no longer available"
          : "Couldn't open this article"
        : item.data.title || "Untitled",
  );
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

  // Beside the list the footer goes away while the next article loads: focus moves to the toolbar's Next, which stays.
  const footerNext = () => {
    if (pane) frameEl?.querySelector<HTMLElement>('[role="toolbar"] [aria-label="Next article"]')?.focus();
    next();
  };

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
  // A layout effect, so a picture that stands alone is centred before the first paint, not after it.
  const bodyRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!bodyRef.current) return;
    enhanceEmbeds(bodyRef.current);
    enhanceTables(bodyRef.current);
    enhanceImages(bodyRef.current);
    return watchScrollRegions(bodyRef.current);
  }, [html, item.data?.trimmed]);

  // An image refused for good gets the "Image not shown" cue instead of a blank gap. The watch follows the body element
  // itself (a ref callback with a cleanup), so a body that comes back after "Try again" is watched too. The original is
  // offered only online, as on the error screen below: offline it cannot open. Going on or offline re-attaches the
  // watch, which updates the cues already shown.
  const articleUrl = browserOnline ? safeHttpUrl(item.data?.url) : undefined;
  // A feed that sent only a title: nothing to read and no picture either. Told apart from an article that is all pictures.
  const textless = !!a && !a.trimmed && hasNothingToRead(html);
  const originalButton = articleUrl ? <Button variant="solid" onClick={() => openOriginalAndRecord({ id, url: articleUrl })}>Open original</Button> : null;
  const sameTab = linkTarget === "same";
  const bodyCallback = useCallback(
    (el: HTMLDivElement | null) => {
      bodyRef.current = el;
      if (!el) return;
      const stop = watchImages(el, articleUrl, sameTab);
      return () => {
        stop();
        bodyRef.current = null;
      };
    },
    [articleUrl, sameTab],
  );

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

  const ftOn = a?.fulltext.effective === 1;
  // The wide pane is not remounted per article (that would drop focus from the toolbar on Next), so the
  // mutation outlives the article it was started for: busy only means busy for this one.
  const ftBusy = fulltext.isPending && fulltext.variables?.id === id;
  // The toolbar is the pane's, not the article's: beside the list it stays mounted while the next article loads or
  // fails, so the button that was pressed keeps focus, and Next still works from an article that could not open.
  const toolbar = (top: boolean) => (
    <Toolbar
      top={top}
      a={a}
      ftOn={ftOn}
      ftBusy={ftBusy}
      paging={paging}
      canPrev={!!prevId}
      canNext={canNext}
      onPrev={() => prev()}
      onNext={() => next()}
      onStar={toggleStar}
      onRead={toggleRead}
      onFulltext={toggleFulltext}
      onOriginal={openOriginal}
      onShare={share}
      onMuteSimilar={muteSimilar}
      onManageFeed={manageFeed}
    />
  );

  if (!a) {
    // The way back is there from the first frame: on a phone (an installed app has no browser Back) a slow load
    // must never be a screen with no exit.
    // The failure is the page's h1 in both layouts: beside the article the list's name is an h2 (ScopeHeader), so nothing else is one.
    // A 404 is final: retention trimmed the article, or it was deleted, or the id was never one. Retrying cannot help,
    // so that answer says so and offers the list; only other failures (network, 5xx) offer Try again.
    // The original is on the web: with the browser offline it cannot open either, so it is offered only online (the
    // server alone being unreachable, #78, still leaves it).
    const originalUrl = !gone && browserOnline ? safeHttpUrl(cachedCard?.url) : undefined;
    return (
      <div className="flex h-full flex-col">
        {!pane && <TopBar onBack={back} />}
        {pane && toolbar(true)}
        {failed ? (
          <div className="flex min-h-0 flex-1 items-center justify-center">
            {gone ? (
              <NotFoundBlock noun="article" why="It may have been removed by a feed's retention limit or deleted." action={pane ? null : <Button onClick={back}>Back to the list</Button>} />
            ) : (
              <EmptyState
                role="alert"
                asH1
                title="Couldn't open this article"
                why={originalUrl ? "The article couldn't be loaded. You can read the original instead." : "The article couldn't be loaded."}
                action={
                  <div className="flex flex-wrap items-center justify-center gap-2">
                    <Button onClick={() => void item.refetch()}>Try again</Button>
                    {originalUrl ? <Button variant="ghost" onClick={() => openOriginalAndRecord({ id, url: originalUrl })}>Read the original</Button> : null}
                  </div>
                }
              />
            )}
          </div>
        ) : (
          <div className="p-6" aria-busy="true" role="status">
            <span className="sr-only-live">Loading article</span>
            <div aria-hidden="true" className="mx-auto max-w-[46rem] space-y-3">
              {cachedCard ? (
                <LoadingCard card={cachedCard} />
              ) : (
                <>
                  <div className="h-7 w-4/5 rounded bg-surface" />
                  <div className="h-4 w-1/3 rounded bg-surface" />
                </>
              )}
              <div className="h-4 w-full rounded bg-surface" />
              <div className="h-4 w-full rounded bg-surface" />
              <div className="h-4 w-2/3 rounded bg-surface" />
            </div>
          </div>
        )}
      </div>
    );
  }

  return (
    <div ref={frameRef} className="flex h-full min-h-0 flex-col bg-bg">
      {!pane && <TopBar onBack={back} />}
      {pane && toolbar(true)}
      <div ref={scrollerRef} className="swipe-back-area min-h-0 flex-1 overflow-y-auto overscroll-y-contain">
        <article className="kp-article px-4 pt-4 pb-10 md:px-6" aria-labelledby="article-title" style={{ "--kp-col": columnWidth(dp.articleWidth) } as CSSProperties}>
          <header className="kp-ui mx-auto mb-5 max-w-[min(var(--kp-col),100%)]">
            {/* The line runs in the feed name's direction: "Unread" has a dir of its own, so dir="auto" skips it and an
                Arabic feed's line sits on the right like its title. */}
            <p dir="auto" className="text-sm text-fg2">
              {/* Only an unread article says so, in plain text, not a pill: a "Read" word beside the toolbar's read toggle
                  looked like a second button, and a read article is the ordinary case that needs no mark. */}
              {a.read ? null : (
                <>
                  <span dir="ltr" data-testid="read-state" className="font-semibold text-fg">
                    Unread
                  </span>
                  <span aria-hidden="true">{" · "}</span>
                </>
              )}
              <Link to={openListTo({ feed: a.feed_id })} className="hit-pad hover:underline">
                {a.source || a.feed.title}
              </Link>
            </p>
            <h1 id="article-title" ref={headingRef} tabIndex={-1} dir="auto" className="mt-1 text-title leading-tight font-[650] text-balance wrap-anywhere outline-none [font-family:var(--kp-reading-font)] [font-size-adjust:0.5]">
              <Hl text={a.title || "Untitled"} field="title" feedId={a.feed_id} />
            </h1>
            <p dir="auto" className="mt-2 text-sm text-fg2">
              {a.author ? (
                <>
                  <Hl text={a.author} field="author" feedId={a.feed_id} />
                  {" · "}
                </>
              ) : null}
              {[fullDate(a.published_at), a.reading_minutes ? `${a.reading_minutes} min read` : null].filter(Boolean).join(" · ")}
            </p>
          </header>
          {/* Not a live region: whatever muted the open article (a filter saved from it) says so in its own toast. */}
          {a.muted_by !== null && a.muted_by !== undefined ? (
            <div data-testid="muted-banner" className="kp-ui mx-auto mb-4 flex max-w-[min(var(--kp-col),100%)] flex-wrap items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1 text-sm">
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
            <p role="status" className="kp-ui mx-auto mb-4 flex max-w-[min(var(--kp-col),100%)] items-center gap-3 rounded-lg border border-line bg-surface px-3 py-2 text-sm">
              <span className="flex-1">Couldn't load full text. Showing the version from the feed instead.</span>
              <Button size="default" onClick={() => fulltext.mutate({ id, refresh: true })}>
                Try again
              </Button>
            </p>
          ) : null}
          {a.trimmed || textless ? null : <ListenBar bodyRef={bodyRef} articleId={id} />}
          {a.trimmed ? (
            <EmptyState title="This article was removed" why="Kipple keeps only the newest articles for this feed. Open the original to read it." action={originalButton} />
          ) : textless ? (
            <EmptyState title="This feed sent only a title" why="There is no text to show here. The original may have more." action={originalButton} />
          ) : (
            <div
              ref={bodyCallback}
              className="article-body"
              dir="auto"
              data-testid="article-body"
              onClick={(e) => {
                if (bodyRef.current) handleArticleClick(e, bodyRef.current, scroller.current, !prefersReducedMotion());
              }}
              dangerouslySetInnerHTML={{ __html: html }}
            />
          )}
          {paging && canPage ? (
            <ArticleEnd at={at + 1} of={order.length} more={list.more} canNext={canNext} label={nextLabel} end={endCopy(scope)} onNext={footerNext} />
          ) : null}
        </article>
      </div>
      {!pane && toolbar(false)}
    </div>
  );
}

/** The end of an article: where it sits in its list and the next one by name, so a long read ends without a scroll back to the toolbar. */
function ArticleEnd(p: { at: number; of: number; more: boolean; canNext: boolean; label: string; end: string; onNext: () => void }) {
  return (
    <footer data-testid="article-end" className="kp-ui mx-auto mt-10 max-w-[min(var(--kp-col),100%)] border-t border-line pt-4 text-sm text-fg2">
      <p>{p.more ? `Article ${p.at} of ${p.of} so far` : `Article ${p.at} of ${p.of}`}</p>
      {p.canNext ? (
        <Button variant="ghost" onClick={p.onNext} className="mt-2 h-auto min-h-11 max-w-full justify-start gap-2 px-2 py-2 text-left text-base">
          <ChevronDown aria-hidden="true" className="shrink-0" />
          <span className="line-clamp-2 min-w-0">{p.label}</span>
        </Button>
      ) : (
        <p className="mt-2">{p.end}</p>
      )}
    </footer>
  );
}

function TopBar({ onBack }: { onBack: () => void }) {
  return (
    <header className="pt-safe flex shrink-0 items-center border-b border-line bg-bg px-1">
      {/* History back: it may return to a feed, a search or the Feeds screen, so it is named for what it does, not "Back to list". */}
      <Button variant="ghost" onClick={onBack} className="px-2">
        <ChevronLeft aria-hidden="true" />
        <span>Back</span>
      </Button>
    </header>
  );
}

interface ToolbarProps {
  /** The open article; none while the next one loads or after it failed (beside the list the toolbar stays). */
  a: ReturnType<typeof useItem>["data"];
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


function Toolbar(p: ToolbarProps) {
  // With no article (the next one loading) More actions is switched off but keeps focus, and its menu does not open.
  const [more, setMore] = useState(false);
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
          <Button variant="ghost" size="icon" onClick={p.onPrev} aria-disabled={!p.canPrev || undefined} aria-label="Previous article">
            <ChevronUp aria-hidden="true" />
          </Button>
          <Button variant="ghost" size="icon" onClick={p.onNext} aria-disabled={!p.canNext || undefined} aria-label="Next article">
            <ChevronDown aria-hidden="true" />
          </Button>
        </>
      ) : null}
      <Button
        variant="ghost"
        size="icon"
        onClick={p.onStar}
        aria-disabled={!p.a || undefined}
        aria-pressed={!!p.a?.starred}
        aria-label={p.a?.starred ? "Unstar" : "Star"}
        className={p.a?.starred ? "text-star" : undefined}
      >
        <Star aria-hidden="true" fill={p.a?.starred ? "currentColor" : "none"} />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        onClick={p.onFulltext}
        aria-disabled={p.ftBusy || !p.a || undefined}
        aria-pressed={p.ftOn}
        aria-label="Full text"
        className={p.ftOn ? "bg-selection" : undefined}
      >
        {/* The state is the glyph (a page with a check mark while the full text is shown), not only the tint: a toggle's state never rests on color alone. */}
        {p.ftOn ? <FileCheck aria-hidden="true" /> : <FileText aria-hidden="true" />}
      </Button>
      <Button variant="ghost" size="icon" onClick={p.onShare} aria-disabled={!p.a || undefined} aria-label="Share" title="Share">
        <Share2 aria-hidden="true" />
      </Button>
      <ReadingMenu />
      {/* The rarely used actions (read state included) share one 44 px target, last in the row, so seven targets (308 px) fit the narrowest phone and a narrow pane with Aa. */}
      <DropdownMenu.Root open={more && !!p.a} onOpenChange={(o) => setMore(o && !!p.a)}>
        <DropdownMenu.Trigger asChild>
          <Button variant="ghost" size="icon" aria-label="More actions" title="More actions" aria-disabled={!p.a || undefined}>
            <MoreHorizontal aria-hidden="true" />
          </Button>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-56">
            <DropdownMenu.Item onSelect={p.onRead}>
              {p.a?.read ? <Mail className="size-5" aria-hidden="true" /> : <MailOpen className="size-5" aria-hidden="true" />}
              {actionLabel("read", { on: p.a?.read })}
            </DropdownMenu.Item>
            <DropdownMenu.Item onSelect={p.onManageFeed}>
              <Rss className="size-5" aria-hidden="true" />
              Edit feed
            </DropdownMenu.Item>
            <DropdownMenu.Item onSelect={p.onOriginal}>
              <ExternalLink className="size-5" aria-hidden="true" />
              Open original
            </DropdownMenu.Item>
            <DropdownMenu.Item onSelect={p.onMuteSimilar}>
              <BellOff className="size-5" aria-hidden="true" />
              Mute similar…
            </DropdownMenu.Item>
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
    </div>
  );
}


/** What the list already knew of the article (title, source, lead picture), shown while the full text loads. */
function LoadingCard({ card }: { card: Card }) {
  const failure = useImageFailure(card.image);
  return (
    <>
      <p data-testid="loading-card-title" className="kp-ui text-2xl leading-tight font-semibold text-balance wrap-anywhere text-fg">{card.title || "Untitled"}</p>
      <p className="kp-ui text-sm text-fg2">{card.source}</p>
      {card.image ? <ListImage src={card.image} failure={failure} className="aspect-video w-full rounded-lg bg-surface" /> : null}
    </>
  );
}
