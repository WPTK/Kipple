// Telling a browser that failed under a UAT run apart from a finding about Kipple (used by common.mjs). No browser or
// Playwright import, so `node --test uat/faults.test.mjs` runs without them.

export const INTERNAL_ERROR = "WebKit encountered an internal error";

/**
 * Where a browser fault begins in one attempt's findings, or -1 when there is none. `found` lists the attempt's findings
 * in order as `{ text, jar }`, where `jar` says whether the browser context still held the session cookie it signed
 * in with when the finding was recorded (null when not known, as before sign-in). `serverAccepts` says whether the
 * server still accepts that cookie.
 *
 * A finding about WebKit's internal error is a fault. So is the first finding recorded after the cookie left the jar
 * while the server still accepts it: the server did not end the session, so most likely the browser lost the cookie
 * (not proven, so the reason says so, and the rerun must still pass every check). A session the server
 * ended (a sign-out, a revoked session) is not a fault, and neither is a cookie still in the jar. Findings before the
 * fault were recorded while the browser still worked, so they are about Kipple and stay.
 */
export function faultStart(found, serverAccepts) {
  return found.findIndex((f) => f.text.includes(INTERNAL_ERROR) || (f.jar === false && serverAccepts));
}

/** What the fault at `found[i]` was, for the log. */
export const faultReason = (f) =>
  f.text.includes(INTERNAL_ERROR)
    ? "WebKit reported an internal error"
    : "the session cookie vanished from the browser while the server still accepted it (cause unproven: the browser or Kipple)";
