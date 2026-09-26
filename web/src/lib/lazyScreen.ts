import { createElement, lazy, type ComponentProps, type ComponentType, type LazyExoticComponent } from "react";

/** A screen of the app (a lazy chunk) could not be downloaded: offline, or a build that has since been replaced. */
export class ChunkLoadError extends Error {
  constructor(cause: unknown) {
    super("a screen of Kipple could not be downloaded", { cause });
    this.name = "ChunkLoadError";
  }
}

/** Screens whose download failed, each with the way to start a fresh one. */
const failed = new Set<() => void>();
/** Whether "Try again" already downloaded the failed screens again once since the last success. */
let retried = false;

/**
 * React.lazy for a screen, with a way back after a failed download. React.lazy keeps a rejected load for good, so
 * the same component would throw the same error on every later render; this one swaps in a fresh React.lazy after
 * a failure (resetFailedScreens), and the next render downloads the chunk again.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any -- the props of any screen pass through unchanged
export function lazyScreen<T extends ComponentType<any>>(load: () => Promise<{ default: T }>): ComponentType<ComponentProps<T>> {
  type P = ComponentProps<T>;
  const make = (): LazyExoticComponent<T> =>
    lazy(() =>
      load().then(
        (m) => {
          retried = false;
          return m;
        },
        (e: unknown) => {
          failed.add(reset);
          throw new ChunkLoadError(e);
        },
      ),
    );
  let current = make();
  function reset(): void {
    current = make();
  }
  function Screen(props: P) {
    return createElement(current as ComponentType<P>, props);
  }
  return Screen;
}

/** Let every screen whose download failed try again on its next render. Returns whether there was one. */
export function resetFailedScreens(): boolean {
  if (failed.size === 0) return false;
  for (const reset of [...failed]) reset();
  failed.clear();
  return true;
}

/**
 * "Try again" after a failed screen download: the first time, download it again in place; if that failed too,
 * reload the whole app (the browser or the network may have kept the failure, and a reload starts clean).
 */
export function retryFailedScreens(reload: () => void = () => window.location.reload()): void {
  if (retried) {
    reload();
    return;
  }
  retried = true;
  resetFailedScreens();
}

/** Tests: forget earlier failures. */
export function resetLazyScreensForTests(): void {
  failed.clear();
  retried = false;
}
