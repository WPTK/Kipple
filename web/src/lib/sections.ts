import type { Feed, Folder, Scope } from "@/api/types";
import { chainOf, folderTree } from "@/lib/folderTree";
import { BREAK_LENGTHS, READING_LENGTH_LABELS } from "@/lib/readingLength";

// The one section rule: a list is cut into sections one level below its scope. The Gazette and the Digest both use it,
// so a folder's stories are grouped the same way wherever they are drawn.

/** One section of a list: a folder (with everything inside it), a single feed, or one reading length of the list (the Coffee break). */
export interface Section {
  /** Stable across renders and loads; also the key a section's own state is kept under. */
  key: string;
  title: string;
  /** What a list of just this section is scoped to. */
  scope: Pick<Scope, "feed" | "folder" | "length">;
}

/**
 * The Coffee break's sections: the same list as `scope` (its feed or folder), cut by reading time, in the order the page
 * draws them. A length is a section's whole definition (the server filters by it), so no feed or folder is grouped.
 */
export function byLength(scope: Pick<Scope, "feed" | "folder">): Section[] {
  const of = scope.feed ? { feed: scope.feed } : scope.folder ? { folder: scope.folder } : {};
  return BREAK_LENGTHS.map((length) => ({ key: `len:${length}`, title: READING_LENGTH_LABELS[length], scope: { ...of, length } }));
}

/** Which section a feed belongs to, by the feed's id. */
export type SectionOf = (feedId: string) => Section;

type FeedLike = Pick<Feed, "id" | "title" | "folder_id">;

/** Every feed is its own section. A feed that is not in the list is "Other". */
export function byFeed(feeds: readonly FeedLike[]): SectionOf {
  const byId = new Map(feeds.map((f) => [f.id, f]));
  return (id) => ({ key: `feed:${id}`, title: byId.get(id)?.title ?? "Other", scope: { feed: id } });
}

/**
 * The sections one level below `scope`: on the library the top-level folders, in a folder its subfolders, in a feed
 * the feed itself. A feed straight in the scope folder (or one the folder chain cannot place) is a section of its own.
 */
export function byFolder(folders: readonly Pick<Folder, "id" | "name" | "parent_id">[], feeds: readonly FeedLike[], scope: Pick<Scope, "feed" | "folder">): SectionOf {
  const tree = folderTree(folders);
  const own = byFeed(feeds);
  const byId = new Map(feeds.map((f) => [f.id, f]));
  const folderSection = (id: string): Section => ({ key: `folder:${id}`, title: tree.byId.get(id)?.name ?? "Other", scope: { folder: id } });
  return (id) => {
    if (scope.feed) return own(id);
    const f = byId.get(id);
    if (!f || !tree.byId.has(f.folder_id)) return own(id);
    const chain = chainOf(tree, f.folder_id);
    if (!scope.folder) return folderSection(chain[chain.length - 1]!);
    const at = chain.indexOf(scope.folder);
    return at > 0 ? folderSection(chain[at - 1]!) : own(id);
  };
}
