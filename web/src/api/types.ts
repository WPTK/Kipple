// Types for the UI JSON API (docs/design.md section 7). Ids are strings in
// every JSON body; times are unix seconds.

export type View = "unread" | "all" | "starred";

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
  is_archive: boolean;
  starred_count: number;
}

export interface RunStatus {
  /** A string on the wire (design 7); String() at the boundary tolerates a number from an older server. */
  id: string;
  kind: string;
  done: number;
  total: number;
  new_items: number;
  errors: number;
}

export interface Warning {
  code: string;
  message: string;
}

export interface Bootstrap {
  user: { username: string; api_enabled: boolean };
  settings: Record<string, unknown>;
  folders: Folder[];
  feeds: Feed[];
  counts: { unread: number; starred: number };
  runs: RunStatus[];
  warnings: Warning[];
  server_time: number;
  version: string;
}

export interface StatusResponse {
  runs: RunStatus[];
  inflight: number;
  unread_total: number;
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
  /** Oldest first (device preference). Absent means newest first. */
  order?: "oldest";
}

// ---- SSE ----

/** Run ids are strings (design 7); a number is tolerated defensively and normalised with String(). */
export type RunId = string | number;

export interface ItemsStateEvent {
  ids: string[];
  read?: boolean;
  starred?: boolean;
  restored?: string[];
  source: string;
}

export interface CountsEvent {
  unread_total: number;
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
  | { type: "run.start"; data: { run_id: RunId; kind: string; total: number } }
  | { type: "run.progress"; data: { run_id: RunId; done: number; total: number; new_items: number; errors: number } }
  | { type: "run.done"; data: { run_id: RunId; new_items: number; errors: number } }
  | { type: "fetch.done"; data: FetchDoneEvent }
  | { type: "items.state"; data: ItemsStateEvent }
  | { type: "fulltext.ready"; data: { ids: string[]; source: string } }
  | { type: "counts"; data: CountsEvent }
  | { type: "feed.changed"; data: { feed_id: string } }
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
  "resync",
];

/** Response of a scoped or bounded mark-read. `changed` is empty and `undoable` false above the server cap. */
export interface BulkMarkResponse extends MarkReadResponse {
  count?: number;
  undoable?: boolean;
  /** Trimmed-ledger rows a scope mark also flipped; undo sends them back with `changed`. */
  ledger_ids?: string[];
}
