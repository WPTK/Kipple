import { useSyncExternalStore } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { settingsKey, type SettingsResponse } from "@/api/admin";
import { keys } from "@/api/queries";
import type { Bootstrap } from "@/api/types";
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

const DISMISSED_STORAGE_NAME = "kipple.zoneOfferDismissed.v1";

const readDismissed = (): boolean => {
  try {
    return localStorage.getItem(DISMISSED_STORAGE_NAME) === "1";
  } catch {
    return false;
  }
};

const dismissedStore = createStore<boolean>(readDismissed());

// A dismissal made in another tab of this browser closes the offer here too (the event fires in the tabs that did not write).
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key === null || e.key === DISMISSED_STORAGE_NAME) dismissedStore.set(readDismissed());
  });
}

/** Remembers on this device that the Stats screen's offer was declined. */
export function dismissZoneOffer(): void {
  dismissedStore.set(true);
  try {
    localStorage.setItem(DISMISSED_STORAGE_NAME, "1");
  } catch {
    /* not remembered; the line shows again next visit */
  }
}

/** Forgets a dismissal (tests; nothing in the app calls it). */
export function resetZoneOffer(): void {
  dismissedStore.set(false);
  try {
    localStorage.removeItem(DISMISSED_STORAGE_NAME);
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

/**
 * Accepts the offer. The saved zone is read again from the server first: when another device chose one in the meantime the
 * caches take it (so the offer goes, here and wherever it shows) and nothing is written, so a zone someone chose is never
 * overwritten by a device's guess. Only a zone still empty is saved, through `save`, which is the ordinary write.
 * Returns "taken" when a zone was already chosen. A zone the server does not know is refused by `save` in the usual words.
 */
export async function acceptDeviceZone(qc: QueryClient, zone: string, save: (zone: string) => unknown): Promise<"saved" | "taken"> {
  const live = await api<SettingsResponse>("/api/settings", { quiet: true });
  if ((live.values.tz ?? "") !== "") {
    qc.setQueryData(settingsKey, live);
    qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => (old ? { ...old, settings: live.values } : old));
    void qc.invalidateQueries({ queryKey: ["stats"] });
    return "taken";
  }
  await save(zone);
  return "saved";
}
