// Types for the UI JSON API (docs/design.md section 7). Ids are strings in
// every JSON body; times are unix seconds.

export type View = "unread" | "all" | "starred" | "muted";

export interface Card {
  id: string;
  feed_id: string;
  title: string;
  url: string;
  author: string;
  excerpt: string;
  image: string | null;
  published_at: number;
  sort_at: number;
  read: boolean;
  starred: boolean;
  word_count: number;
  reading_minutes: number | null;
  snippet?: string;
  origin_title: string | null;
  /** Source name to show: the feed title, or the origin title for archived items. */
  source: string;
  /** The muting filter's id, or null. Set on every muted item (they are always read). */
  muted_by: string | null;
  /** The muting filter's name; null when the filter was deleted (the item stays muted, orphaned). */
  muted_by_name: string | null;
}

export interface Fulltext {
  /** 1, 0, or null (follow the feed). */
  mode: 0 | 1 | null;
  effective: 0 | 1;
  available: boolean;
  error: string | null;
}

export interface ItemDetail extends Card {
  content_html: string;
  fulltext: Fulltext;
  enclosures: unknown;
  feed: { id: string; title: string; site_url: string };
  trimmed: boolean;
}

export interface ItemsPage {
  items: Card[];
  next_cursor: string | null;
  /** The store's highest committed id when the page was read; sent back as mark-read `max_id`. */
  as_of?: string;
  /** A text search with no exact match that shows partial (prefix or OR) matches instead (docs/design.md 2.4). */
  fallback?: boolean;
}

export interface Folder {
  id: string;
  name: string;
  position: number;
  is_default: boolean;
  unread: number;
}

export interface Feed {
  id: string;
  folder_id: string;
  title: string;
  site_url: string;
  icon: string | null;
  unread: number;
  status: string;
  /** The feed's own "fetch full article" flag (what the feed editor edits). */
  fulltext: boolean;
  /** What new items really get: true while the global "fetch full article for every feed" setting is on. */
  fulltext_effective?: boolean;
  retention: number | null;
  interval_minutes: number | null;
  /** This feed's own auto-read threshold: null follows the global setting, 0 is off, 1 to 365 is its own (docs/design.md 7.1d). */
  auto_read_days?: number | null;
  is_archive: boolean;
  starred_count: number;
}

export interface RunStatus {
  /** A string on the wire (design 7); String() at the boundary tolerates a number from an older server. */
  id: string;
  kind: string;
  /** A filter apply run names its rule and reports how many articles it changed. */
  filter_id?: string;
  changed?: number;
  done: number;
  total: number;
  new_items: number;
  errors: number;
}

export interface Warning {
  code: string;
  message: string;
}

/** A device profile (docs/design.md 7.1c): overrides only, plus the built-in defaults and the effective values. */
export interface DeviceView {
  id: string;
  name: string;
  created_at?: number;
  last_seen_at?: number;
  profile: Record<string, unknown>;
  defaults?: Record<string, unknown>;
  merged: Record<string, unknown>;
}

/** An enabled, non-inverted highlight rule, for drawing in titles and articles (docs/design.md 7.1b). */
export interface Highlight {
  id: string;
  scope: "global" | "folder" | "feed";
  folder_id: string | null;
  feed_id: string | null;
  terms: string[];
  fields: string[];
  case_sensitive: boolean;
  whole_word: boolean;
  fold_diacritics: boolean;
}

export interface Bootstrap {
  user: { username: string; api_enabled: boolean };
  settings: Record<string, unknown>;
  /** Absent only from a server older than device profiles: the per-device settings then stay local. */
  device?: DeviceView;
  highlights?: Highlight[];
  folders: Folder[];
  feeds: Feed[];
  counts: { unread: number; starred: number; muted?: number };
  /** The saved searches, without counts (docs/design.md 7.1d). Absent on an older server. */
  saved_searches?: SavedSearch[];
  runs: RunStatus[];
  warnings: Warning[];
  server_time: number;
  version: string;
}

export interface StatusResponse {
  runs: RunStatus[];
  inflight: number;
  unread_total: number;
  /** How many articles are muted right now (absent on an older server). */
  muted?: number;
}

export interface OpenResponse {
  session_key: string;
  item: ItemDetail;
}

export interface FulltextResponse {
  mode: 0 | 1 | null;
  effective: 0 | 1;
  status: "ok" | "error" | "skipped";
  content_html: string | null;
  word_count: number;
  error: string | null;
}

export interface MarkReadResponse {
  changed: string[];
  restored: string[];
}

/** What a list screen asks for. `q` is search text. */
export interface Scope {
  view: View;
  feed?: string;
  folder?: string;
  q?: string;
  /** Oldest first (device preference), or relevance (search only). Absent means newest first. */
  order?: "oldest" | "rank";
  /**
   * Search-as-you-type: the unfinished last word also matches as a prefix. Only while the user is typing; a
   * submitted search, a saved-search run and every count leave it out.
   */
  typing?: boolean;
  /**
   * The list's own `fallback` flag, echoed into mark-read `scope.fallback` so the server marks what the list showed.
   * Not part of the scope key and never sent to GET /api/items.
   */
  fallback?: boolean;
}

/** A saved search (docs/design.md 7.1d). `scope` is exactly one of a feed, a folder or a view; omitted means the whole library. */
export interface SavedSearch {
  id: string;
  name: string;
  q: string;
  scope?: { feed_id?: string; folder_id?: string; view?: "all" | "unread" | "starred" };
  order?: "date" | "oldest" | "rank";
  /** Absent until the counts load; null when the server ran out of time counting it (show nothing). */
  unread?: number | null;
  unread_capped?: boolean;
}

// ---- SSE ----

/** Run ids are strings (design 7); a number is tolerated defensively and normalised with String(). */
export type RunId = string | number;

export interface ItemsStateEvent {
  ids: string[];
  read?: boolean;
  starred?: boolean;
  /** true: a mute was applied (leave All); false: a filter was deleted with un-muting (visible again). */
  muted?: boolean;
  restored?: string[];
  source: string;
}

export interface CountsEvent {
  unread_total: number;
  muted?: number;
  feeds: Record<string, number>;
}

export interface FetchDoneEvent {
  feed_id: string;
  run_ids?: RunId[];
  trigger?: string;
  outcome: string;
  new_items: number;
  new_item_ids?: string[];
  updated_items?: number;
  trimmed_items?: number;
  error_class?: string;
  error?: string;
}

export type ServerEvent =
  | { type: "run.start"; data: { run_id: RunId; kind: string; total: number; filter_id?: string } }
  | { type: "run.progress"; data: { run_id: RunId; done: number; total: number; new_items: number; errors: number; changed?: number } }
  | { type: "run.done"; data: { run_id: RunId; new_items: number; errors: number; kind?: string; filter_id?: string; changed?: number; scanned?: number; error?: string } }
  | { type: "fetch.done"; data: FetchDoneEvent }
  | { type: "items.state"; data: ItemsStateEvent }
  | { type: "fulltext.ready"; data: { ids: string[]; source: string } }
  | { type: "counts"; data: CountsEvent }
  | { type: "feed.changed"; data: { feed_id: string } }
  | { type: "filters.changed"; data: Record<string, never> }
  | { type: "saved_searches.changed"; data: Record<string, never> }
  | { type: "folder.changed"; data: { folder_id?: string } }
  | { type: "resync"; data: Record<string, never> };

export const SERVER_EVENT_TYPES: ServerEvent["type"][] = [
  "run.start",
  "run.progress",
  "run.done",
  "fetch.done",
  "items.state",
  "fulltext.ready",
  "counts",
  "feed.changed",
  "filters.changed",
  "saved_searches.changed",
  "folder.changed",
  "resync",
];

/** Response of a scoped or bounded mark-read. `changed` is empty and `undoable` false above the server cap. */
export interface BulkMarkResponse extends MarkReadResponse {
  count?: number;
  undoable?: boolean;
  /** Trimmed-ledger rows a scope mark also flipped; undo sends them back with `changed`. */
  ledger_ids?: string[];
}
