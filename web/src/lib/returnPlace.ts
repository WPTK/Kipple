import { useEffect, useLayoutEffect, useRef, useState, type Dispatch, type RefObject, type SetStateAction } from "react";
import { useNavigationType } from "react-router";

// Where a page that is not a list of articles was left, for the way back: how far it was scrolled and which row was the
// Tab stop. Going back to it (history POP, from an article or the browser's Back) puts both back; opening it afresh
// starts at the top. A list of articles keeps its own place (ListPane); this is for the pages made of blocks.

interface Place {
  offset: number;
  stop: string | undefined;
}

const places = new Map<string, Place>();

/**
 * The scrolling box of the page `name` (put the returned ref on it), the row of the page that last had focus (its id, from
 * data-item-id or data-stop-id) with a setter, and the remembering of that and of the scroll position. `ready` turns true
 * once the page has drawn its rows; when it does after a way back, the scroll position returns and the remembered row
 * takes focus.
 */
export function useReturnPlace<T extends HTMLElement>(name: string, ready: boolean): { scroller: RefObject<T | null>; stop: string | undefined; setStop: Dispatch<SetStateAction<string | undefined>> } {
  const scroller = useRef<T>(null);
  const back = useNavigationType() === "POP";
  const [start] = useState<Place | undefined>(() => {
    const was = back ? places.get(name) : undefined;
    places.delete(name);
    return was;
  });
  const [stop, setStop] = useState<string | undefined>(start?.stop);
  const done = useRef(false);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!ready || done.current || !el) return;
    done.current = true;
    if (!start) return;
    el.scrollTop = start.offset;
    if (!start.stop) return;
    const id = CSS.escape(start.stop);
    el.querySelector<HTMLElement>(`[data-item-id="${id}"] a, [data-stop-id="${id}"] a`)?.focus({ preventScroll: true });
  }, [ready, start]);
  useEffect(() => {
    const el = scroller.current;
    if (!ready || !el) return;
    const save = () => places.set(name, { offset: el.scrollTop, stop: places.get(name)?.stop });
    save();
    el.addEventListener("scroll", save, { passive: true });
    return () => el.removeEventListener("scroll", save);
  }, [name, ready]);
  useEffect(() => {
    const was = places.get(name);
    if (ready && was) places.set(name, { ...was, stop });
  }, [name, ready, stop]);
  return { scroller, stop, setStop };
}
