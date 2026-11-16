import type { Feed } from "@/api/types";
import { subtreeOf, type FolderTree } from "./folderTree";

/** A library with more feeds than this offers "Find a feed": below it the whole list fits on a screen or two. */
export const FIND_MIN_FEEDS = 12;

/** What the feed filter shows: the wording the Feed health search uses, and the same on every screen that filters feeds. */
export const feedCountText = (shown: number, total: number): string => `Showing ${shown} of ${total} feeds`;

/** The feeds a name filter keeps, by id; `null` means no filter. */
export type FeedMatch = ReadonlySet<string> | null;

/**
 * The feeds whose title, short address or folder name holds the text (case-insensitive, ends trimmed). A folder whose
 * name matches keeps every feed of its subtree, so a folder is found by its name and opens onto its feeds. A screen
 * shows a folder when its subtree holds a kept feed, so the folders above a match stay as its context. One pass over
 * the feeds and the folders: typing never asks the server for anything.
 */
export function feedMatch(tree: FolderTree, feeds: readonly Feed[], query: string): FeedMatch {
  const needle = query.trim().toLowerCase();
  if (!needle) return null;
  const keep = new Set<string>();
  const wholeFolders = new Set<string>();
  for (const id of tree.preorder) {
    const folder = tree.byId.get(id);
    if (folder?.name.toLowerCase().includes(needle)) {
      for (const inside of subtreeOf(tree, id)) wholeFolders.add(inside);
    }
  }
  for (const f of feeds) {
    if (wholeFolders.has(f.folder_id) || f.title.toLowerCase().includes(needle) || (f.address_hint ?? "").toLowerCase().includes(needle)) keep.add(f.id);
  }
  return keep;
}
