import { useCallback, useEffect, useRef, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import { Mail, MailOpen, MoreHorizontal, Star } from "lucide-react";
import type { Card } from "@/api/types";
import { LongPress, ROW, RowSwipe, prefersReducedMotion, rubberBand } from "./tracking";
import { gestureLock } from "./lock";

// Row swipe, matching iOS Mail: swipe RIGHT (leading) toggles read/unread, swipe LEFT
// (trailing) reveals Star and More. A full swipe commits (the caller records an undo),
// a partial trailing swipe rests open on the two buttons. Touch only: mouse and pen use
// the visible buttons. All the thresholds live in tracking.ts and are unit-tested there.

const SNAP_MS = 200;
const SLIDE_MS = 160;
const SNAP_EASE = "cubic-bezier(0.2, 0, 0, 1)";

/** Only one row rests open at a time. */
let openRow: { id: string; close: () => void } | null = null;

interface Props {
  item: Card;
  /** Off for mouse-only surfaces and multi-column card grids. */
  enabled: boolean;
  onLeading: () => void;
  onTrailing: () => void;
  onStarButton: () => void;
  onMore: () => void;
  onLongPress: () => void;
  /** Play the first-run peek on this row. */
  peek?: boolean;
  onPeekEnd?: () => void;
  children: ReactNode;
}

export function SwipeRow({ item, enabled, onLeading, onTrailing, onStarButton, onMore, onLongPress, peek, onPeekEnd, children }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const leading = useRef<HTMLDivElement>(null);
  const trailing = useRef<HTMLDivElement>(null);
  const tracker = useRef(new RowSwipe());
  const restOffset = useRef(0); // 0 closed, -revealWidth open
  const suppressClick = useRef(false);
  const suppressTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  const activePointer = useRef<number | null>(null);
  const touches = useRef(new Set<number>());
  const latest = useRef({ onLeading, onTrailing, onLongPress });
  useEffect(() => {
    latest.current = { onLeading, onTrailing, onLongPress };
  });
  const lpRef = useRef<LongPress | null>(null);
  const fireRef = useRef<() => void>(() => undefined);

  const later = (fn: () => void, ms: number) => {
    const h = setTimeout(fn, ms);
    timers.current.push(h);
  };

  /** Paint an offset (px, signed) and the panels that go with it. */
  const paint = useCallback((raw: number, animate: boolean) => {
    const el = content.current;
    const width = el?.offsetWidth || 375;
    const off = rubberBand(raw, width);
    if (!el) return;
    const reduced = prefersReducedMotion();
    el.style.transition = animate && !reduced ? `transform ${SNAP_MS}ms ${SNAP_EASE}` : "none";
    el.style.transform = off === 0 ? "" : `translateX(${off}px)`;
    const lead = leading.current;
    const trail = trailing.current;
    const armed = tracker.current.armed();
    if (lead) {
      lead.style.width = `${Math.max(off, 0)}px`;
      lead.style.visibility = off > 0 ? "visible" : "hidden";
      lead.style.opacity = String(Math.min(1, Math.max(0, (off - ROW.iconFadePx) / ROW.iconFadePx + 0.01)));
      lead.dataset.armed = String(off > 0 && armed);
    }
    if (trail) {
      trail.style.width = `${Math.max(-off, ROW.revealWidth)}px`;
      trail.style.visibility = off < 0 ? "visible" : "hidden";
      trail.dataset.armed = String(off < 0 && armed);
    }
  }, []);

  const closeRow = useCallback(() => {
    restOffset.current = 0;
    paint(0, true);
    if (openRow?.id === item.id) openRow = null;
  }, [paint, item.id]);

  const suppress = () => {
    suppressClick.current = true;
    if (suppressTimer.current) clearTimeout(suppressTimer.current);
    suppressTimer.current = setTimeout(() => (suppressClick.current = false), 120);
  };

  function fireLongPress() {
    // Holding opens the row menu; the release must not also open the article.
    tracker.current.cancel();
    gestureLock.rowSwipe = false;
    suppress();
    paint(restOffset.current, true);
    latest.current.onLongPress();
  }

  const finishRef = useRef<(cancelled: boolean) => void>(() => undefined);
  useEffect(() => {
    fireRef.current = fireLongPress;
  });

  useEffect(() => {
    const lp = new LongPress(() => fireRef.current());
    lpRef.current = lp;
    const timerList = timers.current;
    const pointers = touches.current;
    return () => {
      timerList.forEach(clearTimeout);
      lp.cancel();
      // Unmounted mid-swipe (virtualizer recycle, resync, navigation): no pointerup will come to release the lock.
      if (activePointer.current !== null) {
        activePointer.current = null;
        pointers.clear();
        gestureLock.rowSwipe = false;
      }
      if (suppressTimer.current) clearTimeout(suppressTimer.current);
      if (openRow?.id === item.id) openRow = null;
    };
  }, [item.id]);

  // A tap or touch outside an open row closes it.
  useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (restOffset.current !== 0 && !host.current?.contains(e.target as Node)) closeRow();
    };
    document.addEventListener("pointerdown", onDown, true);
    return () => document.removeEventListener("pointerdown", onDown, true);
  }, [closeRow]);

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!enabled || e.pointerType !== "touch") return;
    touches.current.add(e.pointerId);
    if (touches.current.size > 1) {
      // A second finger: a pinch or a two-finger scroll. Give the row back.
      tracker.current.cancel();
      lpRef.current?.cancel();
      gestureLock.rowSwipe = false;
      paint(restOffset.current, true);
      return;
    }
    if (openRow && openRow.id !== item.id) openRow.close();
    activePointer.current = e.pointerId;
    const width = content.current?.offsetWidth || 375;
    tracker.current.start(e.clientX, e.clientY, performance.now(), {
      width,
      viewportWidth: window.innerWidth || 375,
      base: restOffset.current,
    });
    if (tracker.current.phase === "pending") lpRef.current?.start(e.clientX, e.clientY);
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.pointerId !== activePointer.current) return;
    lpRef.current?.move(e.clientX, e.clientY);
    const before = tracker.current.phase;
    const phase = tracker.current.move(e.clientX, e.clientY, performance.now());
    if (phase === "horizontal") {
      if (before !== "horizontal") {
        lpRef.current?.cancel();
        gestureLock.rowSwipe = true;
        try {
          e.currentTarget.setPointerCapture(e.pointerId);
        } catch {
          /* synthetic pointer */
        }
        suppress();
      }
      paint(tracker.current.offset, false);
    } else if (phase === "vertical") {
      lpRef.current?.cancel();
    }
  };

  const slideOff = (dir: 1 | -1, then: () => void) => {
    const el = content.current;
    const width = el?.offsetWidth || 375;
    if (el && !prefersReducedMotion()) {
      el.style.transition = `transform ${SLIDE_MS}ms ease-in`;
      el.style.transform = `translateX(${dir * width}px)`;
      later(() => {
        then();
        restOffset.current = 0;
        paint(0, true);
      }, SLIDE_MS);
    } else {
      then();
      restOffset.current = 0;
      paint(0, false);
    }
  };

  const finish = (cancelled: boolean) => {
    lpRef.current?.cancel();
    const wasHorizontal = tracker.current.phase === "horizontal";
    const res = cancelled ? { action: "close" as const } : tracker.current.end();
    if (cancelled) tracker.current.cancel();
    gestureLock.rowSwipe = false;
    activePointer.current = null;
    if (!wasHorizontal) return;
    if (res.action === "commit") {
      restOffset.current = 0;
      if (res.side === "leading") slideOff(1, () => latest.current.onLeading());
      else slideOff(-1, () => latest.current.onTrailing());
    } else if (res.action === "open") {
      restOffset.current = -ROW.revealWidth;
      openRow = { id: item.id, close: closeRow };
      paint(restOffset.current, true);
    } else {
      restOffset.current = 0;
      if (openRow?.id === item.id) openRow = null;
      paint(0, true);
    }
  };

  useEffect(() => {
    finishRef.current = finish;
  });

  // A touch that ends without a pointer event (the app loses focus, the OS steals the touch) must not leave the lock on.
  useEffect(() => {
    const abort = () => {
      if (activePointer.current !== null) finishRef.current(true);
    };
    window.addEventListener("blur", abort);
    document.addEventListener("touchcancel", abort, true);
    return () => {
      window.removeEventListener("blur", abort);
      document.removeEventListener("touchcancel", abort, true);
    };
  }, []);

  const onPointerUp = (e: ReactPointerEvent<HTMLDivElement>) => {
    touches.current.delete(e.pointerId);
    if (e.pointerId !== activePointer.current) return;
    finish(false);
  };
  const onPointerCancel = (e: ReactPointerEvent<HTMLDivElement>) => {
    touches.current.delete(e.pointerId);
    if (e.pointerId !== activePointer.current) return;
    finish(true);
  };

  // First-run peek: show that the row can be swiped, changing no state.
  useEffect(() => {
    if (!peek) return;
    const list: ReturnType<typeof setTimeout>[] = [];
    let cancelled = false;
    const t = (fn: () => void, ms: number) => list.push(setTimeout(() => !cancelled && fn(), ms));
    const move = (px: number) => {
      const el = content.current;
      if (!el) return;
      el.style.transition = `transform 300ms ${SNAP_EASE}`;
      el.style.transform = px ? `translateX(${px}px)` : "";
      const lead = leading.current;
      const trail = trailing.current;
      if (lead) {
        lead.style.width = `${Math.max(px, 0)}px`;
        lead.style.visibility = px > 0 ? "visible" : "hidden";
        lead.style.opacity = "1";
      }
      if (trail) {
        trail.style.width = `${Math.max(-px, ROW.revealWidth)}px`;
        trail.style.visibility = px < 0 ? "visible" : "hidden";
      }
    };
    const end = () => {
      move(0);
      onPeekEnd?.();
    };
    const onTouch = () => {
      cancelled = true;
      list.forEach(clearTimeout);
      move(0);
      onPeekEnd?.();
    };
    window.addEventListener("pointerdown", onTouch, { once: true, capture: true });
    if (prefersReducedMotion()) {
      // No motion: the caption alone teaches it.
      t(end, 0);
    } else {
      t(() => move(72), 600);
      t(() => move(0), 600 + 320 + 700);
      t(() => move(-72), 600 + 320 + 700 + 240 + 100);
      t(end, 600 + 320 + 700 + 240 + 100 + 320 + 700);
    }
    return () => {
      cancelled = true;
      list.forEach(clearTimeout);
      window.removeEventListener("pointerdown", onTouch, true);
    };
    // The sequence is one-shot per mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [peek]);

  const onClickCapture = (e: React.MouseEvent) => {
    if (suppressClick.current) {
      e.preventDefault();
      e.stopPropagation();
      suppressClick.current = false;
      return;
    }
    if (restOffset.current !== 0) {
      // First tap on an open row closes it instead of opening the article.
      e.preventDefault();
      e.stopPropagation();
      closeRow();
    }
  };

  if (!enabled) return <>{children}</>;

  const willRead = !item.read;
  return (
    <div ref={host} data-swipe-row className="relative overflow-hidden">
      <div
        ref={leading}
        aria-hidden="true"
        data-armed="false"
        style={{ width: 0, visibility: "hidden" }}
        className="swipe-panel absolute inset-y-0 left-0 overflow-hidden bg-accent text-bg"
      >
        <div className="flex h-full w-28 items-center gap-2 pl-4 text-sm font-semibold">
          <span className="swipe-icon inline-flex transition-transform [[data-armed=true]_&]:scale-115">
            {willRead ? <MailOpen className="size-6" /> : <Mail className="size-6" />}
          </span>
          <span className="[[data-armed=true]_&]:underline">{willRead ? "Read" : "Unread"}</span>
        </div>
      </div>
      <div
        ref={trailing}
        data-armed="false"
        style={{ width: ROW.revealWidth, visibility: "hidden" }}
        className="swipe-panel absolute inset-y-0 right-0 flex overflow-hidden bg-fg text-bg"
      >
        <button
          type="button"
          tabIndex={-1}
          onClick={() => {
            closeRow();
            onStarButton();
          }}
          className="flex min-w-[72px] flex-1 flex-col items-center justify-center gap-1 text-xs font-semibold"
        >
          <Star className="size-6" fill={item.starred ? "currentColor" : "none"} aria-hidden="true" />
          {item.starred ? "Unstar" : "Star"}
        </button>
        <button
          type="button"
          tabIndex={-1}
          onClick={() => {
            closeRow();
            onMore();
          }}
          className="flex w-[72px] shrink-0 flex-col items-center justify-center gap-1 bg-fg2 text-xs font-semibold text-bg"
        >
          <MoreHorizontal className="size-6" aria-hidden="true" />
          More
        </button>
      </div>
      <div
        ref={content}
        className="swipe-content relative bg-bg"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
        onClickCapture={onClickCapture}
        onContextMenu={(e) => {
          // A long press on touch opens our menu, not the browser's.
          if (lpRef.current?.fired || tracker.current.phase !== "idle") e.preventDefault();
        }}
      >
        {children}
      </div>
    </div>
  );
}
