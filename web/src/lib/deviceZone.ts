import { useSyncExternalStore } from "react";
import { createStore } from "./store";

// The stats time zone is empty until someone chooses one, and the server uses UTC meanwhile. A device whose own zone is
// something else is offered that zone, once and explicitly (Settings > Stats, and a line on the Stats screen); nothing is
// written until the person says yes. "Not now" on the Stats screen is remembered per device here, in localStorage: the
// device profile has no key for it and gets none, because the answer is only about this device.

const UTC_NAMES = /^(?:etc\/)?(?:utc|uct|universal|zulu|greenwich|gmt(?:[+-]?0)?)$/i;

/** True for a name that means UTC under another spelling, which is not worth offering over the default. */
export const isUtcName = (zone: string): boolean => UTC_NAMES.test(zone.trim());

/** The browser's own time zone when it is a valid zone other than UTC, else null. */
export function deviceZone(): string | null {
  try {
    const zone = new Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (!zone || isUtcName(zone)) return null;
    new Intl.DateTimeFormat(undefined, { timeZone: zone }); // throws RangeError for a name the browser does not know
    return zone;
  } catch {
    return null;
  }
}

const KEY = "kipple.zoneOfferDismissed.v1";

const dismissedStore = createStore<boolean>(
  (() => {
    try {
      return localStorage.getItem(KEY) === "1";
    } catch {
      return false;
    }
  })(),
);

/** Remembers on this device that the Stats screen's offer was declined. */
export function dismissZoneOffer(): void {
  dismissedStore.set(true);
  try {
    localStorage.setItem(KEY, "1");
  } catch {
    /* not remembered; the line shows again next visit */
  }
}

/** Forgets a dismissal (tests; nothing in the app calls it). */
export function resetZoneOffer(): void {
  dismissedStore.set(false);
  try {
    localStorage.removeItem(KEY);
  } catch {
    /* nothing to forget */
  }
}

/**
 * The zone to offer on the Stats screen: the device's zone when the saved one is empty (`saved === ""`), the device zone
 * is not UTC and "Not now" has not been said here. Null when nothing is to be shown, including while the saved value is
 * not known yet (`saved` undefined).
 */
export function useStatsZoneOffer(saved: unknown): string | null {
  const dismissed = useSyncExternalStore(dismissedStore.subscribe, dismissedStore.get, dismissedStore.get);
  if (saved !== "" || dismissed) return null;
  return deviceZone();
}
