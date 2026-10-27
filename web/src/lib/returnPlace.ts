import { useEffect, useLayoutEffect, useRef, useState, type Dispatch, type RefObject, type SetStateAction } from "react";
import { useLocation, useNavigationType } from "react-router";

// Where a page that is not a list of articles was left, for the way back: how far it was scrolled and which row was the
// Tab stop. Going back to it (history POP, from an article or the browser's Back) puts both back; opening it afresh
// starts at the top. A list of articles keeps its own place (ListPane); this is for the pages made of blocks.
//
// The place belongs to a history entry (the router's location key), not to the page's name: pressing Back several
// times lands on older entries of the same page, each with the place it was left at.

interface Place {
  offset: number;
  stop: string | undefined;
  /** How far below the top of the scrolling box the stop row sat, so the row can be put back where it was if the rows above it changed. */
  anchor: number | undefined;
}

const places = new Map<string, Place>();
const LIMIT = 100;

function remember(id: string, place: Place): void {
  places.delete(id);
  places.set(id, place);
  if (places.size > LIMIT) places.delete(places.keys().next().value as string);
}

const rowOf = (el: HTMLElement, stop: string | undefined): HTMLElement | null => {
  if (!stop) return null;
  const id = CSS.escape(stop);
  return el.querySelector<HTMLElement>(`[data-item-id="${id}"], [data-stop-id="${id}"]`);
};

const anchorOf = (el: HTMLElement, stop: string | undefined): number | undefined => {
  const row = rowOf(el, stop);
  return row ? row.getBoundingClientRect().top - el.getBoundingClientRect().top : undefined;
};

/** How long after a way back the rows may still be settling (a refetch, pictures): the place follows them until the reader moves it. */
const SETTLE_MS = 1500;

/**
 * The scrolling box of the page `name` (put the returned ref on it), the row of the page that last had focus (its id, from
 * data-item-id or data-stop-id) with a setter, and the remembering of that and of the scroll position. `ready` turns true
 * once the page has drawn its rows; when it does after a way back, the scroll position returns and the remembered row
 * takes focus. If the rows above that row change size while the page settles (the data was fetched again), the row is
 * kept where it was left until the reader scrolls.
 */
export function useReturnPlace<T extends HTMLElement>(name: string, ready: boolean): { scroller: RefObject<T | null>; stop: string | undefined; setStop: Dispatch<SetStateAction<string | undefined>> } {
  const scroller = useRef<T>(null);
  const back = useNavigationType() === "POP";
  const id = `${name}:${useLocation().key}`;
  const [start] = useState<Place | undefined>(() => {
    const was = back ? places.get(id) : undefined;
    places.delete(id);
    return was;
  });
  const [stop, setStop] = useState<string | undefined>(start?.stop);
  const done = useRef(false);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!ready || done.current || !el) return;
    done.current = true;
    if (!start) return;
    const put = () => {
      el.scrollTop = start.offset;
      const row = rowOf(el, start.stop);
      if (row && start.anchor !== undefined) el.scrollTop += row.getBoundingClientRect().top - el.getBoundingClientRect().top - start.anchor;
    };
    put();
    if (start.stop) rowOf(el, start.stop)?.querySelector<HTMLElement>("a")?.focus({ preventScroll: true });
    // The rows may still change under the place (the data is fetched again on the way back): keep the row where it was
    // left until the reader moves the page or the page has settled.
    const content = el.firstElementChild;
    if (!content || typeof ResizeObserver === "undefined") return;
    const settle = new ResizeObserver(put);
    settle.observe(content);
    const end = () => {
      settle.disconnect();
      clearTimeout(timer);
      for (const type of ["wheel", "touchstart", "pointerdown", "keydown"]) el.removeEventListener(type, end);
    };
    const timer = setTimeout(end, SETTLE_MS);
    for (const type of ["wheel", "touchstart", "pointerdown", "keydown"]) el.addEventListener(type, end, { passive: true, once: true });
    return end;
  }, [ready, start]);
  useEffect(() => {
    const el = scroller.current;
    if (!ready || !el) return;
    const save = () => remember(id, { offset: el.scrollTop, stop: places.get(id)?.stop, anchor: anchorOf(el, places.get(id)?.stop) });
    save();
    el.addEventListener("scroll", save, { passive: true });
    return () => el.removeEventListener("scroll", save);
  }, [id, ready]);
  useEffect(() => {
    const was = places.get(id);
    const el = scroller.current;
    if (ready && was) remember(id, { ...was, stop, anchor: el ? anchorOf(el, stop) : undefined });
  }, [id, ready, stop]);
  return { scroller, stop, setStop };
}
