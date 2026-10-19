// Where the reader was in each list, kept on the device so a reload or a reopened app comes back to the same article
// (#481). The in-page list memory (ListPane) handles going back from an article; this only fills in when the page has
// no memory of the list. The place is the article at the top of the list, not a pixel offset: after a reload the rows
// are measured again and new articles may have arrived above it, so only the article itself still points at the
// right spot. An article that has since left the list (read in Unread, trimmed) means the list starts at the top.

export const LIST_PLACE_KEY = "kipple.listPlace.v1";
/** Lists remembered: the most recently scrolled ones. */
export const LIST_PLACE_CAP = 30;

type Places = Record<string, { id: string; at: number }>;

function read(): Places {
  try {
    const v = JSON.parse(localStorage.getItem(LIST_PLACE_KEY) ?? "null") as unknown;
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Places) : {};
  } catch {
    return {};
  }
}

function write(p: Places): void {
  try {
    localStorage.setItem(LIST_PLACE_KEY, JSON.stringify(p));
  } catch {
    /* not kept: the list starts at the top next time */
  }
}

/** The article at the top of list `key` when it was last scrolled, if any. */
export function listPlace(key: string): string | null {
  const p = read()[key];
  return p && typeof p.id === "string" ? p.id : null;
}

/** Record the article at the top of list `key`; null when the list is at its top (nothing to come back to). */
export function rememberListTop(key: string, id: string | null): void {
  const p = read();
  if (id === null) {
    if (!(key in p)) return;
    delete p[key];
    return write(p);
  }
  if (p[key]?.id === id) return;
  p[key] = { id, at: Date.now() };
  const keys = Object.keys(p);
  if (keys.length > LIST_PLACE_CAP) {
    for (const k of keys.sort((a, b) => (p[a]?.at ?? 0) - (p[b]?.at ?? 0)).slice(0, keys.length - LIST_PLACE_CAP)) delete p[k];
  }
  write(p);
}
