/**
 * How many list pictures the page loads at once. A browser keeps six HTTP/1.1 connections per host on plain http, and
 * the app's own requests (the event stream, the article's read) use the same six. Pictures that load without a limit
 * (a list can ask for a dozen at once) hold every connection while a slow source is waited on, and the article being
 * opened waits behind them. Three leaves the rest free.
 */
export const MAX_LIST_IMAGES = 3;

/** A picture whose source hangs gives its place back after this long; the picture keeps loading. */
export const SLOT_HOLD_MS = 8_000;

let active = 0;
const waiting: (() => void)[] = [];

function pump() {
  while (active < MAX_LIST_IMAGES && waiting.length > 0) waiting.shift()!();
}

/**
 * Asks for a place to load one picture: `go` runs when there is one (at once when one is free). The returned function
 * gives the place back (the picture loaded, failed or left the page) or withdraws the request, and may be called more
 * than once.
 */
export function takeImageSlot(go: () => void): () => void {
  let state: "waiting" | "held" | "done" = "waiting";
  let timer: ReturnType<typeof setTimeout> | undefined;
  const grant = () => {
    state = "held";
    active++;
    timer = setTimeout(release, SLOT_HOLD_MS);
    go();
  };
  if (active < MAX_LIST_IMAGES) grant();
  else waiting.push(grant);
  function release() {
    clearTimeout(timer);
    if (state === "held") active--;
    else if (state === "waiting") waiting.splice(waiting.indexOf(grant), 1);
    else return;
    state = "done";
    pump();
  }
  return release;
}
