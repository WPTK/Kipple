import type { Feed } from "@/api/types";

/** A feed's address without the scheme or a trailing slash: short, and still what makes it this feed. */
export function shortAddress(url: string): string {
  return url.replace(/^https?:\/\//i, "").replace(/\/$/, "");
}

/**
 * The feeds whose title another feed in `feeds` also has, each mapped to its short address: a row or a list shows it
 * so feeds with the same title can be told apart (issue #484). Titles compare trimmed and case-insensitively.
 */
export function sameTitleHints(feeds: readonly Pick<Feed, "id" | "title" | "url">[]): Map<string, string> {
  const byTitle = new Map<string, number>();
  const key = (t: string) => t.trim().toLowerCase();
  for (const f of feeds) byTitle.set(key(f.title), (byTitle.get(key(f.title)) ?? 0) + 1);
  const out = new Map<string, string>();
  for (const f of feeds) if ((byTitle.get(key(f.title)) ?? 0) > 1 && f.url) out.set(f.id, shortAddress(f.url));
  return out;
}
