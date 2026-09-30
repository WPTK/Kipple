import type { Release } from "./whatsNewParse";

/** The newest releases of the changelog, from a chunk that is only fetched when "What's new" is about to show. */
export async function loadReleases(): Promise<Release[]> {
  return (await import("virtual:kipple-whats-new")).default;
}
