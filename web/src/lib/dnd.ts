import { useCallback, useEffect, useRef, useState } from "react";

// Drag-to-reorder by grabbing the row itself, with pointer (mouse, pen), touch (press and hold on the row, or
// a touch on the grip at once) and keyboard (arrow keys on the grip). Everything that decides WHAT a drop
// means is pure and unit-tested (`planFeedDrop`, `planFolderDrop`, `dropSlot`, `reorderBody`); the hook only
// measures and paints. There is no library: the list is short and the rules are ours.

export interface Tree {
  /** Folder ids in order. */
  folders: string[];
  /** Feed ids of each folder, in order. */
  feeds: Record<string, string[]>;
}

/** Where a feed is dropped: into a folder, before one of its feeds (null = at the end). */
export interface FeedTarget {
  folder: string;
  before: string | null;
}

export function arrayMove<T>(list: readonly T[], from: number, to: number): T[] {
  const out = [...list];
  if (from < 0 || from >= out.length) return out;
  const [x] = out.splice(from, 1);
  out.splice(Math.min(Math.max(to, 0), out.length), 0, x as T);
  return out;
}

/** Put `id` into `list` before `before` (null: at the end), removing an earlier copy. */
export function insertBefore(list: readonly string[], id: string, before: string | null): string[] {
  const rest = list.filter((x) => x !== id);
  const at = before === null ? -1 : rest.indexOf(before);
  if (at < 0) return [...rest, id];
  return [...rest.slice(0, at), id, ...rest.slice(at)];
}

export function planFolderDrop(tree: Tree, folder: string, before: string | null): Tree {
  return { ...tree, folders: insertBefore(tree.folders, folder, before) };
}

export function planFeedDrop(tree: Tree, feed: string, target: FeedTarget): Tree {
  const feeds: Record<string, string[]> = {};
  for (const [k, v] of Object.entries(tree.feeds)) feeds[k] = v.filter((x) => x !== feed);
  feeds[target.folder] = insertBefore(tree.feeds[target.folder] ?? [], feed, target.before);
  return { ...tree, feeds };
}

const sameList = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

/**
 * The POST /api/reorder body for going from `a` to `b`: the folders when their order changed, and the feed list of
 * every folder whose feeds changed (a feed listed under another folder moves there). Null when nothing changed.
 */
export function reorderBody(a: Tree, b: Tree): { folders?: string[]; feeds?: { folder_id: string; ids: string[] }[] } | null {
  const body: { folders?: string[]; feeds?: { folder_id: string; ids: string[] }[] } = {};
  if (!sameList(a.folders, b.folders)) body.folders = b.folders;
  const feeds = Object.keys(b.feeds)
    .filter((f) => !sameList(a.feeds[f] ?? [], b.feeds[f] ?? []))
    .map((f) => ({ folder_id: f, ids: b.feeds[f] as string[] }));
  if (feeds.length) body.feeds = feeds;
  return body.folders || body.feeds ? body : null;
}

/** Where a vertical pointer position falls among rows: before the first row whose middle is below it. */
export function dropSlot(rows: readonly { id: string; top: number; bottom: number }[], y: number): { before: string | null } {
  for (const r of rows) if (y < (r.top + r.bottom) / 2) return { before: r.id };
  return { before: null };
}

// ------------------------------------------------------------------ hook

export type DragKind = "folder" | "feed" | "fav";

export interface DragSource {
  kind: DragKind;
  id: string;
  /** The list it lives in: the folder id for a feed, "folders" for a folder, "fav" for a favorite. */
  group: string;
}

export interface DropTarget {
  kind: DragKind;
  /** Destination list (folder id for feeds). */
  group: string;
  /** Insert before this id; null appends. */
  before: string | null;
}

interface Options {
  enabled: boolean;
  onDrop: (from: DragSource, to: DropTarget) => void;
  /** Scrolling ancestor, for edge auto-scroll. */
  scroller: () => HTMLElement | null;
  /** Arrow keys on the grip: move one place up or down inside its list. */
  onKeyMove?: (src: DragSource, delta: -1 | 1) => void;
}

const HOLD_MS = 280;
/** Classes for a draggable row: vertical scroll before the hold, no iOS link callout, no text selection. */
export const DND_ROW_CLASS = "touch-pan-y select-none [-webkit-touch-callout:none]";
const SLOP = 6;
const HOLD_SLOP = 8;
const EDGE = 56;

const rectOf = (r: HTMLElement) => {
  const b = r.getBoundingClientRect();
  return { top: b.top, bottom: b.bottom };
};

function firstFeedOf(root: ParentNode, folder: string): string | null {
  const el = root.querySelector<HTMLElement>(`[data-dnd-kind="feed"][data-dnd-group="${CSS.escape(folder)}"]`);
  return el?.dataset.dndId ?? null;
}

/** Find the drop target under a point: rows carry data-dnd-id, -kind and -group; a folder header takes feeds at its top. */
export function targetAt(src: DragSource, x: number, y: number, root: ParentNode = document): DropTarget | null {
  const stack: Element[] = typeof document.elementsFromPoint === "function" ? document.elementsFromPoint(x, y) : [];
  const el = stack.find(
    (e): e is HTMLElement =>
      e instanceof HTMLElement &&
      !!e.dataset.dndId &&
      e.dataset.dndId !== src.id &&
      (e.dataset.dndKind === src.kind || (src.kind === "feed" && e.dataset.dndKind === "folder")),
  );
  if (!el) return null;
  const kind = el.dataset.dndKind as DragKind;
  const id = el.dataset.dndId as string;
  if (src.kind === "feed" && kind === "folder") return { kind: "feed", group: id, before: firstFeedOf(root, id) };
  if (src.kind === "fav" || src.kind === "folder") {
    const rows = [...root.querySelectorAll<HTMLElement>(`[data-dnd-kind="${src.kind}"]`)].filter((r) => r.dataset.dndId !== src.id);
    return { kind: src.kind, group: src.group, before: dropSlot(rows.map((r) => ({ id: r.dataset.dndId as string, ...rectOf(r) })), y).before };
  }
  const group = el.dataset.dndGroup as string;
  const rows = [...root.querySelectorAll<HTMLElement>(`[data-dnd-kind="feed"][data-dnd-group="${CSS.escape(group)}"]`)].filter(
    (r) => r.dataset.dndId !== src.id,
  );
  return { kind: "feed", group, before: dropSlot(rows.map((r) => ({ id: r.dataset.dndId as string, ...rectOf(r) })), y).before };
}

export interface DragState {
  source: DragSource | null;
  target: DropTarget | null;
  /** Pointer offset from where the drag started (the row follows it). */
  dy: number;
}

interface Live {
  src: DragSource;
  x0: number;
  y0: number;
  x: number;
  y: number;
  id: number;
  type: string;
  started: boolean;
  timer?: ReturnType<typeof setTimeout>;
  raf?: number;
  el: HTMLElement;
}

/** The listeners live on the document for the length of one press, and are always removed with it. */
const proxy = { move: (_e: PointerEvent) => undefined as void, up: (_e: PointerEvent) => undefined as void };
const docMove = (e: PointerEvent) => proxy.move(e);
const docUp = (e: PointerEvent) => proxy.up(e);

/**
 * Row drag. `rowProps(src)` goes on the row; `gripProps(src)` on the grip button (a touch starts at once, arrow keys
 * call `onKeyMove`). A drag that moved suppresses the click that follows, so dropping on a link does not open it.
 */
export function useRowDnd(opts: Options) {
  const [state, setState] = useState<DragState>({ source: null, target: null, dy: 0 });
  const optsRef = useRef(opts);
  useEffect(() => {
    optsRef.current = opts;
  });
  const live = useRef<Live | null>(null);
  const suppress = useRef(false);
  const stateRef = useRef(state);
  useEffect(() => {
    stateRef.current = state;
  });

  const finish = useCallback((commit: boolean) => {
    const l = live.current;
    live.current = null;
    if (!l) return;
    if (l.timer) clearTimeout(l.timer);
    if (l.raf) cancelAnimationFrame(l.raf);
    document.removeEventListener("pointermove", docMove);
    document.removeEventListener("pointerup", docUp);
    document.removeEventListener("pointercancel", docUp);
    try {
      l.el.releasePointerCapture(l.id);
    } catch {
      /* not captured */
    }
    if (l.started) {
      suppress.current = true;
      setTimeout(() => (suppress.current = false), 0);
      const target = stateRef.current.target;
      setState({ source: null, target: null, dy: 0 });
      if (commit && target) optsRef.current.onDrop(l.src, target);
    }
  }, []);

  const begin = useCallback((l: Live) => {
    l.started = true;
    setState({ source: l.src, target: null, dy: 0 });
    const tick = () => {
      const cur = live.current;
      if (!cur || !cur.started) return;
      const sc = optsRef.current.scroller();
      if (sc) {
        const r = sc.getBoundingClientRect();
        if (cur.y < r.top + EDGE) sc.scrollTop -= Math.ceil((r.top + EDGE - cur.y) / 6);
        else if (cur.y > r.bottom - EDGE) sc.scrollTop += Math.ceil((cur.y - (r.bottom - EDGE)) / 6);
      }
      cur.raf = requestAnimationFrame(tick);
    };
    l.raf = requestAnimationFrame(tick);
  }, []);

  const onMove = useCallback(
    (e: PointerEvent) => {
      const l = live.current;
      if (!l || e.pointerId !== l.id) return;
      l.x = e.clientX;
      l.y = e.clientY;
      const dx = e.clientX - l.x0;
      const dy = e.clientY - l.y0;
      if (!l.started) {
        const dist = Math.hypot(dx, dy);
        // A touch that moves before the hold finishes is a scroll, not a drag.
        if (l.type === "touch" || l.type === "pen") {
          if (l.timer !== undefined && dist > HOLD_SLOP) finish(false);
          return;
        }
        if (dist < SLOP) return;
        begin(l);
      }
      const target = targetAt(l.src, e.clientX, e.clientY);
      setState((s) => ({ ...s, dy, target: target ?? s.target }));
    },
    [begin, finish],
  );

  const onUp = useCallback(
    (e: PointerEvent) => {
      if (live.current && e.pointerId === live.current.id) finish(e.type !== "pointercancel");
    },
    [finish],
  );

  useEffect(() => {
    proxy.move = onMove;
    proxy.up = onUp;
  });

  useEffect(() => {
    if (!state.source) return;
    const esc = (e: KeyboardEvent) => e.key === "Escape" && finish(false);
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  }, [state.source, finish]);

  useEffect(() => () => finish(false), [finish]);

  // A non-passive touchmove listener on the list, there from the start and not added when the hold ends: iOS ignores
  // a listener added mid-touch, and by the time a late one exists the browser has already chosen to scroll. It only
  // cancels the scroll while a drag is live, so a swipe that starts before the hold still scrolls normally.
  const guarded = useRef<HTMLElement | null>(null);
  const guard = useRef((e: TouchEvent) => {
    if (live.current?.started && e.cancelable) e.preventDefault();
  });
  useEffect(() => {
    const el = optsRef.current.scroller();
    if (el === guarded.current) return;
    guarded.current?.removeEventListener("touchmove", guard.current);
    el?.addEventListener("touchmove", guard.current, { passive: false });
    guarded.current = el;
  });
  useEffect(() => {
    const fn = guard.current;
    return () => {
      guarded.current?.removeEventListener("touchmove", fn);
      guarded.current = null;
    };
  }, []);

  const start = (src: DragSource, e: React.PointerEvent<HTMLElement>, viaGrip: boolean) => {
    if (!optsRef.current.enabled || (e.pointerType === "mouse" && e.button !== 0)) return;
    if (!viaGrip && (e.target as HTMLElement).closest("button, input, select, textarea, [data-no-drag]")) return;
    finish(false);
    const el = e.currentTarget;
    const l: Live = { src, x0: e.clientX, y0: e.clientY, x: e.clientX, y: e.clientY, id: e.pointerId, type: e.pointerType, started: false, el };
    live.current = l;
    const capture = () => {
      try {
        el.setPointerCapture(l.id);
      } catch {
        /* synthetic pointer */
      }
    };
    if (e.pointerType === "touch" || e.pointerType === "pen") {
      if (viaGrip) {
        // The grip has touch-action none: the drag can begin at once.
        capture();
        begin(l);
      } else {
        l.timer = setTimeout(() => {
          l.timer = undefined;
          capture();
          begin(l);
        }, HOLD_MS);
      }
    } else {
      capture();
    }
    document.addEventListener("pointermove", docMove);
    document.addEventListener("pointerup", docUp);
    document.addEventListener("pointercancel", docUp);
  };

  const rowProps = (src: DragSource) => ({
    "data-dnd-id": src.id,
    "data-dnd-kind": src.kind,
    "data-dnd-group": src.group,
    onPointerDown: (e: React.PointerEvent<HTMLElement>) => start(src, e, false),
    onClickCapture: (e: React.MouseEvent) => {
      if (suppress.current) {
        e.preventDefault();
        e.stopPropagation();
      }
    },
    onDragStart: (e: React.DragEvent) => e.preventDefault(),
    // A held touch on the row's link must not raise iOS's link preview or Android's context menu, or select text.
    onContextMenu: (e: React.MouseEvent) => {
      if (live.current && (live.current.type === "touch" || live.current.type === "pen")) e.preventDefault();
    },
  });

  const gripProps = (src: DragSource) => ({
    "data-drag-handle": "",
    onPointerDown: (e: React.PointerEvent<HTMLElement>) => {
      e.stopPropagation();
      start(src, e, true);
    },
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
      e.preventDefault();
      optsRef.current.onKeyMove?.(src, e.key === "ArrowUp" ? -1 : 1);
    },
  });

  return { state, rowProps, gripProps };
}
