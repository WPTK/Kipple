import type { Card, Feed, Folder, View } from "@/api/types";
import { feedOrder, folderTree } from "@/lib/folderTree";
import type { Section, SectionOf } from "@/lib/sections";

// The Digest planner. Pure functions: the same feeds, folders and loaded cards always give the same sections, whatever
// order they arrive in. A section is a folder or a feed (any grouping of feeds the section function makes); each shows
// its first few cards and "N more", and loads on its own, so loading a later section never changes an earlier one.
// Nothing here knows about React or the network. It is generic over the section function so another layout can group
// feeds some other way (for example by reading time) and reuse the ordering and the layout.

/** Cards per section ("Rows per section" setting). */
export const ROW_CHOICES = [3, 5, 10] as const;
/** Cards one "N more" press adds to a section. */
export const EXPAND_BATCH = 20;

export type SectionOrder = "sidebar" | "quiet";

type FeedLike = Pick<Feed, "id" | "title" | "folder_id" | "unread" | "starred_count" | "is_archive"> & { pace?: number | null };
type FolderLike = Pick<Folder, "id" | "name" | "parent_id">;

/** A section before anything is loaded: which feeds it holds, how many cards it has and how fast they arrive. */
export interface SectionDef extends Section {
  feedIds: string[];
  /** Cards in the section for the view (unread, or starred); null where the bootstrap does not say (All, Muted). */
  count: number | null;
  /** New articles a day, summed over the section's feeds; null when any of its feeds has no pace yet (the sum would count it as quiet). */
  pace: number | null;
}

/** Feeds in the order the sidebar lists them: folders in tree order (subfolders, then own feeds), then feeds in no known folder. */
function sidebarOrder(feeds: readonly FeedLike[], folders: readonly FolderLike[]): FeedLike[] {
  // The bootstrap sorts a folder's feeds by title, then id; sorting here makes the result independent of input order.
  const sorted = [...feeds].sort((a, b) => {
    const x = a.title.toLowerCase();
    const y = b.title.toLowerCase();
    return x < y ? -1 : x > y ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
  const tree = folderTree(folders);
  const own = new Map<string, string[]>();
  for (const f of sorted) {
    const list = own.get(f.folder_id);
    if (list) list.push(f.id);
    else own.set(f.folder_id, [f.id]);
  }
  const byId = new Map(sorted.map((f) => [f.id, f]));
  const ordered = feedOrder(tree, (id) => own.get(id) ?? []).map((id) => byId.get(id)!);
  const placed = new Set(ordered.map((f) => f.id));
  return [...ordered, ...sorted.filter((f) => !placed.has(f.id))];
}

/**
 * The sections of a list, in sidebar order by each section's first feed. `feeds` are the feeds in the list's scope; the
 * archive feed ("Unsubscribed (starred)") only shows in Starred, as a section of its own after the rest. Counts are
 * those of the view (Unread unless said).
 */
export function groupFeeds(feeds: readonly FeedLike[], folders: readonly FolderLike[], keyOf: SectionOf, view: View = "unread"): SectionDef[] {
  const defs = new Map<string, SectionDef>();
  const unpaced = new Set<string>();
  const archive: FeedLike[] = [];
  for (const f of sidebarOrder(feeds, folders)) {
    if (f.is_archive) {
      if (view === "starred") archive.push(f);
      continue;
    }
    add(defs, unpaced, keyOf(f.id), f, view);
  }
  for (const f of archive) add(defs, unpaced, { key: `feed:${f.id}`, title: f.title, scope: { feed: f.id } }, f, view);
  return [...defs.values()].map((d) => ({ ...d, pace: unpaced.has(d.key) ? null : d.pace }));
}

function add(defs: Map<string, SectionDef>, unpaced: Set<string>, s: Section, f: FeedLike, view: View): void {
  let d = defs.get(s.key);
  if (!d) defs.set(s.key, (d = { ...s, feedIds: [], count: view === "unread" || view === "starred" ? 0 : null, pace: null }));
  d.feedIds.push(f.id);
  if (d.count !== null) d.count += view === "starred" ? f.starred_count : f.unread;
  if (typeof f.pace === "number") d.pace = (d.pace ?? 0) + f.pace;
  else unpaced.add(s.key);
}

/**
 * "sidebar" keeps the order groupFeeds gave. "quiet": sections with a pace first, slowest first, ties in sidebar
 * order; sections without one (a section with any feed counted for under 7 days has none) follow in sidebar order.
 */
export function orderSections(defs: readonly SectionDef[], order: SectionOrder): SectionDef[] {
  if (order === "sidebar") return [...defs];
  const at = new Map(defs.map((d, i) => [d.key, i]));
  const rank = (d: SectionDef) => at.get(d.key)!;
  const paced = defs.filter((d) => d.pace !== null).sort((a, b) => a.pace! - b.pace! || rank(a) - rank(b));
  return [...paced, ...defs.filter((d) => d.pace === null)];
}

export interface DigestOptions {
  view: View;
  /** Rows per section: one of ROW_CHOICES. */
  rows: number;
  /** How many times "N more" was pressed, by section key. */
  expanded: ReadonlyMap<string, number>;
  /** The sections whose cards are being (or have been) asked for. */
  activated: ReadonlySet<string>;
}

/** What has been loaded for one section: its cards, newest first as the server sends them, and whether the server has more. */
export interface LoadedSection {
  cards: readonly Card[];
  more: boolean;
}

export type SectionState = "waiting" | "loading" | "ready";

export interface DigestSection {
  key: string;
  title: string;
  scope: Section["scope"];
  cards: Card[];
  /** Cards beyond those shown, where the count is known; null where it is not (All, Muted). */
  moreCount: number | null;
  /** More cards can be shown or loaded for this section. */
  hasMore: boolean;
  state: SectionState;
}

export interface DigestLayout {
  sections: DigestSection[];
  /** Every shown card in reading order, section by section. */
  cards: Card[];
  /** Some section is not loaded yet, or has cards it does not show. */
  more: boolean;
}

/**
 * The page. A section shows its first `rows` cards, and EXPAND_BATCH more for each press of "N more". In Unread a
 * section with nothing unread is dropped, unless it holds cards already loaded (reading must not remove a section
 * from under the reader). Cards are kept as loaded, read or not. A section not asked for yet is "waiting", one asked
 * for and not back is "loading".
 */
export function layoutDigest(defs: readonly SectionDef[], opts: DigestOptions, loaded: ReadonlyMap<string, LoadedSection>): DigestLayout {
  const sections: DigestSection[] = [];
  for (const d of defs) {
    const got = loaded.get(d.key);
    if (opts.view === "unread" && d.count === 0 && !got?.cards.length) continue;
    const limit = opts.rows + EXPAND_BATCH * (opts.expanded.get(d.key) ?? 0);
    const cards = (got?.cards ?? []).slice(0, limit);
    const state: SectionState = got ? "ready" : opts.activated.has(d.key) ? "loading" : "waiting";
    // Exact once the whole section has loaded; before that, the count the page opened with, where there is one.
    const moreCount = got && !got.more ? got.cards.length - cards.length : d.count === null ? null : Math.max(0, d.count - cards.length);
    const hasMore = got ? got.cards.length > cards.length || got.more : true;
    sections.push({ key: d.key, title: d.title, scope: d.scope, cards, moreCount, hasMore, state });
  }
  return { sections, cards: sections.flatMap((s) => s.cards), more: sections.some((s) => s.hasMore) };
}
