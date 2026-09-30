import { compareVersions, parseSemver } from "./semver";
import type { Release } from "./whatsNewParse";

/** The setting that remembers the newest version whose changes the reader has been shown (account-wide). */
export const WHATS_NEW_SEEN = "ui.whats_new_seen";

/**
 * Which releases to show after an upgrade. `current` is the running bundle's version and `seen` the stored setting.
 *  - a development bundle ("dev") or a version this cannot order: nothing;
 *  - nothing seen yet: only the current release's own section (there is no way to know what came before);
 *  - otherwise every release newer than `seen`, up to and including `current`, newest first.
 * Prereleases count: 0.5.0-beta.2 is newer than 0.5.0-beta.1 and older than 0.5.0.
 */
export function releasesToShow(releases: Release[], seen: string, current: string): Release[] {
  if (!parseSemver(current)) return [];
  const notAfterCurrent = releases.filter((r) => (compareVersions(r.version, current) ?? 1) <= 0);
  if (!seen) return notAfterCurrent.slice(0, 1);
  if ((compareVersions(seen, current) ?? 1) >= 0) return [];
  return notAfterCurrent.filter((r) => (compareVersions(r.version, seen) ?? -1) > 0);
}

/**
 * Whether the panel is due at all, which is decided before the changelog is even loaded. An install that has never
 * had a feed (a first run: the setup wizard, the first-run screen) is not an upgrade: it is marked as seen silently.
 */
export type WhatsNewDue = "show" | "mark" | "no";

export function whatsNewDue(seen: string, current: string, hasFeeds: boolean): WhatsNewDue {
  if (!parseSemver(current)) return "no";
  if (!seen) return hasFeeds ? "show" : "mark";
  const c = compareVersions(seen, current);
  return c !== null && c < 0 ? "show" : "no";
}
