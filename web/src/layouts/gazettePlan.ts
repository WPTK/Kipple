import type { Card, Feed, Folder } from "@/api/types";
import type { Favorite } from "@/lib/devicePrefs";
import { chainOf, folderTree, subtreeOf } from "@/lib/folderTree";

// The Gazette planner (docs/ui-decisions.md, "The Gazette"). A pure function: the same articles, feeds, folders,
// favorites, scope and screen class always give the same pages, whatever order the input arrives in. It knows nothing
// about React, read state or the network; a renderer draws what it returns, in the order it returns it.
//
// Pages never reflow: a page that has been returned is returned unchanged by every later call, as long as the
// loaded articles are a prefix of the server's list (sort_at DESC, id DESC). The caller must therefore always fetch
// newest first, whatever order the reader displays; with oldest-first fetching the loaded set is not a prefix.
// To make that true the planner returns no pages until LEAD_WINDOW articles are loaded or the list is complete,
// plans the front page from the newest LEAD_WINDOW articles only, cuts inner pages as fixed chunks of
// INNER_PAGE_SIZE, withholds a trailing partial chunk while the server has more, and makes the closing briefs page
// only from a partial chunk (a full last chunk stays an inner page).

/** The lead is looked for among this many of the newest articles. */
export const LEAD_WINDOW = 100;
/** A window with fewer stories than this is a quiet day. */
export const QUIET_BELOW = 6;
/** A column on the front page holds at most this many stories; the rest become briefs. */
export const STORIES_PER_COLUMN = 4;
/** Headline-only lines at the foot of the front page. */
export const FRONT_BRIEFS = 10;
/** Articles per inner page. */
export const INNER_PAGE_SIZE = 24;
/** Stories beside the lead on a "lead with image" front page. */
export const BESIDE_LEAD = 3;
/** Pictures in the photo row of a busy day. */
export const PHOTO_ROW = 3;
/** Recent articles considered when no pinned source has news and the longest headline leads. */
export const HEADLINE_WINDOW = 20;

export type FrontType = "co-leads" | "lead-image" | "big-headline" | "quiet" | "busy" | "text-only";
export type SlotKind = "lead" | "co-lead" | "story" | "photo" | "brief";

export interface Slot {
  kind: SlotKind;
  /** The article's id. */
  id: string;
  /** Zero-based column inside its block. Slots are listed in reading order, which is column by column. */
  column: number;
  /** Lead and co-lead slots only: draw the article's picture. False when the story leads as a headline. */
  picture?: boolean;
}

export interface Block {
  /** A section header, or null for a block that has none (the lead area, the stories of the front page). */
  title: string | null;
  /** 1 on a phone, otherwise 2 to 4. */
  columns: number;
  slots: Slot[];
}

export interface PagePlan {
  /** 1 is the front page. */
  number: number;
  kind: "front" | "inner" | "briefs";
  /** The front-page type; null on every other page. */
  type: FrontType | null;
  blocks: Block[];
}

export interface GazettePlan {
  pages: PagePlan[];
  /** True when every article of the list is loaded, so the last page ends the paper. */
  complete: boolean;
}

export type PlanCard = Pick<Card, "id" | "feed_id" | "title" | "image" | "sort_at">;

export interface GazetteInput {
  articles: readonly PlanCard[];
  feeds: readonly Pick<Feed, "id" | "title" | "folder_id">[];
  folders: readonly Pick<Folder, "id" | "name" | "parent_id">[];
  favorites: readonly Favorite[];
  /** The list being shown. Neither set means everything. */
  scope: { feed?: string; folder?: string };
  screen: "wide" | "phone";
  /** The server has more articles than are loaded. */
  more: boolean;
}

/** Ids compare numerically (shorter is smaller, then by text), as the server's integer ids do. */
const idCmp = (a: string, b: string) => a.length - b.length || (a < b ? -1 : a > b ? 1 : 0);
/** Newest first; equal times by id descending, the server's order (sort_at DESC, id DESC). */
const newest = (a: PlanCard, b: PlanCard) => b.sort_at - a.sort_at || idCmp(b.id, a.id);
const hasImage = (a: PlanCard) => !!a.image;

/** A block of slots spread over columns in contiguous runs, so reading order is column by column. Never more
 * columns than slots, so no column is empty. */
function block(title: string | null, kinds: { kind: SlotKind; id: string }[], columns: number): Block {
  const n = Math.max(1, Math.min(columns, kinds.length));
  return { title, columns: n, slots: kinds.map((k, i) => ({ ...k, column: Math.floor((i * n) / kinds.length) })) };
}

/** Two columns for a few stories, up to four for many. */
const columnsFor = (stories: number) => (stories >= 16 ? 4 : stories >= 8 ? 3 : 2);

/** Feeds that count as pinned: favorite feeds, and every feed inside a favorite folder. */
function pinnedFeeds(input: GazetteInput): Set<string> {
  const tree = folderTree(input.folders);
  const folders = new Set<string>();
  for (const f of input.favorites) if (f.t === "folder") for (const id of subtreeOf(tree, f.id)) folders.add(id);
  const out = new Set<string>();
  for (const f of input.favorites) if (f.t === "feed") out.add(f.id);
  for (const feed of input.feeds) if (folders.has(feed.folder_id)) out.add(feed.id);
  return out;
}

interface Front {
  type: FrontType;
  lead: PlanCard | null;
  co: PlanCard | null;
}

/**
 * Pick the front-page type; first match wins. Big headline is listed third in the design but also stands in when
 * nothing else fits (no picture lead, no pinned news, too few pictures for a photo row), so it is tried for pinned
 * news first and is the final fallback.
 */
function chooseFront(input: GazetteInput, win: readonly PlanCard[]): Front {
  const inList = !!(input.scope.feed || input.scope.folder);
  // A single feed or folder has no pin: the lead is the newest picture in that list.
  const pins = inList ? null : pinnedFeeds(input);
  const lead = win.find((a) => hasImage(a) && (!pins || pins.has(a.feed_id))) ?? null;
  if (lead) {
    const co = pins ? (win.find((a) => hasImage(a) && pins.has(a.feed_id) && a.feed_id !== lead.feed_id) ?? null) : null;
    return { type: co ? "co-leads" : "lead-image", lead, co };
  }
  const pinnedNews = pins ? win.find((a) => pins.has(a.feed_id)) : undefined;
  if (pinnedNews) return { type: "big-headline", lead: pinnedNews, co: null };
  const newestOne = win[0] ?? null;
  if (win.length < QUIET_BELOW) return { type: "quiet", lead: newestOne, co: null };
  // The photo row needs PHOTO_ROW pictures besides the lead.
  const pictures = win.filter((a) => a !== newestOne && hasImage(a)).length;
  if (pictures >= PHOTO_ROW) return { type: "busy", lead: newestOne, co: null };
  if (!win.some(hasImage)) return { type: "text-only", lead: newestOne, co: null };
  let longest = win[0]!;
  for (const a of win.slice(0, HEADLINE_WINDOW)) if (a.title.length > longest.title.length) longest = a;
  return { type: "big-headline", lead: longest, co: null };
}

function frontPage(input: GazetteInput, sorted: readonly PlanCard[], used: Set<string>): PagePlan {
  const phone = input.screen === "phone";
  // The front page reads only the lead window, so articles loaded later never change it.
  const win = sorted.slice(0, LEAD_WINDOW);
  const { type, lead, co } = chooseFront(input, win);
  const blocks: Block[] = [];
  const take = (a: PlanCard) => used.add(a.id);
  const cols = (n: number) => (phone ? 1 : n);
  const picture = type === "co-leads" || type === "lead-image";
  const pool = () => win.filter((a) => !used.has(a.id));

  if (lead) take(lead);
  if (co) take(co);
  if (type === "lead-image") {
    // The lead's picture and a second column of stories beside it.
    const beside = pool().slice(0, BESIDE_LEAD);
    beside.forEach(take);
    const slots: Slot[] = [{ kind: "lead", id: lead!.id, column: 0, picture }, ...beside.map((a) => ({ kind: "story" as const, id: a.id, column: phone ? 0 : 1 }))];
    blocks.push({ title: null, columns: beside.length ? cols(2) : 1, slots });
  } else if (lead) {
    const slots: Slot[] = [{ kind: "lead", id: lead.id, column: 0, picture }];
    if (co) slots.push({ kind: "co-lead", id: co.id, column: phone ? 0 : 1, picture });
    blocks.push({ title: null, columns: cols(slots.length), slots });
  }

  if (type === "busy") {
    const photos = pool().filter(hasImage).slice(0, PHOTO_ROW);
    photos.forEach(take);
    if (photos.length) blocks.push(block(null, photos.map((a) => ({ kind: "photo", id: a.id })), cols(photos.length)));
  }

  const rest = pool();
  const wide = type === "quiet" ? 2 : type === "busy" ? 4 : columnsFor(rest.length);
  const stories = type === "quiet" ? rest : rest.slice(0, wide * STORIES_PER_COLUMN);
  stories.forEach(take);
  if (stories.length) blocks.push(block(null, stories.map((a) => ({ kind: "story", id: a.id })), cols(wide)));

  if (type !== "quiet") {
    const briefs = pool().slice(0, FRONT_BRIEFS);
    briefs.forEach(take);
    if (briefs.length) blocks.push(block("In brief", briefs.map((a) => ({ kind: "brief", id: a.id })), cols(2)));
  }
  return { number: 1, kind: "front", type, blocks };
}

/** Which section an article belongs to: one level below the list's scope. */
function sectioner(input: GazetteInput): (a: PlanCard) => { key: string; title: string } {
  const tree = folderTree(input.folders);
  const feeds = new Map(input.feeds.map((f) => [f.id, f]));
  const byFeed = (a: PlanCard) => ({ key: `feed:${a.feed_id}`, title: feeds.get(a.feed_id)?.title ?? "Other" });
  const byFolder = (id: string) => ({ key: `folder:${id}`, title: tree.byId.get(id)?.name ?? "Other" });
  const { feed, folder } = input.scope;
  return (a) => {
    if (feed) return byFeed(a);
    const f = feeds.get(a.feed_id);
    if (!f || !tree.byId.has(f.folder_id)) return byFeed(a);
    const chain = chainOf(tree, f.folder_id);
    if (!folder) return byFolder(chain[chain.length - 1]!);
    const at = chain.indexOf(folder);
    // A feed straight in the scope folder (or one the chain cannot place) is grouped by its own name.
    return at > 0 ? byFolder(chain[at - 1]!) : byFeed(a);
  };
}

function innerPage(input: GazetteInput, chunk: readonly PlanCard[], number: number, last: boolean, sectionOf: ReturnType<typeof sectioner>): PagePlan {
  const phone = input.screen === "phone";
  if (last) {
    return { number, kind: "briefs", type: null, blocks: [block("In brief", chunk.map((a) => ({ kind: "brief", id: a.id })), phone ? 1 : 3)] };
  }
  // Sections in the order their newest article appears; the chunk is already newest first.
  const groups = new Map<string, { title: string; items: PlanCard[] }>();
  for (const a of chunk) {
    const s = sectionOf(a);
    const g = groups.get(s.key);
    if (g) g.items.push(a);
    else groups.set(s.key, { title: s.title, items: [a] });
  }
  const blocks: Block[] = [];
  for (const { title, items } of groups.values()) {
    // At most one picture feature per section: the newest article with an image, shown first.
    const feature = items.find(hasImage);
    const ordered = feature ? [feature, ...items.filter((a) => a !== feature)] : items;
    blocks.push(block(title, ordered.map((a) => ({ kind: a === feature ? "photo" : "story", id: a.id })), phone ? 1 : items.length >= 6 ? 3 : 2));
  }
  return { number, kind: "inner", type: null, blocks };
}

export function planGazette(input: GazetteInput): GazettePlan {
  const sorted = [...input.articles].sort(newest);
  const complete = !input.more;
  // Until the lead window is loaded (or nothing more exists) the front page could still change, so show nothing.
  if (!sorted.length || (!complete && sorted.length < LEAD_WINDOW)) return { pages: [], complete };

  const used = new Set<string>();
  const pages = [frontPage(input, sorted, used)];
  const rest = sorted.filter((a) => !used.has(a.id));
  const sectionOf = sectioner(input);

  // Not complete: a trailing partial chunk waits until it is full, so no page already shown changes later.
  const chunks = complete ? Math.ceil(rest.length / INNER_PAGE_SIZE) : Math.floor(rest.length / INNER_PAGE_SIZE);
  for (let i = 0; i < chunks; i++) {
    const chunk = rest.slice(i * INNER_PAGE_SIZE, (i + 1) * INNER_PAGE_SIZE);
    pages.push(innerPage(input, chunk, i + 2, complete && i === chunks - 1 && chunk.length < INNER_PAGE_SIZE, sectionOf));
  }
  return { pages, complete };
}
