-- Kipple schema v1. Applied by the migration runner inside BEGIN IMMEDIATE; the runner then sets
-- PRAGMA user_version = 1. Conventions: *_at = unix SECONDS; items.id / trimmed_items.id = unix
-- MICROSECONDS (crawl time). Booleans are INTEGER 0/1 with CHECKs. Strings served on the wire are
-- NOT NULL DEFAULT '' so no JSON null can leak (decision 32).
PRAGMA application_id = 1263095884;   -- 'KIPL' = 0x4B49504C

-- Single-user key/value settings. Values are JSON. Defaults live in Go; a row exists only for
-- overridden keys. User keys (whitelisted for PATCH): refresh.interval_minutes, retention.default,
-- retention.restore_days, fetch.user_agent, fetch.honor_publisher_ttl, greader.icon_urls,
-- greader.ot_includes_user_changes, greader.subscribe_fetch_now, stats.api_single_read_is_open,
-- imgproxy.mode, tz, ui.* (theme, font_body, font_ui, font_size, line_height, content_width,
-- layouts, mark_read_on_scroll, ...). System keys (never PATCHable): sys.id_high_water (JSON
-- integer, allocator high-water mark), sys.last_snapshot_at, sys.last_snapshot_error.
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL CHECK (json_valid(value)),
  updated_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT, WITHOUT ROWID;

-- Exactly one row. password_hash = web login; api_password_hash = Reader API password (NULL =
-- Reader API disabled). Both argon2id PHC strings with m=19456 KiB, t=2, p=1. secret = 32 random
-- bytes hex: keys the Reader token HMAC, the login memo and image-proxy signatures. Written by Go
-- on first start from KIPPLE_USERNAME / KIPPLE_PASSWORD (/ KIPPLE_API_PASSWORD).
CREATE TABLE account (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  username          TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64
                                         AND username NOT GLOB '*[^A-Za-z0-9._-]*'),
  password_hash     TEXT NOT NULL,
  api_password_hash TEXT,
  secret            TEXT NOT NULL CHECK (length(secret) = 64),
  created_at        INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at        INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

-- Web UI sessions only (Reader tokens are stateless). id = hex sha256 of the cookie value; the
-- cookie itself is never stored. 30-day sliding expiry, purged nightly.
CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  user_agent   TEXT,
  remote_ip    TEXT
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- Single-level folders (Reader labels are flat; nested OPML is flattened). Row 1 is the
-- undeletable default. AUTOINCREMENT: ids are never reused. position = OPML document order.
CREATE TABLE folders (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (length(trim(name)) > 0),
  position   INTEGER NOT NULL DEFAULT 0,
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  created_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;
CREATE UNIQUE INDEX idx_folders_one_default ON folders(is_default) WHERE is_default = 1;
INSERT INTO folders (id, name, is_default) VALUES (1, 'Uncategorized', 1);
CREATE TRIGGER folders_keep_default BEFORE DELETE ON folders WHEN old.is_default = 1
BEGIN SELECT RAISE(ABORT, 'the default folder cannot be deleted'); END;

-- One row per subscription; also all fetch state and health. AUTOINCREMENT: clients cache feed/<id>.
-- url = current fetch URL (rewritten on confirmed permanent redirect; url_original = first URL).
-- url_key / url_original_key = scheme-less normalized form (lowercase host, default port and
-- trailing '#fragment' stripped, path and query kept) used by store.FindFeedByURL so http/https
-- variants and pre-migration URLs match. host = lowercase hostname of url (per-host limiter key).
-- title = from the feed document; custom_title = user rename or OPML title (display =
-- COALESCE(custom_title, title)). disabled_reason 'archive' marks the one hidden archive feed that
-- holds starred items of unsubscribed feeds (url 'kipple:archive', never fetched, retention 0).
-- initial_read_before: set by a UI OPML import option; new items published before it are inserted
-- read on the first successful fetch, then it is cleared. trimmed_unread_count: unread items
-- trimmed since trimmed_unread_since, excluding items trimmed by the transaction that inserted them.
CREATE TABLE feeds (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  folder_id            INTEGER NOT NULL DEFAULT 1 REFERENCES folders(id) ON DELETE SET DEFAULT,
  url                  TEXT NOT NULL UNIQUE,
  url_original         TEXT,
  url_key              TEXT NOT NULL,
  url_original_key     TEXT,
  host                 TEXT NOT NULL,
  title                TEXT NOT NULL DEFAULT '',
  custom_title         TEXT,
  site_url             TEXT NOT NULL DEFAULT '',
  description          TEXT NOT NULL DEFAULT '',
  position             INTEGER NOT NULL DEFAULT 0,
  enabled              INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  disabled_reason      TEXT CHECK (disabled_reason IN ('user','gone','archive')),
  interval_minutes     INTEGER CHECK (interval_minutes IS NULL OR interval_minutes BETWEEN 5 AND 10080),
  retention            INTEGER CHECK (retention IS NULL OR retention IN (0,50,100,250,500,1000)), -- NULL inherit, 0 unlimited
  fulltext             INTEGER NOT NULL DEFAULT 0 CHECK (fulltext IN (0,1)),
  dedup_mode           TEXT NOT NULL DEFAULT 'auto' CHECK (dedup_mode IN ('auto','link','link_title')),
  rekey_pending        INTEGER NOT NULL DEFAULT 0 CHECK (rekey_pending IN (0,1)),
  initial_read_before  INTEGER,
  user_agent           TEXT,
  http_auth            TEXT,                 -- 'user:pass' for Basic auth; single-user box, stored plain, never exported
  ignore_http_cache    INTEGER NOT NULL DEFAULT 0 CHECK (ignore_http_cache IN (0,1)),
  disable_http2        INTEGER NOT NULL DEFAULT 0 CHECK (disable_http2 IN (0,1)),
  allow_insecure_tls   INTEGER NOT NULL DEFAULT 0 CHECK (allow_insecure_tls IN (0,1)),
  allow_private_net    INTEGER NOT NULL DEFAULT 0 CHECK (allow_private_net IN (0,1)),
  etag                 TEXT,
  last_modified        TEXT,
  body_hash            TEXT,
  next_fetch_at        INTEGER NOT NULL DEFAULT 0,
  last_fetch_at        INTEGER,
  last_success_at      INTEGER,
  last_new_items_at    INTEGER,
  last_error_at        INTEGER,              -- kept after a success; resolved when < last_success_at
  last_error_class     TEXT,
  last_error           TEXT,
  last_status          INTEGER,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  current_delay_s      INTEGER NOT NULL DEFAULT 0,
  ttl_hint_s           INTEGER,
  redirect_to          TEXT,
  redirect_kind        TEXT CHECK (redirect_kind IN ('permanent','temporary')),
  redirect_count       INTEGER NOT NULL DEFAULT 0,
  trimmed_unread_count INTEGER NOT NULL DEFAULT 0,
  trimmed_unread_since INTEGER,
  created_at           INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at           INTEGER NOT NULL DEFAULT (unixepoch()),
  CHECK (disabled_reason IS NULL OR enabled = 0),
  CHECK (disabled_reason IS NOT 'archive' OR retention = 0)
) STRICT;
CREATE INDEX idx_feeds_due          ON feeds(next_fetch_at) WHERE enabled = 1;
CREATE INDEX idx_feeds_folder       ON feeds(folder_id, position);
CREATE UNIQUE INDEX idx_feeds_url_key ON feeds(url_key);
CREATE INDEX idx_feeds_url_orig_key ON feeds(url_original_key) WHERE url_original_key IS NOT NULL;
CREATE UNIQUE INDEX idx_feeds_one_archive ON feeds(disabled_reason) WHERE disabled_reason = 'archive';

-- Favicon bytes, served at /api/feeds/{id}/icon (and /api/greader.php/icon/... when enabled).
CREATE TABLE feed_icons (
  feed_id      INTEGER PRIMARY KEY REFERENCES feeds(id) ON DELETE CASCADE,
  data         BLOB NOT NULL,
  content_type TEXT NOT NULL,
  source_url   TEXT,
  hash         TEXT NOT NULL,
  fetched_at   INTEGER NOT NULL
) STRICT;

-- Items, narrow row (decision 30). id = crawl time in unix microseconds (allocator). uid = per-feed
-- dedup key ('g:'|'l:'|'h:' + 32 hex of sha256). content_hash = h(title|url|author|html);
-- text_hash = h(title|plain text): a change sets content_changed_at (Reader API ot leg 2).
-- sort_at = min(published_at, id/1e6 + 86400): UI order and retention rank. url is absolute ('' if
-- none). fulltext_mode: NULL = follow feed, 0 = force feed content, 1 = force extracted text.
-- retain_until: retention exemption set by a mark-unread restore. origin_title: original feed title
-- for items re-parented to the archive feed. Small mutable columns come first.
CREATE TABLE items (
  id                 INTEGER PRIMARY KEY,
  feed_id            INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  read               INTEGER NOT NULL DEFAULT 0 CHECK (read IN (0,1)),
  starred            INTEGER NOT NULL DEFAULT 0 CHECK (starred IN (0,1)),
  read_at            INTEGER,
  starred_at         INTEGER,
  retain_until       INTEGER,
  fulltext_mode      INTEGER CHECK (fulltext_mode IN (0,1)),
  published_at       INTEGER NOT NULL,
  updated_at         INTEGER,
  sort_at            INTEGER NOT NULL,
  content_changed_at INTEGER,
  word_count         INTEGER NOT NULL DEFAULT 0,
  uid                TEXT NOT NULL,
  content_hash       TEXT NOT NULL,
  text_hash          TEXT NOT NULL,
  url                TEXT NOT NULL DEFAULT '',
  title              TEXT NOT NULL DEFAULT '',
  author             TEXT NOT NULL DEFAULT '',
  image_url          TEXT,
  origin_title       TEXT,
  UNIQUE (feed_id, uid)
) STRICT;
CREATE INDEX idx_items_unread      ON items(id)                 WHERE read = 0;
CREATE INDEX idx_items_unread_feed ON items(feed_id, id)        WHERE read = 0;
CREATE INDEX idx_items_starred     ON items(id)                 WHERE starred = 1;
CREATE INDEX idx_items_feed_sort   ON items(feed_id, sort_at, id);
CREATE INDEX idx_items_sort        ON items(sort_at, id);
CREATE INDEX idx_items_unread_sort ON items(sort_at, id)        WHERE read = 0;
CREATE INDEX idx_items_changed     ON items(content_changed_at) WHERE content_changed_at IS NOT NULL;

-- Article bodies (decision 30). content_html = absolutized, bluemonday-sanitized feed HTML with
-- ORIGINAL (absolute) image URLs. content_text = plain text (FTS source, excerpt, word_count).
CREATE TABLE item_content (
  item_id         INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  content_html    TEXT NOT NULL DEFAULT '',
  content_text    TEXT NOT NULL DEFAULT '',
  enclosures_json TEXT CHECK (enclosures_json IS NULL OR json_valid(enclosures_json))
) STRICT;

-- Full-text search: external-content FTS5 over a view joining the narrow row and the body
-- (validated: MATCH, snippet(), integrity-check, rebuild source). rowid = items.id. Triggers keep it
-- in sync; every update trigger is guarded so unchanged values never touch the index. Order of
-- operations in Go: INSERT items then INSERT item_content (FTS insert fires on the content row);
-- the items BEFORE DELETE trigger removes the FTS row while item_content still exists.
CREATE VIEW item_search (id, title, author, content_text) AS
  SELECT i.id, i.title, i.author, c.content_text FROM items i JOIN item_content c ON c.item_id = i.id;
CREATE VIRTUAL TABLE items_fts USING fts5(
  title, author, content_text,
  content='item_search', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER item_content_fts_ai AFTER INSERT ON item_content BEGIN
  INSERT INTO items_fts(rowid, title, author, content_text)
    SELECT new.item_id, i.title, i.author, new.content_text FROM items i WHERE i.id = new.item_id;
END;
CREATE TRIGGER items_fts_bd BEFORE DELETE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.id, old.title, old.author, c.content_text FROM item_content c WHERE c.item_id = old.id;
END;
CREATE TRIGGER item_content_fts_bd BEFORE DELETE ON item_content
WHEN EXISTS (SELECT 1 FROM items WHERE id = old.item_id) BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.item_id, i.title, i.author, old.content_text FROM items i WHERE i.id = old.item_id;
END;
CREATE TRIGGER items_fts_au AFTER UPDATE OF title, author ON items
WHEN old.title IS NOT new.title OR old.author IS NOT new.author BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.id, old.title, old.author, c.content_text FROM item_content c WHERE c.item_id = old.id;
  INSERT INTO items_fts(rowid, title, author, content_text)
    SELECT new.id, new.title, new.author, c.content_text FROM item_content c WHERE c.item_id = new.id;
END;
CREATE TRIGGER item_content_fts_au AFTER UPDATE OF content_text ON item_content
WHEN old.content_text IS NOT new.content_text BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.item_id, i.title, i.author, old.content_text FROM items i WHERE i.id = old.item_id;
  INSERT INTO items_fts(rowid, title, author, content_text)
    SELECT new.item_id, i.title, i.author, new.content_text FROM items i WHERE i.id = new.item_id;
END;

-- Readability extraction, one row per attempted item. error set and content_html NULL = failed.
-- content_html is absolutized against the article URL and sanitized with the feed policy.
CREATE TABLE item_fulltext (
  item_id      INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  content_html TEXT,
  content_text TEXT,
  word_count   INTEGER NOT NULL DEFAULT 0,
  image_url    TEXT,
  source_url   TEXT,
  extracted_at INTEGER NOT NULL,
  error        TEXT,
  CHECK (content_html IS NOT NULL OR error IS NOT NULL)
) STRICT;

-- Retention ledger and tombstone. id = the former items.id; (feed_id, uid) blocks re-ingestion;
-- read is kept truthful by edit-tag / mark-all-as-read; last_seen_at is bumped while the feed
-- document still lists the uid and gates the 180-day purge.
CREATE TABLE trimmed_items (
  id           INTEGER PRIMARY KEY,
  feed_id      INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  uid          TEXT NOT NULL,
  read         INTEGER NOT NULL CHECK (read IN (0,1)),
  trimmed_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  UNIQUE (feed_id, uid)
) STRICT;
CREATE INDEX idx_trimmed_seen    ON trimmed_items(last_seen_at);
CREATE INDEX idx_trimmed_trimmed ON trimmed_items(trimmed_at);

-- Restore stubs (decision 17): everything needed to put a trimmed item back into items +
-- item_content with its original id. Kept retention.restore_days (default 90) after trimmed_at,
-- then deleted nightly (the ledger row stays as a tombstone). Extracted full text is not kept.
CREATE TABLE trimmed_content (
  id              INTEGER PRIMARY KEY REFERENCES trimmed_items(id) ON DELETE CASCADE ON UPDATE CASCADE,
  published_at    INTEGER NOT NULL,
  updated_at      INTEGER,
  sort_at         INTEGER NOT NULL,
  word_count      INTEGER NOT NULL,
  content_hash    TEXT NOT NULL,
  text_hash       TEXT NOT NULL,
  url             TEXT NOT NULL,
  title           TEXT NOT NULL,
  author          TEXT NOT NULL,
  image_url       TEXT,
  origin_title    TEXT,
  fulltext_mode   INTEGER,
  content_html    TEXT NOT NULL,
  content_text    TEXT NOT NULL,
  enclosures_json TEXT
) STRICT;

-- Per-feed fetch history (health view). Kept 14 days with a 50-row floor per feed; the last 10
-- error rows and every keep = 1 row (redirect_migrated, guid_churn_suspected, rekeyed) survive.
-- first_item_id/last_item_id bound the ids this fetch inserted ("mark this fetch read").
-- error_class: timeout|dns|connect|tls|http|cloudflare|too_large|empty|parse|ssrf|redirect_loop|gone
-- note: 'redirect_migrated: <old> -> <new>', 'redirect_target_owned_by_feed <id>',
--       'retry_after=<s>s', 'guid_churn_suspected', 'guid_duplicates: <k>/<n>', 'rekeyed: <k>',
--       'skipped: host retry-after until <ts>', 'fulltext: <ok>/<tried>', 'initial_read: <k>'.
CREATE TABLE fetch_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  feed_id       INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  trigger       TEXT NOT NULL CHECK (trigger IN ('scheduled','manual','feed_manual','subscribe','import','retention')),
  started_at    INTEGER NOT NULL,
  duration_ms   INTEGER NOT NULL,
  outcome       TEXT NOT NULL CHECK (outcome IN ('ok','not_modified','unchanged','error','skipped','trim_only')),
  http_status   INTEGER,
  error_class   TEXT,
  error         TEXT,
  new_items     INTEGER NOT NULL DEFAULT 0,
  updated_items INTEGER NOT NULL DEFAULT 0,
  trimmed_items INTEGER NOT NULL DEFAULT 0,
  first_item_id INTEGER,
  last_item_id  INTEGER,
  bytes         INTEGER,
  final_url     TEXT,
  note          TEXT,
  keep          INTEGER NOT NULL DEFAULT 0 CHECK (keep IN (0,1))
) STRICT;
CREATE INDEX idx_fetch_log_feed ON fetch_log(feed_id, id);
CREATE INDEX idx_fetch_log_err  ON fetch_log(feed_id, id) WHERE outcome = 'error';

-- Reading stats: append-only, never trimmed, no FKs (survive trims and unsubscribes). Feed,
-- folder and item identity are snapshotted at write time. local_* computed in settings.tz at
-- write time (DST baked in). value: read_time = seconds, scroll = percent 0-100.
-- inferred = 1 for opens derived from Reader API edit-tag (approximate; off by default).
CREATE TABLE stats_events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  ts            INTEGER NOT NULL,
  local_date    TEXT NOT NULL,                  -- 'YYYY-MM-DD' local, for streaks
  local_hour    INTEGER NOT NULL CHECK (local_hour BETWEEN 0 AND 23),
  local_weekday INTEGER NOT NULL CHECK (local_weekday BETWEEN 0 AND 6),   -- 0 = Sunday
  kind          TEXT NOT NULL CHECK (kind IN ('open','read_time','scroll','star','unstar','open_original','share')),
  client        TEXT NOT NULL CHECK (client IN ('web','pwa','reeder','netnewswire','unread','api')),
  inferred      INTEGER NOT NULL DEFAULT 0 CHECK (inferred IN (0,1)),
  item_id       INTEGER NOT NULL,
  feed_id       INTEGER NOT NULL,
  feed_title    TEXT NOT NULL,
  folder_id     INTEGER,
  folder_name   TEXT,
  item_title    TEXT,
  item_url      TEXT,
  value         INTEGER,
  session_key   TEXT
) STRICT;
CREATE INDEX idx_stats_kind_ts ON stats_events(kind, ts);
CREATE INDEX idx_stats_feed    ON stats_events(feed_id, kind, ts);
CREATE INDEX idx_stats_session ON stats_events(session_key, kind) WHERE session_key IS NOT NULL;
