import { useEffect, useMemo } from "react";
import { useBootstrap } from "@/api/queries";
import { useDevicePrefs } from "./devicePrefs";
import { compileHighlights, groupsFor, highlightRanges, highlightStore, segments, type Group, type HighlightField } from "./highlight";
import { useStore } from "./store";

/** Keep the compiled highlight rules current: from the bootstrap, and off when "Highlight keywords" is off. Mount once. */
export function useSyncHighlights(): void {
  const boot = useBootstrap();
  const on = useDevicePrefs().highlightKeywords;
  const rules = boot.data?.highlights;
  const feedList = boot.data?.feeds;
  const compiled = useMemo(() => (on ? compileHighlights(rules) : []), [on, rules]);
  useEffect(() => {
    highlightStore.set({ groups: compiled, feeds: new Map((feedList ?? []).map((f) => [f.id, f.folder_id])) });
  }, [compiled, feedList]);
}

const NONE: Group[] = [];

/** The rules that mark this field of an article of this feed. A stable empty array when there are none. */
export function useGroups(field: HighlightField, feedId: string | undefined): Group[] {
  const s = useStore(highlightStore);
  return useMemo(() => {
    if (s.groups.length === 0) return NONE;
    const folder = feedId ? s.feeds.get(feedId) : undefined;
    const got = groupsFor(s.groups, field, feedId && folder !== undefined ? { id: feedId, folder_id: folder } : undefined);
    return got.length ? got : NONE;
  }, [s, field, feedId]);
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
