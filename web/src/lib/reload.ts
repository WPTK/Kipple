/**
 * The query parameter a "Reload to sign in again" carries. The service worker answers a navigation that has it from
 * the network however long that takes, never with its stored shell after five seconds: the stored shell would load
 * the app, which hits the expired sign-in again, and the access proxy's login page could never be reached on a slow
 * network. The app removes the parameter from the address as it starts (stripSignInReload).
 */
export const SIGN_IN_RELOAD = "kipple-signin";

type Loc = Pick<Location, "href" | "assign">;

/** Reload the app so the access proxy in front of Kipple can show its sign-in page. */
export function reloadToSignIn(loc: Loc = window.location, now = Date.now()): void {
  const u = new URL(loc.href);
  u.searchParams.set(SIGN_IN_RELOAD, String(now));
  loc.assign(u.href);
}

/** Take the sign-in reload parameter back out of the address bar (called once at startup). */
export function stripSignInReload(loc: Pick<Location, "href"> = window.location, hist: Pick<History, "state" | "replaceState"> = window.history): void {
  const u = new URL(loc.href);
  if (!u.searchParams.has(SIGN_IN_RELOAD)) return;
  u.searchParams.delete(SIGN_IN_RELOAD);
  hist.replaceState(hist.state, "", u.pathname + u.search + u.hash);
}
