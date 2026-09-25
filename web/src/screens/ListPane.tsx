import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useNavigate } from "react-router";
import { RefreshCw, X } from "lucide-react";
import { flattenItems, keys, scopeKey, useBootstrap, useItems } from "@/api/queries";
import { maxItemId } from "@/api/bulk";
import { clearPending, liveStore, pendingFor } from "@/api/events";
import { useRefreshAll, useRefreshing } from "@/api/refresh";
import type { Card, Scope } from "@/api/types";
import { SwipeRow } from "@/gestures/SwipeRow";
import { openRowMenu } from "@/gestures/rowMenu";
import { prefersReducedMotion } from "@/gestures/tracking";
import { COLLAPSE_MS, captureAnchor, compensate, type ScrollAnchor } from "@/lib/collapse";
import { usePullToRefresh } from "@/gestures/usePullToRefresh";
import { useResolvedLayout } from "@/layouts";
import type { RowMenuActions } from "@/layouts";
import { sessionLayoutStore, updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { withDayHeaders, type Row } from "@/lib/format";
import { useHotkeys } from "@/lib/keys";
import { useItemActions } from "@/lib/itemActions";
import { Button } from "@/ui/button";
import { articleTo } from "@/lib/routes";
import { announce, toast } from "@/shell/toasts";

// Scroll and selection memory per list, so "back" lands where you were
// (design 3.4: one restore path). Module scope: survives route changes.
const memory = new Map<string, { offset: number; selectedId?: string }>();

/** Forget remembered scroll and selection (tests). */
export function clearListMemory(): void {
  memory.clear();
}

export function emptyCopy(scope: Scope): { title: string; body: string } {
  if (scope.q) return { title: `No results for "${scope.q}"`, body: "Try fewer words, or search All instead of just this feed." };
  if (scope.view === "starred") return { title: "No starred articles", body: "Star an article to keep it here. Retention never removes starred articles." };
  if (scope.view === "unread") return { title: "All caught up", body: "No unread articles. New ones appear after the next refresh." };
  return { title: "No articles yet", body: "Kipple hasn't fetched anything from these feeds yet." };
}

/** What the header can ask the list to do (mark all needs the list's own loaded ids). */
export interface ListControls {
  markAllRead: () => void;
}

interface Props {
  scope: Scope;
  /** Item open in the reader pane (or route), highlighted in the list. */
  activeId?: string;
  /** Called on j/k. On a wide screen the route opens the item in the reader pane. */
  onKeyMove?: (item: Card) => void;
  keysEnabled?: boolean;
  header?: ReactNode | ((c: ListControls) => ReactNode);
}

type VRow = Row<Card> | { kind: "group"; key: string; items: Card[] };

/** Group consecutive item rows into `cols`-wide rows (cards grid). */
export function chunkRows(rows: Row<Card>[], cols: number): VRow[] {
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
export function columnsFor(width: number): number {
  return width < 600 ? 1 : width < 900 ? 2 : 3;
}

function useWidth(ref: React.RefObject<HTMLElement | null>): number {
  const [w, setW] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const e = entries[0];
      if (e) setW(Math.round(e.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return w;
}

const isTouch = (): boolean => {
  try {
    return window.matchMedia("(pointer: coarse)").matches;
  } catch {
    return false;
  }
};

export function ListPane({ scope, activeId, onKeyMove, keysEnabled = true, header }: Props) {
  const key = scopeKey(scope);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const boot = useBootstrap();
  const q = useItems(scope);
  const prefs = useStore(prefsStore);
  const dp = useDevicePrefs();
  const session = useStore(sessionLayoutStore);
  const { layout } = useResolvedLayout(scope);
  const live = useStore(liveStore);
  const act = useItemActions();
  const refreshAll = useRefreshAll();
  const refreshing = useRefreshing();

  const parentRef = useRef<HTMLDivElement>(null);
  const width = useWidth(parentRef);
  const cols = layout.grid ? columnsFor(width) : 1;

  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => new Set());
  // Rows that are collapsing (COLLAPSE_MS) before they leave the list.
  const [leaving, setLeaving] = useState<ReadonlySet<string>>(() => new Set());
  const [checked, setChecked] = useState<ReadonlySet<string>>(() => new Set());
  const allItems = useMemo(() => flattenItems(q.data), [q.data]);
  const items = useMemo(() => (hidden.size ? allItems.filter((i) => !hidden.has(i.id)) : allItems), [allItems, hidden]);
  const rows = useMemo(() => chunkRows(withDayHeaders(items), cols), [items, cols]);
  const feedById = useMemo(() => new Map((boot.data?.feeds ?? []).map((f) => [f.id, f])), [boot.data]);
  const unreadView = scope.view === "unread" && !scope.q;

  // "Only items present when the list loaded": the highest id the list knew about.
  const asOf = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!asOf.current && allItems.length) asOf.current = maxItemId(allItems.map((i) => i.id));
  }, [allItems]);

  const saved = memory.get(key);
  const [selectedId, setSelectedId] = useState<string | undefined>(saved?.selectedId);
  const selected = activeId ?? selectedId;

  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: (i) => {
      const r = rows[i];
      if (!r || r.kind === "header") return 40;
      if (r.kind === "group") return layout.estimateRow(r.items[0] as Card) + 16;
      return layout.estimateRow(r.item);
    },
    getItemKey: (i) => rows[i]?.key ?? i,
    overscan: 8,
    initialOffset: saved?.offset ?? 0,
  });

  const rowIndexOf = useCallback(
    (id: string) => rows.findIndex((r) => (r.kind === "item" ? r.item.id === id : r.kind === "group" && r.items.some((i) => i.id === id))),
    [rows],
  );

  // Remember position and selection for the way back.
  useEffect(() => {
    const el = parentRef.current;
    if (!el) return;
    const onScroll = () => memory.set(key, { offset: el.scrollTop, selectedId: memory.get(key)?.selectedId });
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [key]);
  useEffect(() => {
    memory.set(key, { offset: memory.get(key)?.offset ?? 0, selectedId });
  }, [key, selectedId]);

  // Restore focus to the anchor row when returning to the list.
  const restoredFocus = useRef(false);
  useEffect(() => {
    if (restoredFocus.current || !saved?.selectedId || items.length === 0) return;
    restoredFocus.current = true;
    requestAnimationFrame(() => focusRow(parentRef.current, saved.selectedId as string));
  }, [items.length, saved?.selectedId]);

  // The reader pane (or j/k in it) changed the open item: keep it in view.
  const lastActive = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!activeId || lastActive.current === activeId) return;
    lastActive.current = activeId;
    const i = rowIndexOf(activeId);
    if (i >= 0) virtualizer.scrollToIndex(i, { align: "auto" });
  }, [activeId, rowIndexOf, virtualizer]);

  // Infinite scroll: fetch the next page when the tail comes into view.
  const virtualItems = virtualizer.getVirtualItems();
  const lastIndex = virtualItems.length ? (virtualItems[virtualItems.length - 1]?.index ?? 0) : 0;
  useEffect(() => {
    // After a failed page the auto-fetch stops (offline would retry every render); the inline Retry row resumes it.
    if (q.hasNextPage && !q.isFetchingNextPage && !q.isFetchNextPageError && rows.length > 0 && lastIndex >= rows.length - 10) void q.fetchNextPage();
  }, [lastIndex, rows.length, q]);

  const openItem = useCallback((item: Card) => setSelectedId(item.id), []);

  const move = useCallback(
    (delta: 1 | -1) => {
      if (items.length === 0) return;
      const cur = selected ? items.findIndex((i) => i.id === selected) : -1;
      const next = Math.min(items.length - 1, Math.max(0, cur < 0 ? (delta === 1 ? 0 : items.length - 1) : cur + delta));
      const item = items[next];
      if (!item) return;
      setSelectedId(item.id);
      virtualizer.scrollToIndex(rowIndexOf(item.id), { align: "auto" });
      requestAnimationFrame(() => requestAnimationFrame(() => focusRow(parentRef.current, item.id)));
      onKeyMove?.(item);
    },
    [items, selected, virtualizer, rowIndexOf, onKeyMove],
  );

  const selectedItem = items.find((i) => i.id === selected);

  // ---- actions -----------------------------------------------------------

  const colsRef = useRef(cols);
  colsRef.current = cols;
  const pendingAnchor = useRef<ScrollAnchor | null>(null);
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>());
  useEffect(() => {
    const t = timers.current;
    return () => t.forEach(clearTimeout);
  }, []);

  /** Take rows out of the list: collapse them, then remove; the still-visible rows stay put on screen. */
  const hide = useCallback((ids: string[]): (() => void) => {
    const commit = () => {
      const el = parentRef.current;
      pendingAnchor.current = el && el.scrollTop > 0 ? captureAnchor(el, ids) : null;
      setHidden((h) => new Set([...h, ...ids]));
      setLeaving((l) => new Set([...l].filter((x) => !ids.includes(x))));
    };
    let timer: ReturnType<typeof setTimeout> | undefined;
    if (prefersReducedMotion() || colsRef.current > 1) commit();
    else {
      setLeaving((l) => new Set([...l, ...ids]));
      timer = setTimeout(commit, COLLAPSE_MS);
      timers.current.add(timer);
    }
    return () => {
      if (timer) clearTimeout(timer);
      setLeaving((l) => new Set([...l].filter((x) => !ids.includes(x))));
      setHidden((h) => new Set([...h].filter((x) => !ids.includes(x))));
    };
  }, []);

  // After rows are removed, put the first visible row back where it was on screen.
  useLayoutEffect(() => {
    const a = pendingAnchor.current;
    pendingAnchor.current = null;
    if (a && parentRef.current) compensate(parentRef.current, a);
  }, [hidden]);

  /** Swipe right, `m` in the menu: toggle read. Unread view: a row that became read leaves at once. */
  const swipeRead = useCallback(
    (item: Card) => {
      const leaves = unreadView && !item.read;
      void act.toggleRead(item, "swipe", leaves ? hide([item.id]) : undefined);
    },
    [act, hide, unreadView],
  );

  const range = useCallback(
    (item: Card, side: "above" | "below") => {
      if (scope.rank) return;
      const at = items.findIndex((i) => i.id === item.id);
      if (at < 0) return;
      const part = side === "above" ? items.slice(0, at) : items.slice(at + 1);
      const local = part.filter((i) => !i.read).map((i) => i.id);
      const restore = unreadView && local.length ? hide(local) : undefined;
      void act.markSide({ scope, order: scope.order === "oldest" ? "oldest" : "date", side, anchor: item, maxId: asOf.current }, local, restore);
    },
    [act, hide, items, scope, unreadView],
  );

  const markAllRead = useCallback(() => {
    const local = items.filter((i) => !i.read).map((i) => i.id);
    const restore = unreadView && local.length ? hide(local) : undefined;
    void act.markAll(scope, asOf.current, local, restore);
  }, [act, hide, items, scope, unreadView]);

  const menuActions: RowMenuActions = useMemo(
    () => ({
      toggleRead: (item) => void act.toggleRead(item, "key"),
      toggleStar: (item) => void act.toggleStar(item),
      markAbove: (item) => range(item, "above"),
      markBelow: (item) => range(item, "below"),
      canRange: !scope.rank,
      openOriginal: (item) => void window.open(item.url, "_blank", "noopener,noreferrer"),
      copyLink: (item) => copyLink(item.url),
      share: (item) => {
        if (typeof navigator !== "undefined" && "share" in navigator) {
          void navigator.share({ title: item.title, url: item.url }).catch(() => undefined);
        } else copyLink(item.url);
      },
    }),
    [act, range, scope.rank],
  );

  const toggleChecked = () => {
    if (!selectedItem) return;
    setChecked((c) => {
      const n = new Set(c);
      if (n.has(selectedItem.id)) n.delete(selectedItem.id);
      else n.add(selectedItem.id);
      announce(n.size === 0 ? "Selection cleared" : `${n.size} selected`);
      return n;
    });
  };
  const targets = (): Card[] => (checked.size ? items.filter((i) => checked.has(i.id)) : selectedItem ? [selectedItem] : []);

  useHotkeys(
    {
      next: () => move(1),
      prev: () => move(-1),
      open: () => {
        if (selectedItem) navigate(articleTo(selectedItem.id, scope), { state: { via: "key" } });
      },
      original: () => selectedItem && window.open(selectedItem.url, "_blank", "noopener,noreferrer"),
      // A background tab is a browser decision; window.open is the best a page can do.
      background: () => selectedItem && window.open(selectedItem.url, "_blank", "noopener,noreferrer"),
      star: () => {
        const t = targets();
        if (t.length === 0) return;
        const allStarred = t.every((i) => i.starred);
        for (const i of t) if (i.starred === allStarred) void act.toggleStar(i);
        setChecked(new Set());
      },
      toggleRead: () => {
        const t = targets();
        if (t.length === 0) return;
        if (t.length === 1) return void act.toggleRead(t[0] as Card, "key");
        const read = t.some((i) => !i.read);
        void act.setRead(
          t.filter((i) => i.read !== read).map((i) => i.id),
          read,
          "key",
        );
        setChecked(new Set());
      },
      select: toggleChecked,
      markAbove: () => selectedItem && range(selectedItem, "above"),
      markBelow: () => selectedItem && range(selectedItem, "below"),
      markAll: markAllRead,
      compact: () => {
        if (session) sessionLayoutStore.set(null);
        else if (layout.id !== "compact") sessionLayoutStore.set("compact");
        else announce("Already using the Compact layout");
      },
      top: () => virtualizer.scrollToOffset(0),
      bottom: () => virtualizer.scrollToIndex(rows.length - 1, { align: "end" }),
    },
    { singleKeys: prefs.shortcuts, enabled: keysEnabled },
  );

  // ---- pull to refresh ---------------------------------------------------

  const [holding, setHolding] = useState(false);
  const startRefresh = useCallback(() => {
    if (typeof navigator !== "undefined" && navigator.onLine === false) {
      announce("Can't refresh while offline");
      return;
    }
    setHolding(true);
    refreshAll.mutate();
  }, [refreshAll]);
  const pull = usePullToRefresh(parentRef, { enabled: !scope.q, onRefresh: startRefresh });
  // Hold the spinner until the run has finished (or a second after the request settles with nothing running).
  useEffect(() => {
    if (!holding || refreshAll.isPending || refreshing) return;
    const h = setTimeout(() => setHolding(false), 1000);
    return () => clearTimeout(h);
  }, [holding, refreshAll.isPending, refreshing]);

  // ---- first-run peek ----------------------------------------------------

  const firstItemId = items[0]?.id;
  const [peekOn, setPeekOn] = useState(false);
  const [caption, setCaption] = useState(false);
  useEffect(() => {
    if (dp.peekSeen || peekOn || !firstItemId || cols > 1 || !isTouch()) return;
    setPeekOn(true);
    setCaption(true);
  }, [dp.peekSeen, peekOn, firstItemId, cols]);
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

  const pendingNew = pendingFor(live.pendingByFeed, scope, boot.data?.feeds ?? []);
  const showPill = pendingNew > 0;
  const loadNew = () => {
    // Only what this list showed is now loaded; other feeds' arrivals keep counting for other lists.
    liveStore.set((s) => ({ ...s, pendingByFeed: clearPending(s.pendingByFeed, scope, boot.data?.feeds ?? []) }));
    memory.delete(key);
    asOf.current = undefined;
    setHidden(new Set());
    setLeaving(new Set());
    setChecked(new Set());
    // Other cached lists for the cleared feeds are now stale too; they refetch when next opened.
    void qc.invalidateQueries({ queryKey: keys.itemsAll, refetchType: "none" });
    void qc.resetQueries({ queryKey: keys.items(scope) }).then(() => {
      virtualizer.scrollToOffset(0);
      announce("List updated");
    });
  };

  const renderRow = (item: Card, swipe: boolean, peek: boolean) => (
    <SwipeRow
      key={item.id}
      item={item}
      enabled={swipe}
      onLeading={() => swipeRead(item)}
      onTrailing={() => void act.toggleStar(item, true)}
      onStarButton={() => void act.toggleStar(item)}
      onMore={() => openRowMenu(item.id)}
      onLongPress={() => openRowMenu(item.id)}
      peek={peek}
      onPeekEnd={endPeek}
    >
      <layout.Row
        item={item}
        feed={feedById.get(item.feed_id)}
        selected={item.id === selected}
        checked={checked.has(item.id)}
        to={articleTo(item.id, scope)}
        onOpen={openItem}
        onToggleStar={(i) => void act.toggleStar(i)}
        actions={menuActions}
        showThumb={dp.inboxThumbs === "auto"}
      />
    </SwipeRow>
  );

  const body = (() => {
    if (q.isPending) return <Skeleton />;
    // Only a failed first load replaces the list; a failed later page keeps it (and the scroll position).
    if (q.isError && !q.data) {
      return (
        <StatusBlock role="alert" title="Couldn't load articles" body="Kipple couldn't reach the server. Your place in the list is saved.">
          <Button onClick={() => void q.refetch()}>Try again</Button>
        </StatusBlock>
      );
    }
    if (rows.length === 0) {
      const c = emptyCopy(scope);
      return <StatusBlock role="status" title={c.title} body={c.body} />;
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
              style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${v.start}px)` }}
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
        {q.isFetchingNextPage ? (
          <p className="absolute bottom-0 w-full py-3 text-center text-sm text-fg2" role="status">
            Loading more
          </p>
        ) : null}
      </div>
      {q.isFetchNextPageError ? (
        <div role="alert" className="flex items-center justify-center gap-2 px-4 py-3 text-sm text-fg2">
          <span>Couldn&apos;t load more.</span>
          <Button variant="ghost" onClick={() => void q.fetchNextPage()}>
            Retry
          </Button>
        </div>
      ) : null}
      </>
    );
  })();

  const reduced = prefersReducedMotion();
  const pullOffset = holding ? 56 : pull.distance;
  const pullLabel = holding || refreshing ? "Refreshing" : pull.armed ? "Release to refresh" : "Pull to refresh";
  const showPull = holding || pull.distance >= 16;
  const headerNode = typeof header === "function" ? header({ markAllRead }) : header;

  return (
    <section aria-label="Articles" className="flex h-full min-h-0 flex-col">
      {headerNode}
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
        {showPill ? (
          <div className="pointer-events-none absolute inset-x-0 top-2 z-20 flex justify-center">
            <Button variant="solid" className="pointer-events-auto rounded-full shadow-lg" onClick={loadNew}>
              {pendingNew} new article{pendingNew === 1 ? "" : "s"}
            </Button>
          </div>
        ) : null}
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
          aria-busy={q.isPending || q.isFetchingNextPage}
          className="@container h-full overflow-y-auto overscroll-y-contain"
          tabIndex={-1}
        >
          <div
            style={
              pullOffset > 0 && !reduced
                ? { transform: `translateY(${pullOffset}px)`, transition: pull.dragging ? "none" : "transform 200ms cubic-bezier(0.2, 0, 0, 1)" }
                : undefined
            }
          >
            {body}
          </div>
        </div>
      </div>
    </section>
  );
}

function copyLink(url: string): void {
  const clip = typeof navigator !== "undefined" ? navigator.clipboard : undefined;
  if (!clip) {
    toast("Couldn't copy the link. Long-press the link to copy it.", "error");
    return;
  }
  clip.writeText(url).then(
    () => announce("Link copied"),
    () => toast("Couldn't copy the link. Long-press the link to copy it.", "error"),
  );
}

function focusRow(container: HTMLElement | null, id: string): void {
  const link = container?.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"] a`);
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
  children,
}: {
  title: string;
  body: string;
  role: "status" | "alert";
  children?: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  // Empty and error screens move focus to the headline (design section 6).
  useEffect(() => {
    if (role === "alert") ref.current?.focus({ preventScroll: true });
  }, [role]);
  return (
    <div ref={ref} tabIndex={-1} role={role} className="mx-auto flex max-w-sm flex-col items-center gap-3 px-6 py-16 text-center outline-none">
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="text-sm text-fg2">{body}</p>
      {children}
    </div>
  );
}
