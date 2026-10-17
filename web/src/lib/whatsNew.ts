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
 * Whether the panel is due at all, which is decided before the changelog is even loaded: the running version is a
 * release and is newer than the one on record. A new account is created with the running version on record (the
 * server does that), so a first run never has anything to show; an account with no record at all (one that predates
 * the setting) is treated as upgrading and shown the current release.
 */
export function whatsNewDue(seen: string, current: string): boolean {
  if (!parseSemver(current)) return false;
  if (!seen) return true;
  const c = compareVersions(seen, current);
  return c !== null && c < 0;
}
