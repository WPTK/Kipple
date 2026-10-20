import { useEffect, useState } from "react";
import { fetchInstance } from "@/setup/api";
import { authStore, setSignedIn } from "./client";

/**
 * The session marker's cookie: the server sets and clears it with the session cookie (docs/design.md §7). Over https
 * it carries the __Host- prefix, so the browser refuses one that another subdomain set for the parent domain (which
 * the server could never clear); over plain http the browser would refuse the prefix, so it is the bare name.
 */
export function markerCookie(protocol: string = location.protocol): string {
  return protocol === "https:" ? "__Host-kipple_signed_in" : "kipple_signed_in";
}

/** How long a launch with no marker waits for GET /api/instance before it asks for the bootstrap anyway. */
export const PROBE_MS = 2_000;

/**
 * Whether this browser holds the session marker. It is a hint, never the truth: a launch that has it asks for the
 * bootstrap at once, and the server's answer decides (a 401 then means the session really ended, and clears it).
 */
export function hasSessionMarker(): boolean {
  try {
    const name = markerCookie() + "=";
    return document.cookie.split(";").some((c) => c.trim().startsWith(name));
  } catch {
    return false;
  }
}

/**
 * Settles whether the app may ask for what needs a session. A request that answers 401 is logged to the console by
 * the browser itself, so a signed-out launch must not send one.
 *
 * With the session marker there is nothing to ask: the launch goes straight to the bootstrap. Without it (signed out,
 * or a session the marker does not cover yet) GET /api/instance, which answers 200 either way, says whether the
 * request is signed in. That question is bounded by PROBE_MS: when it fails or takes longer (offline, a stalled
 * connection, a proxy page in the way) the auth state stays unknown and the bootstrap decides, as it always did; the
 * service worker may answer that one from its stored copy.
 *
 * Returns true once that is settled.
 */
export function useSessionProbe(): boolean {
  const [settled, setSettled] = useState(() => authStore.get() !== "unknown" || hasSessionMarker());
  useEffect(() => {
    if (settled) return;
    let live = true;
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(new DOMException("GET /api/instance got no answer", "TimeoutError")), PROBE_MS);
    const done = () => {
      clearTimeout(timer);
      if (live) setSettled(true);
    };
    fetchInstance(ctl.signal).then((info) => {
      // Something else may have settled it meanwhile (a sign-in); the probe never overrides that.
      if (live && typeof info?.signed_in === "boolean" && authStore.get() === "unknown") {
        if (info.signed_in) setSignedIn();
        else authStore.set("out");
      }
      done();
    }, done);
    return () => {
      live = false;
      clearTimeout(timer);
      ctl.abort();
    };
  }, [settled]);
  return settled;
}
