import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/ui/button";
import { useLatestRef } from "@/lib/useLatestRef";

/** How many rows a long feed tree draws at first, and how many more each time its end comes near (issue #463). */
export const REVEAL_STEP = 300;

export interface Reveal {
  /** Whether the row with this key is drawn: the first `limit` keys in screen order are. */
  shown: (key: string) => boolean;
  /** Whether the key is in the list at all (drawn or not; a row in a collapsed folder is not in it). */
  has: (key: string) => boolean;
  /** How many rows are not drawn yet. */
  left: number;
  /** Draw the next step. With `focus`, the first row it adds takes the focus (the element marked `data-reveal-key`). */
  more: (focus?: boolean) => void;
  /** Draw every row (End in a tree). With `focus`, the last row takes the focus. */
  all: (focus?: boolean) => void;
  /**
   * Keep the row with this key drawn, with every row before it, once it is in the list and wherever it moves (a row
   * just moved down a place or just created). The hold ends when the row has been on screen and is then scrolled out
   * of view, so a row far down a long list does not keep everything above it drawn for good.
   */
  ensure: (key: string) => void;
  /** Whether the row with this key is in the list but not drawn yet (unlike a row in a collapsed folder). */
  past: (key: string) => boolean;
  /** The last of these items that is drawn, in the order given (where a drop at the end of a list lands its marker). */
  lastDrawn: <T>(items: readonly T[], key: (item: T) => string) => T | null;
  /** The first of these items that is in the list but not drawn (where a drop at the end of a list lands). */
  firstPast: <T>(items: readonly T[], key: (item: T) => string) => T | null;
}

/**
 * Draws a long list of rows a step at a time, in screen order: the rows after the first `limit` keys are left out, and
 * `RevealMore` after the list draws the next step when it scrolls near (or is pressed). The nested markup, the drag and
 * drop and the tree semantics stay as they are; only how many rows are drawn changes, so a library of thousands of
 * feeds opens as fast as a small one. Without IntersectionObserver every row is drawn. The result keeps its identity
 * until the list or the drawn count changes, so what depends on it is not redone on every render.
 */
export function useReveal(keys: readonly string[]): Reveal {
  const [limit, setLimit] = useState(() => (typeof IntersectionObserver === "function" ? REVEAL_STEP : Infinity));
  const [kept, setKept] = useState<string | null>(null);
  // The key whose row takes the focus once the next render has drawn it.
  const focusKey = useRef<string | null>(null);
  const index = useMemo(() => new Map(keys.map((k, i) => [k, i])), [keys]);
  const keptAt = kept === null ? -1 : (index.get(kept) ?? -1);
  const drawn = Math.max(limit, keptAt + 1);
  const shownSet = useMemo(() => (drawn >= keys.length ? null : new Set(keys.slice(0, drawn))), [keys, drawn]);
  useLayoutEffect(() => {
    const k = focusKey.current;
    if (!k) return;
    focusKey.current = null;
    document.querySelector<HTMLElement>(`[data-reveal-key="${CSS.escape(k)}"]`)?.focus();
  }, [limit]);
  // A held row ends its hold once it was on screen and the reader has scrolled up above every row the hold draws (the
  // first of them is below the view), so the rows that go are none the reader is looking at and nothing on screen moves.
  useEffect(() => {
    if (kept === null || keptAt < limit || typeof IntersectionObserver !== "function") return;
    const at = (k: string) => document.querySelector<HTMLElement>(`[data-reveal-key="${CSS.escape(k)}"]`);
    const el = at(kept);
    const first = at(keys[limit] ?? kept);
    if (!el || !first) return;
    let seen = false;
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.target === el && e.isIntersecting) seen = true;
          const bottom = e.rootBounds?.bottom ?? window.innerHeight;
          if (e.target === first && seen && !e.isIntersecting && e.boundingClientRect.top >= bottom) setKept(null);
        }
      },
      { root: el.closest<HTMLElement>("[data-reveal-root]") },
    );
    io.observe(el);
    if (first !== el) io.observe(first);
    return () => io.disconnect();
  }, [kept, keptAt, limit, keys]);
  return useMemo(() => {
    const shown = (key: string) => !shownSet || shownSet.has(key);
    const past = (key: string) => (index.get(key) ?? -1) >= drawn;
    return {
      shown,
      has: (key) => index.has(key),
      left: Math.max(0, keys.length - drawn),
      more: (focus) => {
        const next = drawn + REVEAL_STEP;
        // The focus request is answered when the limit changes; with no change it would stay armed for the next one.
        if (focus && next !== limit) focusKey.current = keys[drawn] ?? null;
        setLimit(next);
      },
      all: (focus) => {
        if (focus && limit !== Infinity) focusKey.current = keys.at(-1) ?? null;
        setLimit(Infinity);
      },
      ensure: setKept,
      past,
      lastDrawn: (items, key) => items.findLast((x) => !past(key(x)) && index.has(key(x))) ?? null,
      firstPast: (items, key) => items.find((x) => past(key(x))) ?? null,
    };
  }, [keys, index, drawn, limit, shownSet]);
}
/**
 * What follows a list that is not all drawn: a button that draws the next step, pressed by itself when it scrolls
 * within a screen of the closest `[data-reveal-root]` scroller. A press moves the focus to the first row it adds.
 */
export function RevealMore({ reveal, noun }: { reveal: Reveal; noun: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const more = useLatestRef(reveal.more);
  const left = reveal.left;
  useEffect(() => {
    const el = ref.current;
    if (!el || left === 0 || typeof IntersectionObserver !== "function") return;
    const root = el.closest<HTMLElement>("[data-reveal-root]");
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) more.current(el.contains(document.activeElement));
      },
      { root, rootMargin: "100% 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [left, more]);
  if (left === 0) return null;
  return (
    <div ref={ref} className="px-3 py-2">
      <Button variant="ghost" onClick={() => more.current(true)}>
        Show more {noun} ({left.toLocaleString()} not shown yet)
      </Button>
    </div>
  );
}
