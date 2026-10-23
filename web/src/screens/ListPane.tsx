import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useLatestRef } from "@/lib/useLatestRef";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { defaultRangeExtractor, useVirtualizer, type Range, type VirtualItem } from "@tanstack/react-virtual";
import { remeasureMounted } from "@/lib/remeasure";
import { Link, useNavigate } from "react-router";
import { RefreshCw, X } from "lucide-react";
import { ApiError, SESSION_EXPIRED, errorMessage } from "@/api/client";
import { offlineStore } from "@/lib/offlineState";
import { applyRead, changeError, keys, scopeKey, useBootstrap } from "@/api/queries";
import { clearPending, liveStore, pendingFor } from "@/api/events";
import { CANT_REFRESH_OFFLINE, useRefreshAll, useRefreshing } from "@/api/refresh";
import type { Card, Feed, Scope } from "@/api/types";
import { SwipeRow } from "@/gestures/SwipeRow";
import { openRowMenu } from "@/gestures/rowMenu";
import { prefersReducedMotion } from "@/gestures/tracking";
import { COLLAPSE_MS, captureAnchor, compensate, type ScrollAnchor } from "@/lib/collapse";
import { usePullToRefresh } from "@/gestures/usePullToRefresh";
import { pageOf, useResolvedLayout } from "@/layouts";
import { ROW_KEYSHORTCUTS, RowTabStop, STOP_PREFIX, useRowTabIndex } from "@/layouts/rowTabStop";
import type { ListLayout, RowMenuActions } from "@/layouts";
import { resolveList, updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { prefsStore } from "@/lib/prefs";
import { useStore, useStoreSelector } from "@/lib/store";
import { withHeaders, type Row } from "@/lib/format";
import { useSearchHighlight } from "@/lib/useHighlights";
import { onBecameUnread, readIntent, useItemActions } from "@/lib/itemActions";
import { Button } from "@/ui/button";
import { focusOrigin } from "@/ui/kit";
import { articleTo } from "@/lib/routes";
import { FirstRun } from "./FirstRun";
import { announce, toast } from "@/shell/toasts";
import { copyLink } from "@/lib/share";
import { openOriginalAndRecord, shareAndRecord } from "@/lib/statsSender";
import { openFilterEditor, similarSeed } from "@/lib/similar";
import { openFeedEditor } from "@/lib/feedEditor";
import { useWidth } from "@/lib/useWidth";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { failureCopy, failureNoteFeeds, showsFailure } from "@/lib/feedStatus";
import { FeedFailureNote } from "./FeedFailureNote";
import { listPlace, rememberListTop } from "@/lib/listPlace";
import { useItemsSource, type ListSource } from "./listSource";
import { useDigestSource } from "./digestSource";
import { firstLook, keepStill, lookAgain, measurePage, readerScrolling, SCROLLING_MS, type PageLook, type ReaderInput } from "./pageSeen";

/** After a failed mark-read-on-scroll, scrolling sends nothing for this long. */
export const SCROLL_RETRY_MS = 30_000;
/** A failure streak of mark-read-on-scroll: told once, and retried only after a pause. */
const scrollFailure = { streak: false, until: 0 };
/** Tests: forget an earlier failure. */
export function resetScrollReadForTests(): void {
  scrollFailure.streak = false;
  scrollFailure.until = 0;
}

/**
 * Mark-read-on-scroll: send the unread rows that scrolled past, each once. `sent` remembers them while the request
 * is out (so the next settle does not send them twice). A request that fails forgets them again, so a later scroll
 * retries instead of leaving them unread for good, but not at once: every scroll pause would send and fail again,
 * flipping the rows between read and unread. Scrolling sends nothing for SCROLL_RETRY_MS after a failure, and a
 * streak of failures is told once. An expired sign-in is not retried at all: only a reload helps, and the notice at
 * the top says so.
 */
export async function markScrolledPast(qc: QueryClient, passed: Card[], sent: Set<string>, now = Date.now()): Promise<void> {
  if (offlineStore.get().sessionExpired || now < scrollFailure.until) return;
  const ids = passed.filter((i) => !i.read && !sent.has(i.id)).map((i) => i.id);
  if (ids.length === 0) return;
  ids.forEach((id) => sent.add(id));
  let failure: unknown;
  const res = await applyRead(qc, ids, true, "scroll", { onError: (e) => (failure = e) });
  if (res) {
    scrollFailure.streak = false;
    scrollFailure.until = 0;
    return;
  }
  const expired = failure instanceof ApiError && (failure.code === SESSION_EXPIRED || failure.status === 401);
  if (!expired) ids.forEach((id) => sent.delete(id));
  scrollFailure.until = now + SCROLL_RETRY_MS;
  if (!scrollFailure.streak) toast(changeError(failure), "error");
  scrollFailure.streak = true;
}

// Scroll and selection memory per list, so "back" lands where you were
// (design 3.4: one restore path). Module scope: survives route changes.
interface ListMemory {
  offset: number;
  /** The list's `as_of` when `offset` was recorded: a mismatch (checked once the query settles, not only at
   * mount) means the list was refetched since (new arrivals, a resync, mark-all) and `offset` no longer
   * points at the rows it used to. */
  offsetAsOf?: string;
  /** The failing-feed note's height above the rows when `offset` was recorded: the note may be gone or another height
   * when the list comes back, and the rows must land where they were. */
  offsetNoteH?: number;
  /** What the rows' heights were measured for when `offset` was taken (`sizes.sig` below). */
  offsetSig?: string;
  /** Row heights measured on the last visit, and what they were measured for (layout, columns, width bucket, text
   * size). `offset` was taken over these heights: without them the rows above it come back at their estimates, so
   * the same offset lands rows away from where the reader was (#94). */
  sizes?: { sig: string; items: VirtualItem[] };
  /** The article at the top of the list when `offset` was recorded: where the list goes back to when the saved heights
   * no longer apply (the list came back at another width or text size), since `offset` then points at other rows. */
  topId?: string | null;
  selectedId?: string;
  /** A row the list must scroll to when it next mounts (the article reached by paging Next or Previous). */
  reveal?: string;
  /** Rows swiped or marked away in Unread, so leaving the list and coming back keeps them gone. */
  hidden: ReadonlySet<string>;
  /** Ids already sent by mark-read-on-scroll. */
  sentByScroll: Set<string>;
  /** Rows actually rendered in the visible window at some point (not just overscan), so a mark-on-scroll
   * settle after a remount still knows what was genuinely seen before the list was last unmounted. */
  seenByScroll: Set<string>;
}
const MEMORY_CAP = 100;
/** Rows rendered past each end of the visible window. */
const OVERSCAN = 8;
/** How far down a list a remembered place is looked for after a reload, in articles. */
const PLACE_SEARCH_MAX = 1000;
/** The same for the Digest, which loads up to a page of every section it asks for: about ten sections. */
const PLACE_SEARCH_DIGEST_MAX = 200;

/** The article whose row is at the top of the scrolled list (rows are absolutely placed, so the highest one in view). */
function topItemId(scroller: HTMLElement): string | null {
  const top = scroller.getBoundingClientRect().top + 1;
  let best: { id: string; y: number } | null = null;
  for (const row of scroller.querySelectorAll<HTMLElement>("[data-item-id]")) {
    const r = row.getBoundingClientRect();
    if (r.bottom <= top) continue;
    const id = row.dataset.itemId;
    if (id && (!best || r.top < best.y)) best = { id, y: r.top };
  }
  return best?.id ?? null;
}
const memory = new Map<string, ListMemory>();
/** The page load has shown its first list, which is the only one that goes back to a remembered place. */
let pageRestoredPlace = false;
const memoryFor = (key: string): ListMemory => {
  let m = memory.get(key);
  if (!m) {
    m = { offset: 0, hidden: new Set(), sentByScroll: new Set(), seenByScroll: new Set() };
    memory.set(key, m);
    // Bounded: every scope (each partial search string too) adds an entry. Map order is insertion order, so the
    // oldest go first; a touched entry is re-inserted below to stay recent.
    while (memory.size > MEMORY_CAP) {
      const oldest = memory.keys().next().value;
      if (oldest === undefined) break;
      memory.delete(oldest);
    }
  } else {
    memory.delete(key);
    memory.set(key, m);
  }
  return m;
};

// An article that becomes unread again (undo, mark unread) while its list is not mounted must not stay hidden.
onBecameUnread((ids) => {
  for (const m of memory.values()) if (ids.some((id) => m.hidden.has(id))) m.hidden = new Set([...m.hidden].filter((x) => !ids.includes(x)));
});

/** The article reached by Next or Previous is where the list should be when the reader goes back to it. */
export function rememberListPlace(scope: Scope, id: string): void {
  const m = memoryFor(scopeKey(scope));
  m.selectedId = id;
  m.reveal = id;
}

/** Forget remembered scroll and selection (tests). */
export function clearListMemory(): void {
  memory.clear();
  pageRestoredPlace = false;
}

/** How long a row marked read on purpose stays in the Unread list (the undo toast lasts far longer). */
export const LEAVE_MS = 1500;

/** What an empty list says. `feed` is the one feed a feed list shows: a failing feed says why instead of "All caught up" (issue #476). */
export function emptyCopy(scope: Scope, pendingNew = 0, feed?: Feed): { title: string; body: string; health?: boolean } {
  if (scope.q) {
    // Only a search narrowed to a feed or folder has a wider place to try (the chip on the Search screen widens it).
    const narrowed = scope.feed || scope.folder;
    return { title: `No results for "${scope.q}"`, body: narrowed ? "Try fewer words, or search the whole library." : "Try fewer words." };
  }
  if (scope.view === "muted") return { title: "Nothing muted", body: "No filter has muted an article yet." };
  if (scope.view === "starred") return { title: "No starred articles", body: "Star an article to keep it here. Retention never removes starred articles." };
  // The server sends last_error only while the feed is failing. New articles waiting mean a fetch has worked since.
  const failure = feed && showsFailure(scope.view, pendingNew, feed) ? failureCopy(feed) : null;
  if (failure) return { title: failure.title, body: failure.body, health: true };
  if (scope.view === "unread") {
    return pendingNew > 0
      ? { title: "All caught up", body: `${pendingNew} new article${pendingNew === 1 ? "" : "s"} arrived. Load ${pendingNew === 1 ? "it" : "them"} above to continue reading.` }
      : { title: "All caught up", body: "No unread articles. New ones appear after the next refresh." };
  }
  return { title: "No articles yet", body: "Kipple hasn't fetched anything from these feeds yet." };
}

/** What the header can ask the list to do (mark all needs the list's own loaded ids). */
/** Why Mark all is off while a search is still being typed. */
export const FINISH_SEARCH = "Finish your search first (press Enter)";

export interface ListControls {
  markAllRead: () => void;
  /** How many rows are loaded (0 while loading or empty). */
  count: number;
  /** A search that had no exact match and shows partial matches (docs/design.md 2.4). */
  fallback: boolean;
  /** The list finished loading and has no articles at all. */
  empty: boolean;
  /** Focus the list's Tab stop row; false when there is none. */
  focusStop: () => boolean;
}

/** What a search that came back 422 says: the server's own message, or a plain one. */
export function tooBroadMessage(e: unknown): string | null {
  if (!(e instanceof ApiError) || e.status !== 422 || e.code !== "search_too_broad") return null;
  const m = e.body && typeof e.body.message === "string" ? e.body.message : "";
  return m || "That search matches too much. Add a longer or more specific word.";
}

interface Props {
  scope: Scope;
  /** Item open in the reader pane (or route), highlighted in the list. */
  activeId?: string;
  /** Called when what the header (outside the list, which is remounted per scope) can offer changes: the loaded count, the fallback flag, mark all; null once this list is gone. */
  onControls?: (c: ListControls | null) => void;
}

type VRow = Row<Card> | { kind: "group"; key: string; items: Card[] };

/** Group consecutive item rows into `cols`-wide rows (cards grid). */
function chunkRows(rows: Row<Card>[], cols: number): VRow[] {
  if (cols <= 1) return rows;
  const out: VRow[] = [];
  let cur: Card[] = [];
  const flush = () => {
    if (cur.length) out.push({ kind: "group", key: `g:${(cur[0] as Card).id}`, items: cur });
    cur = [];
  };
  for (const r of rows) {
    if (r.kind === "header") {
      flush();
      out.push(r);
    } else {
      cur.push(r.item);
      if (cur.length === cols) flush();
    }
  }
  flush();
  return out;
}

/** Card columns from the list's own width: 1 under 600 px, 2 under 900, else 3. */
function columnsFor(width: number): number {
  return width < 600 ? 1 : width < 900 ? 2 : 3;
}

const isTouch = (): boolean => {
  try {
    return window.matchMedia("(pointer: coarse)").matches;
  } catch {
    return false;
  }
};

export function ListPane({ scope, activeId, onControls }: Props) {
  const key = scopeKey(scope);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const boot = useBootstrap();
  const prefs = useStore(prefsStore);
  const dp = useDevicePrefs();
  const { layout, ctx } = useResolvedLayout(scope);
  // A search keeps its own headers (days, or the one for relevance): the grouping is a setting of the feed and folder lists.
  const group = scope.q ? "day" : resolveList(dp, ctx, "group").value;
  // A page layout (the Gazette) draws the whole loaded list itself: no virtualized rows, and no row ever leaves it.
  const Page = pageOf(layout, scope);
  // Where the articles come from (listSource.ts): the server's list, a page at a time; or, for the Digest, a list for each
  // of its sections (digestSource.ts).
  const digestOn = layout.id === "digest" && !!Page;
  const listSrc = useItemsSource(scope, !!Page, !digestOn);
  const digestSrc = useDigestSource(scope, digestOn);
  const src: ListSource = digestOn ? digestSrc : listSrc;
  // The Digest has nothing to show only when no section is drawn (even one still loading or failed is) and none is left to ask.
  const nothingShown = src.digest ? src.digest.sections.length === 0 && !src.more : src.cards.length === 0;
  const { startOver } = src;
  const pagedRef = useLatestRef(!!Page);
  // Only the pending-new slices: run progress and fetch ticks must not re-render every row.
  const pendingByFeed = useStoreSelector(liveStore, (s) => s.pendingByFeed);
  const pendingIds = useStoreSelector(liveStore, (s) => s.pendingIds);
  const act = useItemActions();
  const refreshAll = useRefreshAll();
  const refreshing = useRefreshing();

  const parentRef = useRef<HTMLDivElement>(null);
  const width = useWidth(parentRef);
  const cols = layout.grid ? columnsFor(width) : 1;

  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => memory.get(key)?.hidden ?? new Set());
  // Rows that are collapsing (COLLAPSE_MS) before they leave the list.
  const [leaving, setLeaving] = useState<ReadonlySet<string>>(() => new Set());
  const loadedIdSet = useMemo(() => new Set(src.cards.map((i) => i.id)), [src.cards]);
  const items = useMemo(() => (hidden.size ? src.cards.filter((i) => !hidden.has(i.id)) : src.cards), [src.cards, hidden]);
  // Relevance order is not chronological: day headers would repeat and mean nothing. One header says what the order is
  // (and keeps the heading levels in sequence for screen readers, as the day headers do elsewhere).
  const rank = scope.order === "rank";
  const rows = useMemo(
    () =>
      chunkRows(
        rank && items.length > 0
          ? [{ kind: "header", key: "h:rank", label: "Best matches first" } as Row<Card>, ...items.map((item): Row<Card> => ({ kind: "item", key: item.id, item }))]
          : withHeaders(items, group),
        cols,
      ),
    [items, cols, rank, group],
  );
  // A search that had no exact match shows partial matches, and the list's own flag goes back to the server when
  // the whole result is marked read, so it marks what was shown.
  const fallback = !!scope.q && src.fallback;
  const markScope = useMemo<Scope>(() => (scope.q ? { ...scope, fallback } : scope), [scope, fallback]);
  useSearchHighlight(scope.q, { fallback, typing: scope.typing });
  // An archived article's feed (the archive feed) is not one of these, so "Manage this feed" says it is gone.
  const feedById = useMemo(() => new Map(visibleFeeds(boot.data?.feeds).map((f) => [f.id, f])), [boot.data]);
  const unreadView = scope.view === "unread" && !scope.q;

  // "Only items present when the list loaded": the highest id the list knew about.
  // The server's own `as_of` for the first page (ids follow arrival, not sort order, so the page maximum is wrong).
  const currentAsOf = src.asOf;
  const asOf = useLatestRef<string | undefined>(currentAsOf);

  const saved = memory.get(key);
  // The first list a page load shows (a reload, the app reopened, Back from an article opened by a reload) goes back to
  // the article that was at its top (lib/listPlace); a list opened later in the visit starts at its top as before.
  // Not a search (its results are not a place to come back to). With an article open beside it, that article is the
  // place: its row is paged in and brought to the top. `id`: the article still to come back to, null once there, given
  // up on, or the reader moved first. `index`: the row scrolled to, until the visible range has reached it. `moved`:
  // the reader has moved the list, so its top is theirs to remember (the list's own scrolling on the way never is).
  const placeable = !scope.q;
  const [place] = useState(() => ({
    id: placeable && !saved && !pageRestoredPlace ? (activeId ?? listPlace(key)) : null,
    index: null as number | null,
    moved: false,
  }));
  useEffect(() => {
    pageRestoredPlace = true;
  }, []);
  const [selectedId, setSelectedId] = useState<string | undefined>(saved?.selectedId);
  const selected = activeId ?? selectedId;
  // The list is one Tab stop (rowTabStop.ts): the row that last had focus, else the selected row, else the first. Its
  // row is always rendered, so Tab into the list finds it wherever the list is scrolled. Opening another article (Next
  // in the reader, say) makes it the selected row again: Shift+Tab from the reader comes back to the open article.
  const [focusId, setFocusId] = useState<string | undefined>(undefined);
  const [stopFor, setStopFor] = useState(activeId);
  if (stopFor !== activeId) {
    setStopFor(activeId);
    setFocusId(undefined);
  }
  const shown = (id: string | undefined, of: readonly Card[]) => (id && of.some((i) => i.id === id) ? id : undefined);
  const tabStopId = shown(focusId, items) ?? shown(selected, items) ?? items[0]?.id;
  const stopIndex = useMemo(
    () => (tabStopId ? rows.findIndex((r) => (r.kind === "item" ? r.item.id === tabStopId : r.kind === "group" && r.items.some((i) => i.id === tabStopId))) : -1),
    [rows, tabStopId],
  );
  const rangeExtractor = useCallback((range: Range) => keepMounted(range, stopIndex), [stopIndex]);

  // The list's measured width and root font size, for layouts whose rows change shape with width (Editorial).
  const sizeCtx = useRef({ width: 0, rem: 16 });
  sizeCtx.current.width = width;
  const noteBoxRef = useRef<HTMLDivElement>(null);
  const [noteH, setNoteH] = useState(0);
  const noteHRef = useRef(0);
  // The heights from the last visit go with its offset only if the offset was taken over them. A list that unmounts as
  // another mounts in its place (the phone and desktop layouts swapping at a turn) saves its heights only after the
  // new one has read them, so the heights can still be from an earlier visit at another width while the offset is
  // the latest: those heights are dropped, and the rows are measured again and put back on the article (listTop).
  const savedSizes = saved?.sizes && saved.sizes.sig === saved.offsetSig ? saved.sizes : undefined;
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: Page ? 0 : rows.length,
    getScrollElement: () => parentRef.current,
    // The failing-feed note above the rows is part of the scrolling content.
    scrollMargin: noteH,
    estimateSize: (i) => {
      const r = rows[i];
      if (!r || r.kind === "header") return 40;
      if (r.kind === "group") return layout.estimateRow(r.items[0] as Card, sizeCtx.current) + 16;
      return layout.estimateRow(r.item, sizeCtx.current);
    },
    getItemKey: (i) => rows[i]?.key ?? i,
    overscan: OVERSCAN,
    rangeExtractor,
    // A page layout restores its own offset once its pages are laid out (below); a jump here would land short.
    initialOffset: Page ? 0 : (saved?.offset ?? 0),
    // The heights from the last visit, so the restored offset points at the rows it was taken over.
    initialMeasurementsCache: savedSizes?.items,
  });

  // `as_of` is a global watermark (it moves whenever any feed anywhere gets new items), and `staleTime:
  // Infinity` means the cache right after mount can still be the stale page from before an invalidation
  // elsewhere (a mute, mark-all-read, a resync) with its own stale `as_of` — so checking the fingerprint only
  // once at mount can restore a pixel offset that is about to be pulled out from under it by the pending
  // refetch. Checking again whenever this scope's own data actually changes catches that: it fires only when
  // this list's rows really did change since the offset was saved, not on every remount. The check only covers
  // that first settling: once the query has no fetch in flight and still has the data the offset was saved
  // against, the offset is known good and the check is switched off, so a later refetch (a resync, a bulk mark)
  // never throws a reader back to the top mid-visit.
  const restoredAsOf = useRef(saved?.offsetAsOf);
  // A page layout's offset waits for its pages (below); a list refetched since then starts at the top instead.
  const pageRestored = useRef(!saved?.offset && !saved?.reveal);
  useEffect(() => {
    if (restoredAsOf.current === undefined) return;
    if (restoredAsOf.current !== currentAsOf) {
      restoredAsOf.current = undefined;
      pageRestored.current = true;
      virtualizer.scrollToOffset(0);
    } else if (!src.fetching) {
      restoredAsOf.current = undefined;
    }
  }, [currentAsOf, virtualizer, src.fetching]);

  // A different layout has different row heights: drop the sizes measured for the old one.
  // The same goes for a width that moves a row across a breakpoint or changes its image height: re-measure per
  // 48 px of width rather than per pixel, so dragging the list wider does not thrash the cache.
  const widthBucket = Math.round(width / 48);
  // The root font size changes with the text-size preference, so it is re-read (and rows re-measured) then too.
  const textSize = prefs.textSize;
  const sizeSig = `${layout.id}|${cols}|${widthBucket}|${textSize}`;
  // What the heights the virtualizer holds were measured for: the ones restored from the last visit, at first.
  const measuredSig = useRef(savedSizes?.sig);
  const unmeasured = width === 0;
  // The article at the top of the list, as the reader left it, and the list width it was taken at. Rows that change
  // shape (a turned phone, a resized window, another text size, while here or while away) keep the list's pixel offset
  // but not what is under it, so the list goes back to this article afterwards: the reader stays where they were, and
  // mark as read while scrolling (which judges rows by whether they are above the top) finds the same rows above the
  // top as before.
  const listTop = useRef<{ id: string | null; width: number }>({ id: saved?.topId ?? null, width: -1 });
  const rowsRef = useLatestRef(rows);
  useEffect(() => {
    try {
      sizeCtx.current.rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
    } catch {
      /* keep the last */
    }
    const el = parentRef.current;
    // Nothing is laid out for real before the list has a width, and heights restored for this same layout, width
    // and text size are still right: dropping them would put the rows above the restored offset back at their
    // estimates and land the reader rows away from where they were.
    if (!unmeasured && measuredSig.current !== sizeSig) {
      measuredSig.current = sizeSig;
      // measure() drops every cached size. Rows already on screen (the list came back from an article with its
      // data cached, so it renders at once) will not report again until they resize, so measure them now or they
      // keep the estimate and overlap the rows below.
      remeasureMounted(virtualizer, el);
      const id = listTop.current.id;
      const i = id ? rowsRef.current.findIndex((r) => (r.kind === "item" ? r.item.id === id : r.kind === "group" && r.items.some((x) => x.id === id))) : -1;
      if (i >= 0 && !Page) {
        // Nothing on the way there counts as seen (the place.index gate below), as after a reload's restore.
        place.index = i;
        virtualizer.scrollToIndex(i, { align: "start" });
      }
    }
    // Only once the rows were laid out for this width (and put back on the article, above) is the top read again.
    if (el && !unmeasured) listTop.current.width = el.clientWidth;
  }, [sizeSig, unmeasured, virtualizer, width, rowsRef, place, Page]);
  // Keep the measured heights for the way back, with what they were measured for.
  useEffect(
    () => () => {
      const sig = measuredSig.current;
      memoryFor(key).sizes = sig ? { sig, items: virtualizer.takeSnapshot() } : undefined;
    },
    [key, virtualizer],
  );

  const rowIndexOf = useCallback(
    (id: string) => rows.findIndex((r) => (r.kind === "item" ? r.item.id === id : r.kind === "group" && r.items.some((i) => i.id === id))),
    [rows],
  );

  // Remember position and selection for the way back.
  useEffect(() => {
    const el = parentRef.current;
    if (!el) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onScroll = () => {
      const m = memoryFor(key);
      m.offset = el.scrollTop;
      m.offsetSig = measuredSig.current;
      m.offsetNoteH = noteHRef.current;
      m.offsetAsOf = asOf.current;
      // And on the device, for the next page load: the article at the top, a few times a second at most, once the
      // reader has moved the list (a restore landing on estimated row heights must not save a neighbour instead).
      timer ??= setTimeout(() => {
        timer = undefined;
        // A list opening an article on a phone leaves the page before this effect's cleanup clears the timer: a detached
        // list reads as scrolled to its top, which would forget the place the reader is leaving.
        if (!el.isConnected) return;
        if (place.moved && placeable) rememberListTop(key, el.scrollTop < 8 ? null : topItemId(el));
      }, 400);
    };
    // The reader moving the list (or the selection, from the keyboard anywhere) before the place came back wins. Only
    // their own input counts: the list itself scrolls while rows are measured and pages arrive.
    const byReader = () => {
      place.id = null;
      place.index = null;
      place.moved = true;
    };
    const INPUT = ["wheel", "touchstart", "pointerdown"] as const;
    el.addEventListener("scroll", onScroll, { passive: true });
    for (const t of INPUT) el.addEventListener(t, byReader, { passive: true });
    document.addEventListener("keydown", byReader, { passive: true });
    return () => {
      if (timer) clearTimeout(timer);
      el.removeEventListener("scroll", onScroll);
      for (const t of INPUT) el.removeEventListener(t, byReader);
      document.removeEventListener("keydown", byReader);
    };
  }, [key, asOf, place, placeable]);
  useEffect(() => {
    memoryFor(key).selectedId = selectedId;
  }, [key, selectedId]);
  // Hidden rows outlive the mount (opening an article on a phone unmounts the list).
  useEffect(() => {
    const m = memoryFor(key);
    m.hidden = hidden;
  }, [key, hidden]);

  // A page layout has no virtualizer to restore the offset: its pages appear once the list has a width and enough
  // articles to plan from, so the offset is put back after the first render that is tall enough to hold it. Any
  // scroll the restore did not make (the reader's, the article pane's previous and next, find in page) wins: a restore
  // still waiting must not jump the page later, when more pages load (a page that came back shorter, after a rotation
  // or a text size change, never reaches the offset).
  /** Where the restore put the page, so its own scroll event is told apart from every other. */
  const restoreTarget = useRef<number | null>(null);
  // A page is looked at after every scroll and every change in its size (pageSeen.ts): to keep the story at the top
  // still when the page changes under the reader, and, with mark as read while scrolling on, to find the stories a
  // scroll carried from in view to above the top. Those start empty on every mount and again whenever a restore puts
  // the page somewhere the reader did not scroll to.
  const pagePassed = useRef(new Set<string>());
  const markOnScrollPage = prefs.markReadOnScroll && scope.view !== "starred" && !!Page; // never a search: no Page there
  // Written in render on purpose (see markOnScrollRef below): an effect cleanup reads the new value of the setting.
  const markOnScrollPageRef = useRef(markOnScrollPage);
  markOnScrollPageRef.current = markOnScrollPage;
  /** The last look at the stories; null before the first. */
  const pageLook = useRef<PageLook | null>(null);
  /** How far the story at the top was moved while the reader was scrolling, to scroll once they stop (keepStill). */
  const pageOwed = useRef(0);
  /** The reader is scrolling the page: a scroll they made in the last SCROLLING_MS, or a finger or button down on it. */
  const pageScrolling = useRef<ReaderInput>({ at: -Infinity, touch: false, pointer: false });
  /** Start again from what is on screen: nothing is passed, and the next look is judged against this one. */
  const lookFromHere = useCallback((el: HTMLElement) => {
    pagePassed.current.clear();
    pageOwed.current = 0;
    pageLook.current = firstLook(measurePage(el));
  }, []);
  /** The scrolled content (the failing-feed note and the pages), which the pull-to-refresh offset moves. */
  const contentRef = useRef<HTMLDivElement | null>(null);
  /** Where the list last scrolled itself to keep the story at the top still, so that scroll is not taken for the reader's. */
  const keptAt = useRef<number | null>(null);
  /**
   * Look at the stories (after a scroll, or when the pages change size) and judge each against the last look. Both the
   * scroll's look and the ResizeObserver's look this way, so the order in which the browser reports them does not
   * matter: a story the page moved is never taken for one the reader scrolled past. The story at the top is kept still
   * (keepStill), the way the browser's scroll anchoring would, except that this look knows the scroll it made and counts
   * only the rest as the reader's. Not while a pull to refresh holds the content down, and not while the reader is
   * scrolling: what is owed then is scrolled by the first look after they stop.
   */
  const lookAtStories = useCallback((el: HTMLElement) => {
    const was = pageLook.current;
    let m = measurePage(el);
    if (!was) return void (pageLook.current = firstLook(m));
    const passed = markOnScrollPageRef.current ? pagePassed.current : new Set<string>();
    const by = contentRef.current?.style.transform ? 0 : keepStill(was, m, pageOwed.current);
    if (Math.abs(by) < 1 || readerScrolling(pageScrolling.current)) {
      pageOwed.current = by;
      pageLook.current = lookAgain(was, m, passed);
      return;
    }
    const before = m.scrollTop;
    el.scrollTop = before + by;
    // A scroll the browser clamped to where the list already was fires no scroll event to clear this.
    keptAt.current = el.scrollTop === before ? null : el.scrollTop;
    pageOwed.current = 0;
    m = measurePage(el);
    pageLook.current = lookAgain({ ...was, scrollTop: was.scrollTop + m.scrollTop - before }, m, passed);
  }, []);
  useLayoutEffect(() => {
    const el = parentRef.current;
    if (!Page || pageRestored.current || !el) return;
    // Paging in the article moved the place to read: land on that story instead of the old offset. The stories exist
    // only once the pages are laid out, so until then the reveal stays pending (and this runs again each render).
    const reveal = saved?.reveal;
    if (reveal) {
      const story = el.querySelector(`[data-item-id="${CSS.escape(reveal)}"]`);
      if (!story) return;
      if (saved) saved.reveal = undefined;
      pageRestored.current = true;
      story.scrollIntoView?.({ block: "center" });
      restoreTarget.current = el.scrollTop;
      lookFromHere(el);
      return;
    }
    if (el.scrollHeight - el.clientHeight < (saved?.offset ?? 0)) return;
    pageRestored.current = true;
    el.scrollTop = saved?.offset ?? 0;
    restoreTarget.current = el.scrollTop;
    lookFromHere(el);
  });
  // Turning the setting on or off (here, or from another device) starts again too: nothing found while it was off,
  // or before it was turned off with a settle still pending, is marked later.
  useLayoutEffect(() => {
    pagePassed.current.clear();
    pageLook.current = null;
    const el = parentRef.current;
    if (el && pageRestored.current) lookFromHere(el);
  }, [markOnScrollPage, lookFromHere]);
  useEffect(() => {
    const el = parentRef.current;
    if (!Page || !el) return;
    let frame = 0;
    let stopped: ReturnType<typeof setTimeout> | undefined;
    const input = pageScrolling.current;
    // Once the reader stops scrolling, a look scrolls what the looks while they scrolled owed.
    const whenStopped = () => {
      if (stopped) clearTimeout(stopped);
      stopped = setTimeout(() => {
        stopped = undefined;
        // A finger or button still down: its release comes back here.
        if (input.touch || input.pointer) return;
        if (readerScrolling(input)) return void whenStopped();
        if (pageOwed.current && pageRestored.current && el.isConnected) lookAtStories(el);
      }, SCROLLING_MS);
    };
    const onScroll = () => {
      if (restoreTarget.current !== null && el.scrollTop === restoreTarget.current) {
        restoreTarget.current = null;
        return;
      }
      restoreTarget.current = null;
      pageRestored.current = true;
      if (keptAt.current !== null && el.scrollTop === keptAt.current) keptAt.current = null;
      else {
        keptAt.current = null;
        input.at = performance.now();
        whenStopped();
      }
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        lookAtStories(el);
      });
    };
    const hold = (kind: "touch" | "pointer", down: boolean) => () => {
      input[kind] = down;
      if (!down) whenStopped();
    };
    const listeners: [EventTarget, string, () => void][] = [
      [el, "touchstart", hold("touch", true)],
      [el, "pointerdown", hold("pointer", true)],
      [window, "touchend", hold("touch", false)],
      [window, "touchcancel", hold("touch", false)],
      [window, "pointerup", hold("pointer", false)],
      [window, "pointercancel", hold("pointer", false)],
    ];
    el.addEventListener("scroll", onScroll, { passive: true });
    for (const [target, type, fn] of listeners) target.addEventListener(type, fn, { passive: true });
    return () => {
      if (frame) cancelAnimationFrame(frame);
      if (stopped) clearTimeout(stopped);
      el.removeEventListener("scroll", onScroll);
      for (const [target, type, fn] of listeners) target.removeEventListener(type, fn);
      input.touch = input.pointer = false;
    };
  }, [Page, lookAtStories]);
  // A change in the size of the scrolled content with no scroll after it (a new plan, more pages, another width, a text
  // size change, a picture that failed, the failing-feed note coming or going, the page taking the place of rows) is
  // looked at here too (lookAtStories), before it is painted. Each story is watched as well as the content: a story
  // that changes height in a column shorter than the tallest moves the stories under it and leaves the content's size
  // as it was. Not while a restore is pending: the stories at the top are only passing through.
  const storyObserver = useRef<{ observer: ResizeObserver; watched: Set<Element> } | null>(null);
  useLayoutEffect(() => {
    const box = contentRef.current;
    if (!Page || !box) return;
    const observer = new ResizeObserver(() => {
      const el = parentRef.current;
      if (el && pageRestored.current) lookAtStories(el);
    });
    observer.observe(box);
    storyObserver.current = { observer, watched: new Set() };
    return () => {
      observer.disconnect();
      storyObserver.current = null;
    };
  }, [Page, lookAtStories]);
  // The stories on the page change with every plan and every page that loads.
  useLayoutEffect(() => {
    const o = storyObserver.current;
    const box = contentRef.current;
    if (!o || !box) return;
    const now = new Set(box.querySelectorAll("article[data-item-id]"));
    for (const a of o.watched) if (!now.has(a)) o.observer.unobserve(a);
    for (const a of now) if (!o.watched.has(a)) o.observer.observe(a);
    o.watched = now;
  });

  // Back to the remembered place after a reload: once its article is loaded, scroll it to the top. Pages are fetched
  // until it turns up (at most PLACE_SEARCH_MAX articles); a list without it any more starts at the top and forgets it.
  useLayoutEffect(() => {
    const id = place.id;
    const el = parentRef.current;
    if (!id || !el) return;
    if (Page) {
      const story = el.querySelector(`[data-item-id="${CSS.escape(id)}"]`);
      if (story) {
        place.id = null;
        story.scrollIntoView?.({ block: "start" });
        restoreTarget.current = el.scrollTop;
        lookFromHere(el);
        return;
      }
      // Loaded but not laid out yet: the pages come in a later render.
      if (src.cards.some((i) => i.id === id)) return;
      // The Digest shows a few of a section's articles: one it holds but does not show is not found by loading more.
      if (digestOn && digestSrc.loadedCards.some((i) => i.id === id)) {
        place.id = null;
        rememberListTop(key, null);
        return;
      }
    } else {
      const i = rowIndexOf(id);
      if (i >= 0) {
        place.id = null;
        place.index = i;
        virtualizer.scrollToIndex(i, { align: "start" });
        return;
      }
    }
    if (src.pending || src.fetching) return;
    // The Digest asks for sections, not pages: it looks through a few, never every one.
    const searched = digestOn ? digestSrc.loadedCards.length : src.cards.length;
    if (src.more && !src.loadMoreFailed && searched < (digestOn ? PLACE_SEARCH_DIGEST_MAX : PLACE_SEARCH_MAX)) void src.loadMore();
    else {
      place.id = null;
      rememberListTop(key, null);
    }
  });

  // Restore focus to the anchor row when returning to the list.
  const restoredFocus = useRef(false);
  useEffect(() => {
    if (restoredFocus.current || !saved?.selectedId || items.length === 0) return;
    restoredFocus.current = true;
    // Paging Next or Previous in the article moved the place to read: come back to that row, not where the list was
    // when the first article opened.
    // A page layout does this in its own layout effect, once its stories are laid out.
    const reveal = saved.reveal;
    if (reveal && !pagedRef.current) {
      saved.reveal = undefined;
      const i = rowIndexOf(reveal);
      if (i >= 0) virtualizer.scrollToIndex(i, { align: "center" });
    }
    requestAnimationFrame(() => focusRow(parentRef.current, saved.selectedId as string));
  }, [items.length, saved?.selectedId, rowIndexOf, virtualizer, saved]);

  // The reader pane (or the arrow keys in it) changed the open item: keep it in view.
  const lastActive = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!activeId || lastActive.current === activeId) return;
    lastActive.current = activeId;
    setFocusId(undefined); // the open article is the Tab stop again, not a row focused before it changed
    if (pagedRef.current) return storyIntoView(parentRef.current, activeId);
    const i = rowIndexOf(activeId);
    if (i >= 0) virtualizer.scrollToIndex(i, { align: "auto" });
  }, [activeId, rowIndexOf, virtualizer]);

  // Infinite scroll: fetch the next page when the tail comes into view. A page layout has no virtual rows: a marker
  // after its last page says when the end is within reach (and stays in reach while the first page is being planned).
  const [nearEnd, setNearEnd] = useState(false);
  const endObserver = useRef<IntersectionObserver | null>(null);
  const endRef = useCallback((el: HTMLDivElement | null) => {
    endObserver.current?.disconnect();
    endObserver.current = null;
    if (!el) return setNearEnd(false);
    endObserver.current = new IntersectionObserver(([e]) => setNearEnd(!!e?.isIntersecting), {
      root: parentRef.current,
      rootMargin: "0px 0px 1500px 0px",
    });
    endObserver.current.observe(el);
  }, []);
  const virtualItems = virtualizer.getVirtualItems();
  // The last row the window and its overscan reach (not the tab-stop row rangeExtractor adds, which can be far below).
  const lastIndex = windowReach(virtualizer.range, rows.length);
  useEffect(() => {
    // After a failed page the auto-fetch stops (offline would retry every render); the inline Retry row resumes it.
    // Not while the list itself is being refetched (an on-mount refetch of an invalidated list): fetchNextPage would
    // cancel it and append a page to the stale rows, leaving both the stale rows and a restored offset in place.
    const nearTail = Page ? nearEnd : rows.length > 0 && lastIndex >= rows.length - 10;
    if (src.more && !src.fetching && !src.loadMoreFailed && nearTail) void src.loadMore();
  }, [lastIndex, rows.length, src, Page, nearEnd]);

  // The selection is committed to memory before the route changes: on a phone the list unmounts in the
  // same render, before any effect would have saved it, and "back" must land on this row.
  const openItem = useCallback(
    (item: Card) => {
      memoryFor(key).selectedId = item.id;
      setSelectedId(item.id);
    },
    [key],
  );

  // "Mark as read while scrolling" (Accessibility, off by default): rows that have scrolled past the top of
  // the list are marked read once scrolling settles. Goes through mark-read with reason "scroll": no stats,
  // no undo toast, and never on Starred or search.
  const markOnScroll = prefs.markReadOnScroll && scope.view !== "starred" && !scope.q;
  // Written in render on purpose, unlike the other latest-value refs here: the effect below reads it in its cleanup, which
  // runs before any layout effect of the render that switched the setting, and needs the NEW value (see "turning the
  // setting off"). The only reader is that effect and its timer, so a discarded render cannot leave it wrong for long.
  const markOnScrollRef = useRef(markOnScroll);
  markOnScrollRef.current = markOnScroll;
  const sentByScroll = useRef(memoryFor(key).sentByScroll);
  // Rows actually rendered in the visible window at some point (not the overscan rows outside it), so a
  // restored/jumped-to offset that puts rows "above the start index" without them ever having been on screen
  // does not count as scrolled past. Persisted in ListMemory (like sentByScroll) so a quick unmount/remount
  // (opening an article on a phone) does not forget rows that really were seen just before.
  const seenIds = useRef(memoryFor(key).seenByScroll);
  useEffect(() => {
    const r = virtualizer.range;
    // The top rows shown while the place is still coming back were not read there: the list is about to move past them.
    // Nothing counts until the visible range has reached the row the restore scrolled to. Nor while the list has a
    // width its rows were not laid out for (listTop): the range is then over rows that moved, not rows on screen.
    const el = parentRef.current;
    const laidOutFor = listTop.current.width;
    if (!r || place.id || (el && laidOutFor >= 0 && el.clientWidth !== laidOutFor)) return;
    if (place.index !== null) {
      if (place.index < r.startIndex || place.index > r.endIndex) return;
      place.index = null;
    }
    for (let i = r.startIndex; i <= r.endIndex; i++) {
      const row = rowsRef.current[i];
      if (!row) continue;
      if (row.kind === "item") seenIds.current.add(row.item.id);
      else if (row.kind === "group") for (const it of row.items) seenIds.current.add(it.id);
    }
  }, [virtualItems, virtualizer, width]);
  // The article at the top, for rows that change shape (listTop above), read once the rows on screen are measured. Not
  // while the list has a width it was not rendered for: the rows have already moved under the offset, so the article
  // there now is not the one the reader saw.
  useEffect(() => {
    const el = parentRef.current;
    if (Page || !el || el.clientWidth !== listTop.current.width) return;
    // A row scrolled to within a pixel of the top is gone, as the reader sees it: a row's start can be fractional where the
    // browser rounds the scroll position (a restore aligned on it, another device pixel ratio), and reading the row at
    // the rounded offset would take the one above.
    const top = el.scrollTop < 8 ? undefined : virtualizer.getVirtualItemForOffset(el.scrollTop + 1);
    // A day header at the top stands for the article under it.
    const row = top ? rowsRef.current.slice(top.index).find((r) => r.kind !== "header") : undefined;
    listTop.current.id = row?.kind === "item" ? row.item.id : row?.kind === "group" ? (row.items[0]?.id ?? null) : null;
    memoryFor(key).topId = listTop.current.id;
  }, [virtualItems, virtualizer, Page, key, rowsRef]);
  // A page layout has no virtual range: its stories are judged by their own boxes (pageSeen.ts and pagePassed above),
  // so a story beside one still on screen, in another column, is judged alone.
  const pageItemsRef = useLatestRef(src.cards);
  const pagedScroll = !!Page;
  // A layout effect, so its cleanup on unmount (a phone opening a story) still finds the stories' boxes and marks
  // what was scrolled past in the last moments, as rows do.
  useLayoutEffect(() => {
    const el = parentRef.current;
    if (!markOnScroll || !el) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    // `settled`: scrolling stopped and the page is the one on screen, so the stories are judged where they are now. On
    // a cleanup (the list closing, or `c` putting rows in the page's place) only what the scroll frames already found
    // passed is marked: the boxes may be gone or belong to another layout.
    const flush = (settled: boolean) => {
      if (pagedScroll) {
        if (settled && pageRestored.current && el.isConnected) lookAtStories(el);
        const byId = new Map(pageItemsRef.current.map((i) => [i.id, i]));
        const passed = [...pagePassed.current].flatMap((id) => byId.get(id) ?? []);
        pagePassed.current.clear();
        void markScrolledPast(qc, passed, sentByScroll.current);
        return;
      }
      const start = virtualizer.range?.startIndex ?? 0;
      const passed = rowsRef.current
        .slice(0, start)
        .flatMap((r) => (r.kind === "item" ? [r.item] : r.kind === "group" ? r.items : []))
        .filter((i) => seenIds.current.has(i.id));
      void markScrolledPast(qc, passed, sentByScroll.current);
    };
    const onScroll = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = undefined; // settled: nothing is left for the cleanup to flush
        flush(true);
      }, 700);
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      // A pending settle is flushed rather than dropped: unmounting (opening an article on a phone) must not
      // lose rows that were genuinely scrolled past just before.
      if (timer) {
        clearTimeout(timer);
        // Not when the setting was just switched off: this cleanup also runs then, and rows must not be
        // marked read after the reader turned the feature off.
        if (markOnScrollRef.current) flush(false);
      }
      el.removeEventListener("scroll", onScroll);
    };
  }, [markOnScroll, qc, virtualizer, pagedScroll, lookAtStories]);

  // ArrowUp and ArrowDown move focus between rows from any control in a row (its link, star or menu button), Home and
  // End to the first and last loaded row, PageUp and PageDown by a screenful of rows; they only move focus: they select
  // nothing and open nothing, so looking through the list marks nothing read. The list is one Tab stop (and Safari's
  // Tab skips links), so these keys are how the keyboard reaches the other rows. Space on a row's link opens it, as Enter
  // does (a link alone would scroll the page). Taken in the capture phase so a closed menu button does not turn
  // ArrowDown into "open the menu" (Enter and Space still do). Left alone: text fields, an open menu button, and
  // anything inside an open menu or dialog (those are not in a row).
  const onRowKey = (e: React.KeyboardEvent) => {
    if (!ROW_KEYS.has(e.key)) return;
    if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || e.isDefaultPrevented()) return;
    const target = e.target instanceof HTMLElement ? e.target : null;
    const row = target?.closest<HTMLElement>("[data-item-id],[data-stop-id]") ?? null;
    if (!target || !row) return;
    if (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName)) return;
    if (target.getAttribute("aria-expanded") === "true") return;
    if (target.closest('[role="menu"],[role="dialog"]')) return;
    if (e.key === " ") {
      if (target.tagName !== "A") return; // a row's buttons take Space themselves
      e.preventDefault();
      target.click();
      return;
    }
    // A page's keys visit its stories and its buttons (the Digest's "N more") in the order they are drawn.
    const order = Page ? pageOrder(parentRef.current) : items.map((i) => i.id);
    const at = order.indexOf(row.dataset.itemId ?? row.dataset.stopId ?? "");
    const page = Math.max(1, Math.floor((parentRef.current?.clientHeight ?? 0) / Math.max(1, row.offsetHeight))) * cols;
    const last = order.length - 1;
    const to =
      e.key === "ArrowDown" ? at + 1
      : e.key === "ArrowUp" ? at - 1
      : e.key === "Home" ? 0
      : e.key === "End" ? last
      : Math.min(last, Math.max(0, at + (e.key === "PageDown" ? page : -page)));
    const next = order[to];
    e.preventDefault();
    e.stopPropagation();
    if (!next || to === at) return;
    if (!next.startsWith(STOP_PREFIX)) setFocusId(next);
    pageRestored.current = true; // moving by key is the reader moving the page
    if (Page) storyIntoView(parentRef.current, next);
    else virtualizer.scrollToIndex(rowIndexOf(next), { align: "auto" });
    requestAnimationFrame(() => requestAnimationFrame(() => focusRow(parentRef.current, next)));
  };

  /** The row that takes focus in the list (its own focus, or one of its controls) becomes the list's Tab stop. */
  const onListFocus = (e: React.FocusEvent) => {
    const id = e.target instanceof HTMLElement ? e.target.closest<HTMLElement>("[data-item-id],[data-muted-for]") : null;
    const rowId = id?.dataset.itemId ?? id?.dataset.mutedFor;
    if (rowId) setFocusId(rowId);
  };

  // ---- actions -----------------------------------------------------------

  const colsRef = useLatestRef(cols);
  const pendingAnchor = useRef<ScrollAnchor | null>(null);
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());
  useEffect(() => {
    const t = timers.current;
    return () => t.forEach(clearTimeout);
  }, []);

  // Read at commit time by hide(), which runs from timers.
  const allItemsRef = useLatestRef(src.cards);
  const selectionRef = useLatestRef({ selectedId, activeId });

  /** Take rows out of the list: collapse them, then remove; the still-visible rows stay put on screen. */
  const hide = useCallback((ids: string[]): (() => void) => {
    // A page layout keeps every story where it was planned: one marked read fades in place instead.
    if (pagedRef.current) return () => {};
    const commit = () => {
      const el = parentRef.current;
      pendingAnchor.current = el && el.scrollTop > 0 ? captureAnchor(el, ids) : null;
      // The selected row is leaving (marked read in Unread): the selection moves to the row that takes its place, or
      // the one before it at the end, so focus lands on that row when you return to the list.
      const all = allItemsRef.current;
      const gone = memoryFor(key).hidden;
      const stays = (i: Card) => !gone.has(i.id) && !ids.includes(i.id);
      /** The row that takes the place of a leaving one, or the one before it at the end. */
      const after = (id: string) => {
        const at = all.findIndex((i) => i.id === id);
        return at < 0 ? undefined : (all.slice(at + 1).find(stays) ?? all.slice(0, at).reverse().find(stays));
      };
      const { selectedId: sel, activeId: open } = selectionRef.current;
      if (sel && !open && ids.includes(sel)) {
        const next = after(sel);
        if (next) {
          memoryFor(key).selectedId = next.id;
          setSelectedId(next.id);
        }
      }
      setHidden((h) => new Set([...h, ...ids]));
      setLeaving((l) => new Set([...l].filter((x) => !ids.includes(x))));
    };
    let timer: ReturnType<typeof setTimeout> | undefined;
    // Recorded now, not after the collapse: leaving the list mid-animation must not bring the rows back.
    const m = memoryFor(key);
    m.hidden = new Set([...m.hidden, ...ids]);
    if (prefersReducedMotion() || colsRef.current > 1) commit();
    else {
      setLeaving((l) => new Set([...l, ...ids]));
      timer = setTimeout(commit, COLLAPSE_MS);
      timers.current.add(timer);
    }
    return () => {
      if (timer) clearTimeout(timer);
      const m2 = memoryFor(key);
      m2.hidden = new Set([...m2.hidden].filter((x) => !ids.includes(x)));
      setLeaving((l) => new Set([...l].filter((x) => !ids.includes(x))));
      setHidden((h) => new Set([...h].filter((x) => !ids.includes(x))));
    };
  }, [key]);

  /** Bring some hidden rows back (the server did not mark them). */
  const unhide = useCallback((ids: string[]) => {
    const m = memoryFor(key);
    m.hidden = new Set([...m.hidden].filter((x) => !ids.includes(x)));
    setLeaving((l) => new Set([...l].filter((x) => !ids.includes(x))));
    setHidden((h) => new Set([...h].filter((x) => !ids.includes(x))));
  }, [key]);

  // Coming back to a list: rows remembered as hidden that are loaded, unread and not marked read on purpose are
  // stale (undone or marked unread while this list was not mounted), so they come back. Once per mount.
  const reconciled = useRef(false);
  useEffect(() => {
    if (reconciled.current || src.cards.length === 0) return;
    reconciled.current = true;
    const stale = src.cards.filter((i) => hidden.has(i.id) && !i.read && !readIntent.get().has(i.id)).map((i) => i.id);
    if (stale.length) unhide(stale);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [src.cards]);

  // A row that has focus and leaves the list (marked read in Unread, muted, whatever took it out) hands focus to the row
  // that takes its place, or the one before it at the end, instead of dropping it to the page, where the next Tab would
  // start over. Only focus that is in that row moves: its link or a control, its Muted strip, or a menu opened from it with
  // the keyboard. A dialog opened from the row (Mute similar…) gives focus back to the row before it takes the row out
  // (FilterEditor's onClosed), so it lands here too. Focus anywhere else (the open article, the page) stays where it is.
  // Where focus is gets read while rendering, before this render's commit takes the row out of the page.
  const shownIds = useMemo(() => (Page ? src.cards : items).map((i) => i.id), [Page, src.cards, items]);
  const holder = focusOrigin(document.activeElement)?.closest<HTMLElement>("[data-item-id],[data-muted-for]");
  const held = holder && parentRef.current?.contains(holder) ? (holder.dataset.itemId ?? holder.dataset.mutedFor) : undefined;
  const shownBefore = useRef(shownIds);
  useLayoutEffect(() => {
    const before = shownBefore.current;
    shownBefore.current = shownIds;
    if (!held || shownIds.includes(held) || !before.includes(held)) return;
    const now = new Set(shownIds);
    const at = before.indexOf(held);
    const next = before.slice(at + 1).find((id) => now.has(id)) ?? before.slice(0, at).reverse().find((id) => now.has(id));
    if (next) {
      setFocusId(next);
      requestAnimationFrame(() => requestAnimationFrame(() => focusRow(parentRef.current, next)));
    } else requestAnimationFrame(() => parentRef.current?.focus({ preventScroll: true }));
  }, [shownIds, held]);

  // After rows are removed, put the first visible row back where it was on screen.
  useLayoutEffect(() => {
    const a = pendingAnchor.current;
    pendingAnchor.current = null;
    if (a && parentRef.current) compensate(parentRef.current, a);
  }, [hidden]);

  /** Swipe right, `m` in the menu: toggle read. Unread view: a row that became read leaves at once. */
  const mutedView = scope.view === "muted";
  /** Muted list: bring an article back (un-mutes it and puts back the read state it had before) and take its row out. */
  const restoreRow = useCallback(
    (item: Card) => {
      void act.restoreMuted([item], hide([item.id]));
    },
    [act, hide],
  );
  const swipeRead = useCallback(
    (item: Card) => {
      if (mutedView) return restoreRow(item);
      const leaves = unreadView && !item.read;
      void act.toggleRead(item, "swipe", leaves ? hide([item.id]) : undefined);
    },
    [act, hide, unreadView, mutedView, restoreRow],
  );

  // Marked read on purpose (button, key or menu) in the Unread list: the row leaves after LEAVE_MS, with the undo
  // toast still up. The article open beside the list leaves when you move off it instead. Undo, or marking it
  // unread again, cancels this and brings the row back.
  const intent = useStore(readIntent);
  const pendingLeave = useRef(new Map<string, { timer?: ReturnType<typeof setTimeout>; waiting: boolean; undoHide?: () => void }>());
  const activeRef = useLatestRef(activeId);
  const idsRef = useLatestRef<ReadonlySet<string>>(loadedIdSet);
  useEffect(() => {
    const pend = pendingLeave.current;
    if (!unreadView) return;
    for (const id of intent) {
      if (pend.has(id) || !idsRef.current.has(id)) continue;
      const entry: { timer?: ReturnType<typeof setTimeout>; waiting: boolean; undoHide?: () => void } = { waiting: id === activeRef.current };
      pend.set(id, entry);
      if (!entry.waiting) {
        entry.timer = setTimeout(() => {
          entry.timer = undefined;
          entry.undoHide = hide([id]);
        }, LEAVE_MS);
      }
    }
    for (const [id, e] of pend) {
      if (intent.has(id)) continue;
      // Undone or marked unread again: the row stays (or comes back).
      if (e.timer) clearTimeout(e.timer);
      e.undoHide?.();
      pend.delete(id);
    }
  }, [intent, unreadView, hide, loadedIdSet]);
  // Moving off the open article lets it go.
  useEffect(() => {
    for (const [id, e] of pendingLeave.current) {
      if (e.waiting && id !== activeId) {
        e.waiting = false;
        e.undoHide = hide([id]);
      }
    }
  }, [activeId, hide]);
  // Leaving the list (a phone opens an article in a new screen) must not bring back rows that were about to go.
  useEffect(() => {
    const pend = pendingLeave.current;
    return () => {
      const m = memoryFor(key);
      for (const [id, e] of pend) {
        if (e.timer) clearTimeout(e.timer);
        if (!e.undoHide && !e.waiting) m.hidden = new Set([...m.hidden, id]);
      }
      pend.clear();
    };
  }, [key]);

  // Read through refs so a cache patch (a mark-read) does not change `range`, and with it every row's menu actions.
  const srcRef = useLatestRef(src);
  const hiddenRef = useLatestRef(hidden);
  const range = useCallback(
    (item: Card, side: "above" | "below") => {
      if (scope.view === "muted") return; // nothing to mark in the muted list: those articles are already read
      if (rank || scope.typing) return; // relevance order has no above or below, and a search being typed is not a set the server can mark
      // The source says how far a side reaches, or why it reaches nothing (a page whose reading order is not date order).
      const reach = srcRef.current.rangeOf(item);
      if ("why" in reach) return announce(reach.why);
      // The server marks a side within one list: the section's, for the Digest.
      const within = reach.scope ?? markScope;
      const gone = hiddenRef.current;
      const list = gone.size ? reach.cards.filter((i) => !gone.has(i.id)) : reach.cards;
      const at = list.findIndex((i) => i.id === item.id);
      if (at < 0) return;
      const part = side === "above" ? list.slice(0, at) : list.slice(at + 1);
      const local = part.filter((i) => !i.read).map((i) => i.id);
      const restore = unreadView && local.length ? hide(local) : undefined;
      void act.markSide({ scope: within, order: within.order === "oldest" ? "oldest" : "date", side, anchor: item, maxId: reach.asOf ?? asOf.current }, local, restore, unhide);
    },
    [srcRef, hiddenRef, act, hide, unhide, scope, markScope, unreadView, rank],
  );

  const markAllRead = useCallback(() => {
    if (scope.view === "muted") return;
    if (scope.typing) {
      // The key and the button must not do nothing in silence.
      announce(FINISH_SEARCH);
      return;
    }
    // A list of lists (the Digest) marks each loaded one within its own bound; any other is one list.
    const parts = srcRef.current.markAllParts?.() ?? [{ scope: markScope, asOf: asOf.current, cards: items }];
    for (const part of parts) {
      const local = part.cards.filter((i) => !i.read).map((i) => i.id);
      const restore = unreadView && local.length ? hide(local) : undefined;
      void act.markAll(part.scope, part.asOf, local, restore, unhide);
    }
  }, [act, hide, unhide, items, scope, markScope, unreadView, srcRef]);

  const menuActions: RowMenuActions = useMemo(
    () => ({
      toggleRead: (item) => void act.toggleRead(item, "key"),
      toggleStar: (item) => void act.toggleStar(item),
      markAbove: (item) => range(item, "above"),
      markBelow: (item) => range(item, "below"),
      openOriginal: (item) => void openOriginalAndRecord(item),
      copyLink: (item) => void copyLink(item.url),
      share: (item) => void shareAndRecord(item),
      muteSimilar: (item) => openFilterEditor({ mode: "create", seed: similarSeed(item, feedById.get(item.feed_id)?.title) }),
      restore: restoreRow,
      editRule: (item) => item.muted_by && openFilterEditor({ mode: "edit", id: item.muted_by }),
      manageFeed: (item) => (feedById.has(item.feed_id) ? openFeedEditor(item.feed_id) : toast("This feed no longer exists.", "error")),
      noRange: rank || !!scope.typing,
    }),
    [act, range, restoreRow, feedById, rank, scope.typing],
  );

  // ---- pull to refresh ---------------------------------------------------

  const [holding, setHolding] = useState(false);
  const startRefresh = useCallback(() => {
    if (typeof navigator !== "undefined" && navigator.onLine === false) {
      announce(CANT_REFRESH_OFFLINE);
      return;
    }
    setHolding(true);
    refreshAll.mutate();
  }, [refreshAll]);
  // A cursor from before a server upgrade (or one for another ordering) is refused with 400 bad_cursor on a later
  // page: start the list over instead of leaving "Couldn't load more" that can never succeed. At most twice.
  const restarts = useRef(0);
  useEffect(() => {
    if (!src.loadMoreFailed) return;
    const e = src.error;
    if (!(e instanceof ApiError) || e.status !== 400 || e.code !== "bad_cursor" || restarts.current >= 2) return;
    restarts.current++;
    announce(scope.q ? "Search restarted" : "List restarted");
    void startOver();
  }, [src.loadMoreFailed, src.error, scope, startOver]);

  const pull = usePullToRefresh(parentRef, { enabled: !scope.q, onRefresh: startRefresh });
  // Hold the spinner until the run has finished (or a second after the request settles with nothing running).
  useEffect(() => {
    if (!holding || refreshing) return;
    const h = setTimeout(() => setHolding(false), 1000);
    return () => clearTimeout(h);
  }, [holding, refreshing]);

  // ---- first-run peek ----------------------------------------------------

  const firstItemId = items[0]?.id;
  const [peekOn, setPeekOn] = useState(false);
  const [caption, setCaption] = useState(false);
  useEffect(() => {
    // The swipe tip shows on rows; a page layout has none.
    if (dp.peekSeen || peekOn || !firstItemId || cols > 1 || Page || !isTouch()) return;
    setPeekOn(true);
    setCaption(true);
  }, [dp.peekSeen, peekOn, firstItemId, cols, Page]);
  useEffect(() => {
    if (!caption || peekOn) return;
    const h = setTimeout(() => setCaption(false), 4000);
    return () => clearTimeout(h);
  }, [caption, peekOn]);
  const endPeek = useCallback(() => {
    setPeekOn(false);
    updateDevicePrefs({ peekSeen: true });
  }, []);

  // ---- render ------------------------------------------------------------

  // Items the list already holds are not "new" to it, whatever fetched them (a later load, another route).
  const loadedIds = useMemo(() => new Set(src.cards.map((i) => i.id)), [src.cards]);
  const pendingNew = pendingFor(pendingByFeed, scope, boot.data?.feeds ?? [], boot.data?.folders ?? [], { ids: loadedIds, pendingIds });
  const loadNew = () => {
    // Only what this list showed is now loaded; other feeds' arrivals keep counting for other lists.
    liveStore.set((s) => {
      const left = clearPending(s.pendingByFeed, scope, boot.data?.feeds ?? [], boot.data?.folders ?? []);
      return { ...s, pendingByFeed: left, pendingIds: Object.fromEntries(Object.entries(s.pendingIds).filter(([k]) => k in left)) };
    });
    memory.delete(key);
    sentByScroll.current = memoryFor(key).sentByScroll;
    seenIds.current = memoryFor(key).seenByScroll;
    asOf.current = undefined;
    setHidden(new Set());
    setLeaving(new Set());
    // Other cached lists for the cleared feeds are now stale too; they refetch when next opened.
    void qc.invalidateQueries({ queryKey: keys.itemsAll, refetchType: "none" });
    void startOver().then(() => {
      virtualizer.scrollToOffset(0);
      announce("List updated");
    });
  };

  // Row callbacks take the item as an argument, so they stay the same function between renders and the
  // memoized rows below skip renders that did not change them.
  const onLeading = swipeRead;
  // Starring a muted article un-mutes it (the server clears the mute), so its row leaves the Muted list.
  const starRow = useCallback(
    (item: Card, undoable: boolean) => {
      const leaves = mutedView && !item.starred;
      void act.toggleStar(item, undoable).then((ok) => {
        if (ok && leaves) hide([item.id]);
      });
    },
    [act, hide, mutedView],
  );
  const onTrailing = useCallback((item: Card) => starRow(item, true), [starRow]);
  const onStar = useCallback((item: Card) => starRow(item, false), [starRow]);
  const onMore = useCallback((item: Card) => openRowMenu(item.id), []);
  const renderRow = (item: Card, swipe: boolean, peek: boolean) => (
    <ListRow
      key={item.id}
      item={item}
      feed={feedById.get(item.feed_id)}
      layout={layout}
      scope={scope}
      swipe={swipe}
      peek={peek}
      selected={item.id === selected}
      tabStop={item.id === tabStopId}
      showThumb={dp.inboxThumbs === "auto"}
      showMuted={mutedView}
      actions={menuActions}
      onOpen={openItem}
      onLeading={onLeading}
      onTrailing={onTrailing}
      onStar={onStar}
      onMore={onMore}
      onPeekEnd={endPeek}
    />
  );

  const moreFailed = src.loadMoreFailed ? (
    <div role="alert" className="flex items-center justify-center gap-2 px-4 py-3 text-sm text-fg2">
      <span>Couldn&apos;t load more.</span>
      <Button variant="ghost" onClick={() => void src.loadMore()}>
        Retry
      </Button>
    </div>
  ) : null;
  const body = (() => {
    if (src.pending) return <Skeleton />;
    // Only a failed first load replaces the list; a failed later page keeps it (and the scroll position).
    if (src.error != null && !src.hasData) {
      const error = src.error;
      // A search the server calls too broad says so in its own words; asking again would not help.
      const broad = tooBroadMessage(error);
      if (broad) return <StatusBlock role="status" title="That search is too broad" body={broad} />;
      // Only a failed connection is "couldn't reach the server"; any other answer (a 400, a 5xx) is the server's.
      const unreachable = !(error instanceof ApiError) || error.status === 0;
      const refused = error instanceof ApiError && error.status === 400;
      return (
        <StatusBlock
          role="alert"
          title="Couldn't load articles"
          body={unreachable ? "Kipple couldn't reach the server. Your place in the list is saved." : refused ? "Kipple no longer recognises this place in the list." : errorMessage(error)}
        >
          {/* A refused request (400) repeats the same refusal: start the list from its first page instead. */}
          {refused ? <Button onClick={() => void startOver()}>Start over</Button> : <Button onClick={src.retry}>Try again</Button>}
        </StatusBlock>
      );
    }
    if (Page ? nothingShown : rows.length === 0) {
      if (boot.data && visibleFeeds(boot.data.feeds).length === 0 && !scope.q) {
        return (
          <FirstRun onAdd={() => navigate("/feeds", { state: { open: "add" } })} onImport={() => navigate("/feeds", { state: { open: "import" } })} />
        );
      }
      const c = emptyCopy(scope, pendingNew, scope.feed ? feedById.get(scope.feed) : undefined);
      return (
        <StatusBlock role="status" title={c.title} body={c.body}>
          {c.health ? (
            <Link to="/health" className="text-sm text-link underline underline-offset-2">
              Open Feed health
            </Link>
          ) : null}
          {scope.view === "muted" && !scope.q ? (
            <Link to="/settings/filters" className="text-sm text-link underline underline-offset-2">
              Add a filter in Settings
            </Link>
          ) : null}
        </StatusBlock>
      );
    }
    if (Page) {
      return (
        <>
          <div data-page-box="">
            <Page items={src.cards} scope={scope} more={src.more} width={width} selectedId={selected} tabStopId={shown(focusId, src.cards)} onOpen={openItem} actions={menuActions} digest={src.digest} />
          </div>
          <div ref={endRef} aria-hidden="true" className="h-px" />
          {src.loadingMore ? (
            <p className="py-3 text-center text-sm text-fg2" role="status">
              Loading more
            </p>
          ) : null}
          {moreFailed}
        </>
      );
    }
    return (
      <>
      <div style={{ height: virtualizer.getTotalSize(), position: "relative", width: "100%" }}>
        {virtualItems.map((v) => {
          const r = rows[v.index];
          if (!r) return null;
          return (
            <div
              key={v.key}
              data-index={v.index}
              ref={virtualizer.measureElement}
              style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${v.start - noteH}px)` }}
            >
              <div className="kp-row" data-leaving={r.kind === "item" && leaving.has(r.item.id) ? "true" : undefined}>
              {r.kind === "header" ? (
                <h2 className="sticky top-0 z-[1] border-b border-line bg-bg px-4 py-2 text-xs font-semibold tracking-wide text-fg2 uppercase">
                  {r.label}
                </h2>
              ) : r.kind === "group" ? (
                <div className="grid gap-4 px-4 py-2" style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}>
                  {r.items.map((item) => renderRow(item, false, false))}
                </div>
              ) : layout.grid ? (
                <div className="px-4 py-2">{renderRow(r.item, true, peekOn && r.item.id === firstItemId)}</div>
              ) : (
                renderRow(r.item, true, peekOn && r.item.id === firstItemId)
              )}
              </div>
            </div>
          );
        })}
        {src.loadingMore ? (
          <p className="absolute bottom-0 w-full py-3 text-center text-sm text-fg2" role="status">
            Loading more
          </p>
        ) : null}
      </div>
      {moreFailed}
      </>
    );
  })();

  const reduced = prefersReducedMotion();
  const pullOffset = holding ? 56 : pull.distance;
  const pullLabel = holding || refreshing ? "Refreshing" : pull.armed ? "Release to refresh" : "Pull to refresh";
  const showPull = holding || pull.distance >= 16;
  // The pull-to-refresh offset moves every story without anything scrolling: once it lets go, the next look starts from
  // where the stories are now (what was passed stays passed), not from where the offset held them.
  const pulled = pullOffset > 0 && !reduced;
  useLayoutEffect(() => {
    const el = parentRef.current;
    if (pulled || !Page || !el || !pageLook.current || !pageRestored.current) return;
    pageOwed.current = 0;
    pageLook.current = firstLook(measurePage(el));
  }, [pulled, Page]);
  // Known to have nothing (not merely still loading): a search with no results has nothing to save or mark.
  const empty = !src.pending && nothingShown;
  // Each feed is judged by the new articles waiting from it, the way the pill counts them (one rule with the feed's empty list).
  const feeds = boot.data?.feeds;
  const folders = boot.data?.folders;
  const loaded = !src.pending && !(src.error != null && !src.hasData);
  const { view, feed: scopeFeed, folder: scopeFolder, q: scopeQ, order: scopeOrder, length: scopeLength } = scope;
  const noteFeeds = useMemo(() => {
    const noteScope: Scope = { view, feed: scopeFeed, folder: scopeFolder, q: scopeQ, order: scopeOrder, length: scopeLength };
    const pendingOf = (id: string) => pendingFor(pendingByFeed, { ...noteScope, feed: id, folder: undefined }, feeds ?? [], folders ?? [], { ids: loadedIds, pendingIds });
    return failureNoteFeeds({ scope: noteScope, pendingOf, feeds, folders, loaded, empty });
  }, [view, scopeFeed, scopeFolder, scopeQ, scopeOrder, scopeLength, pendingByFeed, pendingIds, loadedIds, feeds, folders, loaded, empty]);
  // The note's height, for the virtualizer: it measures again if the note resizes (a wider or narrower window).
  const noted = noteFeeds.length > 0;
  // The offset is saved over the note and the rows together. The note may be gone, or another height, when the list comes
  // back (the feed recovered, or the window changed), which would move the rows under the reader by the difference: on the
  // first measurement of a list that comes back with its rows, the offset is corrected by that difference. A reader who
  // had not scrolled past the note keeps the offset as it was.
  const returning = useRef(!!saved?.offset);
  const loadedRef = useLatestRef(loaded);
  useLayoutEffect(() => {
    const el = noteBoxRef.current;
    const h = noted && el ? el.offsetHeight : 0;
    if (returning.current) {
      returning.current = false;
      const m = memory.get(key);
      const was = m?.offsetNoteH ?? 0;
      if (loadedRef.current && m && parentRef.current && m.offset > was && h !== was) {
        m.offset += h - was;
        if (!Page || pageRestored.current) parentRef.current.scrollTop = m.offset;
      }
    }
    noteHRef.current = h;
    if (!noted || !el) return setNoteH(0);
    const measure = () => {
      noteHRef.current = el.offsetHeight;
      setNoteH(el.offsetHeight);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [noted, Page, key, loadedRef]);
  const controlsRef = useLatestRef(onControls);
  const stopRef = useLatestRef(tabStopId);
  const focusStop = useCallback(() => {
    const id = stopRef.current;
    if (id) focusRow(parentRef.current, id);
    return !!id && !!parentRef.current?.contains(document.activeElement);
  }, [stopRef]);
  useEffect(() => {
    controlsRef.current?.({ markAllRead, count: items.length, fallback, empty, focusStop });
  }, [markAllRead, items.length, fallback, empty, focusStop]);
  // A header outside the list must not keep acting on a list that is gone (a list remounted under the same scope).
  useEffect(() => () => controlsRef.current?.(null), [controlsRef]);

  return (
    <section aria-label="Articles" className="flex h-full min-h-0 flex-col">
      {fallback ? (
        <p role="status" data-testid="fallback-banner" className="border-b border-line bg-surface px-4 py-2 text-sm text-fg2">
          No exact matches: showing partial matches
        </p>
      ) : null}
      {caption ? (
        <div role="status" className="mx-3 mb-1 flex items-center gap-2 rounded-xl border border-line bg-surface px-3 py-2 text-sm text-fg">
          <span className="flex-1">Swipe a row right to mark it read or unread, left to star it or see more.</span>
          <button
            type="button"
            aria-label="Dismiss tip"
            className="hit inline-flex items-center justify-center rounded-md"
            onClick={() => {
              setCaption(false);
              if (peekOn) endPeek();
            }}
          >
            <X className="size-5" aria-hidden="true" />
          </button>
        </div>
      ) : null}
      <div className="relative min-h-0 flex-1">
        <NewArticlesPill count={pendingNew} onClick={loadNew} />
        <div
          aria-hidden={!showPull}
          data-testid="pull-indicator"
          className="pointer-events-none absolute inset-x-0 top-0 z-10 flex justify-center text-fg2"
          style={{ height: reduced ? undefined : pullOffset, overflow: "hidden", display: showPull ? undefined : "none" }}
        >
          <div className="flex items-center gap-2 self-end pb-2 text-xs font-medium" role={showPull ? "status" : undefined}>
            <RefreshCw className={holding || refreshing ? "size-4 animate-spin" : "size-4"} aria-hidden="true" style={pull.armed ? { transform: "rotate(180deg)" } : undefined} />
            {pullLabel}
          </div>
        </div>
        <div
          ref={parentRef}
          data-testid="list-scroll"
          aria-busy={src.pending || src.loadingMore}
          // A page layout's scroll position moves only when something scrolls it (pageSeen.ts): the browser's scroll
          // anchoring would also move it when a story above the screen changes height.
          className={`@container h-full overflow-y-auto overscroll-y-contain${Page ? " [overflow-anchor:none]" : ""}`}
          tabIndex={-1}
          onKeyDownCapture={onRowKey}
          onFocus={onListFocus}
        >
          {/* Room at the end the size of a toast, always there (a room that came and went with the toast would move the
              list under the reader), so the last row can be scrolled out from under the undo toast. */}
          <div
            ref={contentRef}
            className="pb-20"
            style={
              pulled
                ? { transform: `translateY(${pullOffset}px)`, transition: pull.dragging ? "none" : "transform 200ms cubic-bezier(0.2, 0, 0, 1)" }
                : undefined
            }
          >
            {/* The first thing in the scrolling content, so it scrolls away; the virtualizer is told its height (`scrollMargin`). */}
            {noteFeeds.length > 0 ? (
              <div ref={noteBoxRef} className="pb-2">
                <FeedFailureNote failing={noteFeeds} inFolder={!scope.feed} />
              </div>
            ) : null}
            {body}
          </div>
        </div>
      </div>
    </section>
  );
}

/**
 * "N new articles". It is always mounted and fades and slides on transform and opacity only (compositor
 * work: nothing in the list reflows, and the list is never pushed down), and it keeps the last count while
 * it fades out so the text does not blank. Reduced motion is honoured by the global rule in index.css.
 */
export function NewArticlesPill({ count, onClick }: { count: number; onClick: () => void }) {
  const [n, setN] = useState(count);
  if (count > 0 && count !== n) setN(count);
  const open = count > 0;
  return (
    <div className="pointer-events-none absolute inset-x-0 top-2 z-20 flex justify-center" data-testid="new-pill-wrap">
      <button
        type="button"
        className="kp-pill pointer-events-auto min-h-11 rounded-full bg-accent px-5 text-sm font-medium text-bg tabular-nums shadow-lg"
        data-open={open}
        aria-hidden={!open}
        tabIndex={open ? 0 : -1}
        onClick={onClick}
      >
        {n} new article{n === 1 ? "" : "s"}
      </button>
    </div>
  );
}

interface ListRowProps {
  item: Card;
  feed: Feed | undefined;
  layout: ListLayout;
  scope: Scope;
  swipe: boolean;
  peek: boolean;
  selected: boolean;
  /** This row's controls are the list's one Tab stop (rowTabStop.ts). */
  tabStop: boolean;
  showThumb: boolean;
  /** The Muted list: each row says which filter muted it, with Restore and Edit rule. */
  showMuted: boolean;
  actions: RowMenuActions;
  onOpen: (item: Card) => void;
  onLeading: (item: Card) => void;
  onTrailing: (item: Card) => void;
  onStar: (item: Card) => void;
  onMore: (item: Card) => void;
  onPeekEnd: () => void;
}

/** One list row. Memoized: run progress, fetch ticks and unrelated selection changes leave it alone. */
export const ListRow = memo(function ListRow(p: ListRowProps) {
  const { item } = p;
  const row = (
    <SwipeRow
      item={item}
      enabled={p.swipe}
      onLeading={() => p.onLeading(item)}
      onTrailing={() => p.onTrailing(item)}
      onStarButton={() => p.onStar(item)}
      onMore={() => p.onMore(item)}
      onLongPress={() => p.onMore(item)}
      peek={p.peek}
      onPeekEnd={p.onPeekEnd}
    >
      <p.layout.Row
        item={item}
        feed={p.feed}
        selected={p.selected}
        to={articleTo(item.id, p.scope)}
        onOpen={p.onOpen}
        onToggleStar={p.onStar}
        actions={p.actions}
        showThumb={p.showThumb}
      />
    </SwipeRow>
  );
  return (
    <RowTabStop.Provider value={p.tabStop}>
      {row}
      {p.showMuted ? <MutedStrip item={item} onRestore={() => p.actions.restore(item)} onEditRule={() => p.actions.editRule(item)} /> : null}
    </RowTabStop.Provider>
  );
});

/** "Muted by <rule>" under a row of the Muted list, with the two things you can do about it. */
export function MutedStrip({ item, onRestore, onEditRule }: { item: Card; onRestore: () => void; onEditRule: () => void }) {
  const name = item.muted_by_name;
  const tabIndex = useRowTabIndex();
  return (
    <div data-testid="muted-strip" data-muted-for={item.id} className="flex flex-wrap items-center gap-x-3 border-b border-line bg-surface px-4 text-xs">
      <span className="min-w-0 flex-1 py-1 text-fg2">
        {name ? (
          <>
            Muted by <strong className="font-semibold text-fg">{name}</strong>
          </>
        ) : (
          "Muted by a filter that was deleted"
        )}
      </span>
      <Button variant="ghost" tabIndex={tabIndex} onClick={onRestore} aria-label={`Restore ${item.title || "article"}`}>
        Restore
      </Button>
      {name ? (
        <Button variant="ghost" tabIndex={tabIndex} onClick={onEditRule} aria-label={`Edit the rule that muted ${item.title || "this article"}`}>
          Edit rule
        </Button>
      ) : null}
    </div>
  );
}

/** The rows to render: the visible window and its overscan, plus the list's Tab stop row wherever it is. */
export function keepMounted(range: Range, stopIndex: number): number[] {
  const indexes = defaultRangeExtractor(range);
  if (stopIndex < 0 || indexes.includes(stopIndex)) return indexes;
  return stopIndex < (indexes[0] ?? 0) ? [stopIndex, ...indexes] : [...indexes, stopIndex];
}

/** The last row the visible window and its overscan reach, which decides when the next page loads: not the Tab stop
 * row keepMounted adds, which can be far below. */
export function windowReach(range: { endIndex: number } | null | undefined, count: number): number {
  return range ? Math.min(count - 1, range.endIndex + OVERSCAN) : 0;
}

/** The keys a list row takes (onRowKey): the ones its link advertises, and Space. */
const ROW_KEYS = new Set([...ROW_KEYSHORTCUTS.split(" "), " "]);

/** Put keyboard focus back on a list row (Esc or u in the reader pane); the list itself when the row is not rendered. */
export function focusListRow(id: string): void {
  const list = document.querySelector<HTMLElement>('[data-testid="list-scroll"]');
  const link = list?.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"] a`);
  (link ?? list)?.focus({ preventScroll: true });
}

/**
 * What the arrow keys visit on a page layout, in the order it is drawn, which is its DOM order: its stories (article
 * ids) and the buttons it marks `data-stop-id` (the Digest's "N more" and Retry), which are not Tab stops of their own.
 */
function pageOrder(container: HTMLElement | null): string[] {
  const els = container?.querySelectorAll<HTMLElement>("article[data-item-id],[data-stop-id]") ?? [];
  return [...els].map((e) => e.dataset.itemId ?? e.dataset.stopId ?? "");
}

/** Scroll a page layout's story into view, as little as needed. */
function storyIntoView(container: HTMLElement | null, id: string): void {
  container?.querySelector(`[data-item-id="${CSS.escape(id)}"],[data-stop-id="${CSS.escape(id)}"]`)?.scrollIntoView?.({ block: "nearest" });
}

function focusRow(container: HTMLElement | null, id: string): void {
  const link = container?.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"] a,[data-stop-id="${CSS.escape(id)}"] button`);
  link?.focus({ preventScroll: true });
}

function Skeleton() {
  return (
    <div aria-hidden="true" data-testid="list-skeleton">
      {Array.from({ length: 7 }, (_, i) => (
        <div key={i} className="flex min-h-28 gap-3 border-b border-line px-4 py-3">
          <div className="flex flex-1 flex-col gap-2">
            <div className="h-3 w-1/3 rounded bg-surface" />
            <div className="h-4 w-11/12 rounded bg-surface" />
            <div className="h-4 w-3/4 rounded bg-surface" />
            <div className="h-3 w-2/3 rounded bg-surface" />
          </div>
          <div className="size-[var(--thumb)] rounded-lg bg-surface" />
        </div>
      ))}
    </div>
  );
}

export function StatusBlock({
  title,
  body,
  role,
  asH1,
  children,
}: {
  title: string;
  body: string;
  role: "status" | "alert";
  /** A screen of its own (nothing else names it): the title is the page's h1 rather than an h2 under one. */
  asH1?: boolean;
  children?: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const h1 = useRef<HTMLHeadingElement>(null);
  // Empty and error screens move focus to the headline (design section 6): the h1 where the block is the page's own
  // heading, else the block, which the alert role reads out.
  useEffect(() => {
    if (role === "alert") (h1.current ?? ref.current)?.focus({ preventScroll: true });
  }, [role]);
  return (
    <div ref={ref} tabIndex={-1} role={role} className="mx-auto flex max-w-sm flex-col items-center gap-3 px-6 py-16 text-center outline-none">
      {asH1 ? (
        <h1 ref={h1} tabIndex={-1} className="text-lg font-semibold outline-none">
          {title}
        </h1>
      ) : (
        <h2 className="text-lg font-semibold">{title}</h2>
      )}
      <p className="text-sm text-fg2">{body}</p>
      {children}
    </div>
  );
}
