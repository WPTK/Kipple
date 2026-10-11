import { useLayoutEffect, useRef, type MutableRefObject } from "react";

/**
 * A ref that always holds the value of the latest committed render, for handlers that outlive the render that
 * created them (key, pointer and timer callbacks, saves on unmount).
 *
 * The ref is refreshed in a layout effect, not a passive one: it runs in the same task as the commit, so an event
 * that arrives once the screen shows the new state reads the new value. A passive effect may run only after the
 * browser has handled the next event (React defers it after a render scheduled as a transition, such as a
 * navigation), and that event would then act on the previous render's value. Use this hook instead of writing
 * `useEffect(() => { ref.current = x })`; `src/lib/latestRef.test.ts` fails the build on the passive form.
 */
export function useLatestRef<T>(value: T): MutableRefObject<T> {
  const ref = useRef(value);
  useLayoutEffect(() => {
    ref.current = value;
  });
  return ref;
}
