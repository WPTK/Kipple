import { useEffect, useMemo, useRef, type MouseEvent, type PointerEvent } from "react";
import { useLatestRef } from "@/lib/useLatestRef";
import { openRowMenu } from "./rowMenu";
import { LongPress } from "./tracking";

/** How long after a long press the release's click is swallowed (the one that would open the story). */
const SUPPRESS_MS = 400;

/**
 * Opens the row menu (rowMenuStore, drawn by RowMenu) for `id`: a long press on touch, the context menu with a mouse
 * or pen. For a surface that has no swipe row around it, such as the Gazette's stories. The returned handlers go on
 * the element that holds the story. A long press cancels on movement (a scroll) and on a second finger; the click that
 * ends it opens nothing; the browser's own touch menu never shows.
 */
export function useLongPressMenu(id: string) {
  const pressRef = useRef<LongPress | null>(null);
  const fired = useRef(false);
  const touch = useRef(false);
  const swallow = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const touches = useRef(new Set<number>());
  const idRef = useLatestRef(id);

  useEffect(() => {
    const press = new LongPress(() => {
      fired.current = true;
      swallow.current = true;
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => (swallow.current = false), SUPPRESS_MS * 5);
      openRowMenu(idRef.current);
    });
    pressRef.current = press;
    const pointers = touches.current;
    return () => {
      press.cancel();
      pointers.clear();
      if (timer.current) clearTimeout(timer.current);
    };
  }, [idRef]);

  return useMemo(
    () => ({
      onPointerDown(e: PointerEvent) {
        touch.current = e.pointerType === "touch";
        if (!touch.current) return;
        touches.current.add(e.pointerId);
        if (touches.current.size > 1) return pressRef.current?.cancel(); // a pinch or two-finger scroll
        fired.current = false;
        swallow.current = false;
        pressRef.current?.start(e.clientX, e.clientY);
      },
      onPointerMove(e: PointerEvent) {
        if (touch.current) pressRef.current?.move(e.clientX, e.clientY);
      },
      onPointerUp(e: PointerEvent) {
        touches.current.delete(e.pointerId);
        pressRef.current?.cancel();
        // The click that follows a long press is still to come: swallow it, then forget.
        if (fired.current) {
          if (timer.current) clearTimeout(timer.current);
          timer.current = setTimeout(() => (swallow.current = false), SUPPRESS_MS);
        }
      },
      onPointerCancel(e: PointerEvent) {
        touches.current.delete(e.pointerId);
        pressRef.current?.cancel();
      },
      onClickCapture(e: MouseEvent) {
        if (!swallow.current) return;
        swallow.current = false;
        e.preventDefault();
        e.stopPropagation();
      },
      onContextMenu(e: MouseEvent) {
        // Touch: the long press opens our menu, not the browser's. Mouse and pen: the context menu is ours too.
        e.preventDefault();
        if (!touch.current) openRowMenu(idRef.current);
      },
    }),
    [idRef],
  );
}
