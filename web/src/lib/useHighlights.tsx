import { useEffect, useMemo } from "react";
import { searchHighlightStore, setSearchHighlight, type SearchOpts } from "./searchTerms";
import { useBootstrap } from "@/api/queries";
import { useDevicePrefs } from "./devicePrefs";
import { compileHighlights, groupsFor, highlightRanges, highlightStore, segments, type Group, type HighlightField } from "./highlight";
import { useStore } from "./store";

/** Keep the compiled highlight rules current: from the bootstrap, and off when "Highlight keywords" is off. Mount once. */
export function useSyncHighlights(): void {
  const boot = useBootstrap();
  const on = useDevicePrefs().highlightKeywords;
  const rules = boot.data?.highlights;
  // Only which folder each feed is in matters here. A `counts` event replaces the feeds array (new unread numbers)
  // every few seconds; keying on ids and folder ids keeps the store, and so every <mark> in the article and the
  // list, untouched by it (a rebuild would strip and redraw them and collapse the reader's text selection).
  const feedKey = (boot.data?.feeds ?? []).map((f) => `${f.id}:${f.folder_id}`).join(",");
  const feeds = useMemo(
    () => new Map<string, string>(feedKey ? feedKey.split(",").map((p) => p.split(":") as [string, string]) : []),
    [feedKey],
  );
  const compiled = useMemo(() => (on ? compileHighlights(rules) : []), [on, rules]);
  useEffect(() => {
    const cur = highlightStore.get();
    if (cur.groups === compiled && cur.feeds === feeds) return;
    highlightStore.set({ groups: compiled, feeds });
  }, [compiled, feeds]);
}

const NONE: Group[] = [];

/** The rules that mark this field of an article of this feed. A stable empty array when there are none. */
export function useGroups(field: HighlightField, feedId: string | undefined): Group[] {
  const s = useStore(highlightStore);
  const search = useStore(searchHighlightStore);
  return useMemo(() => {
    if (s.groups.length === 0 && search.groups.length === 0) return NONE;
    const folder = feedId ? s.feeds.get(feedId) : undefined;
    const got = groupsFor(s.groups, field, feedId && folder !== undefined ? { id: feedId, folder_id: folder } : undefined);
    // The words of the search on screen (results and the article opened from them), on top of the keyword rules.
    const found = search.groups.filter((g) => g.fields.has(field));
    const all = found.length ? [...got, ...found] : got;
    return all.length ? all : NONE;
  }, [s, search, field, feedId]);
}

/** Draw the words of the search that a list (or an article opened from it) belongs to. No `q` clears them. */
export function useSearchHighlight(q: string | undefined, opts: SearchOpts = {}): void {
  const { fallback, typing } = opts;
  useEffect(() => {
    setSearchHighlight(q, { fallback, typing });
  }, [q, fallback, typing]);
}

/** Most marks in one title or excerpt. */
const LIST_CAP = 12;

/**
 * Text with the words of the Highlight filters marked. Plain text when nothing applies, so a list with no
 * highlight rules renders exactly as before. Built from React text nodes and <mark> elements, never HTML.
 */
export function Hl({ text, field, feedId }: { text: string; field: HighlightField; feedId: string | undefined }) {
  const groups = useGroups(field, feedId);
  if (groups === NONE || !text) return <>{text}</>;
  const ranges = highlightRanges(text, groups);
  if (ranges.length === 0) return <>{text}</>;
  return (
    <>
      {segments(text, ranges, LIST_CAP).map((s, i) =>
        s.mark ? (
          <mark key={i} className="kp-hl">
            {s.text}
          </mark>
        ) : (
          s.text
        ),
      )}
    </>
  );
}
