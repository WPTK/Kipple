import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useNavigate } from "react-router";
import { flattenItems, keys, scopeKey, useBootstrap, useItems, useMarkRead, useToggleStar } from "@/api/queries";
import { liveStore } from "@/api/events";
import type { Card, Scope } from "@/api/types";
import { getLayout } from "@/layouts";
import { prefsStore } from "@/lib/prefs";
import { useStore } from "@/lib/store";
import { withDayHeaders } from "@/lib/format";
import { useHotkeys } from "@/lib/keys";
import { Button } from "@/ui/button";
import { articleTo } from "@/lib/routes";
import { announce } from "@/shell/toasts";

// Scroll and selection memory per list, so "back" lands where you were
// (design 3.4: one restore path). Module scope: survives route changes.
const memory = new Map<string, { offset: number; selectedId?: string }>();

export function emptyCopy(scope: Scope): { title: string; body: string } {
  if (scope.q) return { title: `No results for "${scope.q}"`, body: "Try fewer words, or search All instead of just this feed." };
  if (scope.view === "starred") return { title: "No starred articles", body: "Star an article to keep it here. Retention never removes starred articles." };
  if (scope.view === "unread") return { title: "All caught up", body: "No unread articles. New ones appear after the next refresh." };
  return { title: "No articles yet", body: "Kipple hasn't fetched anything from these feeds yet." };
}

interface Props {
  scope: Scope;
  /** Item open in the reader pane (or route), highlighted in the list. */
  activeId?: string;
  /** Called on j/k. On a wide screen the route opens the item in the reader pane. */
  onKeyMove?: (item: Card) => void;
  keysEnabled?: boolean;
  header?: ReactNode;
}

export function ListPane({ scope, activeId, onKeyMove, keysEnabled = true, header }: Props) {
  const key = scopeKey(scope);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const boot = useBootstrap();
  const q = useItems(scope);
  const prefs = useStore(prefsStore);
  const layout = getLayout(prefs.layout);
  const star = useToggleStar();
  const markRead = useMarkRead();
  const live = useStore(liveStore);

  const items = useMemo(() => flattenItems(q.data), [q.data]);
  const rows = useMemo(() => withDayHeaders(items), [items]);
  const feedById = useMemo(() => new Map((boot.data?.feeds ?? []).map((f) => [f.id, f])), [boot.data]);

  const parentRef = useRef<HTMLDivElement>(null);
  const saved = memory.get(key);
  const [selectedId, setSelectedId] = useState<string | undefined>(saved?.selectedId);
  const selected = activeId ?? selectedId;

  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: (i) => {
      const r = rows[i];
      return !r || r.kind === "header" ? 40 : layout.estimateRow(r.item);
    },
    getItemKey: (i) => rows[i]?.key ?? i,
    overscan: 8,
    initialOffset: saved?.offset ?? 0,
  });

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
    const i = rows.findIndex((r) => r.kind === "item" && r.item.id === activeId);
    if (i >= 0) virtualizer.scrollToIndex(i, { align: "auto" });
  }, [activeId, rows, virtualizer]);

  // Infinite scroll: fetch the next page when the tail comes into view.
  const virtualItems = virtualizer.getVirtualItems();
  const lastIndex = virtualItems.length ? (virtualItems[virtualItems.length - 1]?.index ?? 0) : 0;
  useEffect(() => {
    if (q.hasNextPage && !q.isFetchingNextPage && rows.length > 0 && lastIndex >= rows.length - 10) void q.fetchNextPage();
  }, [lastIndex, rows.length, q]);

  const openItem = useCallback(
    (item: Card) => {
      setSelectedId(item.id);
    },
    [],
  );

  const move = useCallback(
    (delta: 1 | -1) => {
      if (items.length === 0) return;
      const cur = selected ? items.findIndex((i) => i.id === selected) : -1;
      const next = Math.min(items.length - 1, Math.max(0, cur < 0 ? (delta === 1 ? 0 : items.length - 1) : cur + delta));
      const item = items[next];
      if (!item) return;
      setSelectedId(item.id);
      const rowIndex = rows.findIndex((r) => r.kind === "item" && r.item.id === item.id);
      virtualizer.scrollToIndex(rowIndex, { align: "auto" });
      requestAnimationFrame(() => requestAnimationFrame(() => focusRow(parentRef.current, item.id)));
      onKeyMove?.(item);
    },
    [items, rows, selected, virtualizer, onKeyMove],
  );

  const selectedItem = items.find((i) => i.id === selected);

  useHotkeys(
    {
      next: () => move(1),
      prev: () => move(-1),
      open: () => {
        if (selectedItem) navigate(articleTo(selectedItem.id, scope), { state: { via: "key" } });
      },
      star: () => selectedItem && star.mutate({ id: selectedItem.id, starred: !selectedItem.starred }),
      toggleRead: () => selectedItem && markRead.mutate({ ids: [selectedItem.id], read: !selectedItem.read, reason: "key" }),
      top: () => virtualizer.scrollToOffset(0),
      bottom: () => virtualizer.scrollToIndex(rows.length - 1, { align: "end" }),
    },
    { singleKeys: prefs.shortcuts, enabled: keysEnabled },
  );

  const showPill = live.pendingNew > 0 && scope.view !== "starred" && !scope.q;
  const loadNew = () => {
    liveStore.set((s) => ({ ...s, pendingNew: 0 }));
    memory.delete(key);
    void qc.resetQueries({ queryKey: keys.items(scope) }).then(() => {
      virtualizer.scrollToOffset(0);
      announce("List updated");
    });
  };

  const body = (() => {
    if (q.isPending) return <Skeleton />;
    if (q.isError) {
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
              {r.kind === "header" ? (
                <h2 className="sticky top-0 border-b border-line bg-bg px-4 py-2 text-xs font-semibold tracking-wide text-fg2 uppercase">
                  {r.label}
                </h2>
              ) : (
                <layout.Row
                  item={r.item}
                  feed={feedById.get(r.item.feed_id)}
                  selected={r.item.id === selected}
                  to={articleTo(r.item.id, scope)}
                  onOpen={openItem}
                  onToggleStar={(item) => star.mutate({ id: item.id, starred: !item.starred })}
                />
              )}
            </div>
          );
        })}
        {q.isFetchingNextPage ? (
          <p className="absolute bottom-0 w-full py-3 text-center text-sm text-fg2" role="status">
            Loading more
          </p>
        ) : null}
      </div>
    );
  })();

  return (
    <section aria-label="Articles" className="flex h-full min-h-0 flex-col">
      {header}
      <div className="relative min-h-0 flex-1">
        {showPill ? (
          <div className="pointer-events-none absolute inset-x-0 top-2 z-20 flex justify-center">
            <Button variant="solid" className="pointer-events-auto rounded-full shadow-lg" onClick={loadNew}>
              {live.pendingNew} new article{live.pendingNew === 1 ? "" : "s"}
            </Button>
          </div>
        ) : null}
        <div
          ref={parentRef}
          data-testid="list-scroll"
          aria-busy={q.isPending || q.isFetchingNextPage}
          className="h-full overflow-y-auto overscroll-y-contain"
          tabIndex={-1}
        >
          {body}
        </div>
      </div>
    </section>
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
