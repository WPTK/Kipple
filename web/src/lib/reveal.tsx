import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/ui/button";
import { useLatestRef } from "@/lib/useLatestRef";

/** How many rows a long feed tree draws at first, and how many more each time its end comes near (issue #463). */
export const REVEAL_STEP = 300;

export interface Reveal {
  /** Whether the row with this key is drawn: the first `limit` keys in screen order are. */
  shown: (key: string) => boolean;
  /** How many rows are not drawn yet. */
  left: number;
  /** Draw the next step. With `focus`, the first row it adds takes the focus (the element marked `data-reveal-key`). */
  more: (focus?: boolean) => void;
  /** Draw every row (End in a tree). */
  all: () => void;
  /** Keep the row with this key drawn, with every row before it, once it is in the list and wherever it moves (a row
   * just moved down a place or just created). */
  ensure: (key: string) => void;
  /** Whether the row with this key is in the list but not drawn yet (unlike a row in a collapsed folder). */
  past: (key: string) => boolean;
}

/**
 * Draws a long list of rows a step at a time, in screen order: the rows after the first `limit` keys are left out, and
 * `RevealMore` after the list draws the next step when it scrolls near (or is pressed). The nested markup, the drag and
 * drop and the tree semantics stay as they are; only how many rows are drawn changes, so a library of thousands of
 * feeds opens as fast as a small one. Without IntersectionObserver every row is drawn.
 */
export function useReveal(keys: readonly string[]): Reveal {
  const [limit, setLimit] = useState(() => (typeof IntersectionObserver === "function" ? REVEAL_STEP : Infinity));
  const [kept, setKept] = useState<string | null>(null);
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
  return {
    shown: (key) => !shownSet || shownSet.has(key),
    left: Math.max(0, keys.length - drawn),
    more: (focus) => {
      if (focus) focusKey.current = keys[drawn] ?? null;
      setLimit(drawn + REVEAL_STEP);
    },
    all: () => setLimit(Infinity),
    ensure: setKept,
    past: (key) => (index.get(key) ?? -1) >= drawn,
  };
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
