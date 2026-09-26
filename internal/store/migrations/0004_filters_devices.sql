-- Kipple schema v4 (backend-additions-round2 sections 1.8, 1.13 and 4): the tables and columns
-- for keyword filters, the muted marker, item categories, per-feed auto-read and per-device
-- profiles. Schema only: nothing reads or writes these yet except the trim and restore copies of
-- categories_json. Additive: no table is rebuilt, ADD COLUMN with a NULL default is O(1) with no
-- row rewrite, the new indexes start empty. Runs once, in BEGIN IMMEDIATE, gated by user_version
-- (a failure rolls back the whole file), so the plain ALTERs need no IF NOT EXISTS guard.

-- Keyword rules (mute, mark_read, star, highlight), scoped to everything, a folder or a feed.
-- terms and fields are JSON arrays; the Go side (internal/filter) owns the deeper validation
-- (counts, lengths, regex safety). Deleting a folder or feed cascades its filters; the items they
-- muted keep muted_by (orphans, section 1.6). hits / last_hit_at are bumped per fetch commit.
CREATE TABLE filters (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT NOT NULL DEFAULT '',
  enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  scope           TEXT NOT NULL CHECK (scope IN ('global','folder','feed')),
  folder_id       INTEGER REFERENCES folders(id) ON DELETE CASCADE,
  feed_id         INTEGER REFERENCES feeds(id)   ON DELETE CASCADE,
  kind            TEXT NOT NULL CHECK (kind IN ('text','regex')),
  terms           TEXT NOT NULL CHECK (json_valid(terms) AND json_type(terms) = 'array'),
  fields          TEXT NOT NULL DEFAULT '["title"]' CHECK (json_valid(fields) AND json_type(fields) = 'array'),
  case_sensitive  INTEGER NOT NULL DEFAULT 0 CHECK (case_sensitive IN (0,1)),
  whole_word      INTEGER NOT NULL DEFAULT 1 CHECK (whole_word IN (0,1)),
  fold_diacritics INTEGER NOT NULL DEFAULT 1 CHECK (fold_diacritics IN (0,1)),
  invert          INTEGER NOT NULL DEFAULT 0 CHECK (invert IN (0,1)),
  action          TEXT NOT NULL CHECK (action IN ('mute','mark_read','star','highlight')),
  position        INTEGER NOT NULL DEFAULT 0,
  hits            INTEGER NOT NULL DEFAULT 0,
  last_hit_at     INTEGER,
  created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at      INTEGER NOT NULL DEFAULT (unixepoch()),
  CHECK ((scope = 'global' AND folder_id IS NULL AND feed_id IS NULL)
      OR (scope = 'folder' AND folder_id IS NOT NULL AND feed_id IS NULL)
      OR (scope = 'feed'   AND feed_id IS NOT NULL AND folder_id IS NULL)),
  CHECK (action != 'highlight' OR kind = 'text')
) STRICT;
-- The cascade from folders and feeds looks filters up by these columns.
CREATE INDEX idx_filters_folder ON filters(folder_id) WHERE folder_id IS NOT NULL;
CREATE INDEX idx_filters_feed   ON filters(feed_id)   WHERE feed_id IS NOT NULL;

-- The filter id that muted the item; NULL = not muted. No FK: deleting a filter may leave orphans
-- on purpose (?unmute=keep). Invariant muted_by IS NOT NULL => read = 1 is kept in Go, not by a
-- CHECK (a missed path must never turn a Reader edit-tag into a 500).
ALTER TABLE items ADD COLUMN muted_by INTEGER;
CREATE INDEX idx_items_muted ON items(sort_at, id) WHERE muted_by IS NOT NULL;

-- Item categories (gofeed Categories, at most 20 of 100 runes), filled for new items only.
-- Kept in the retention stub so a restore brings them back.
ALTER TABLE item_content    ADD COLUMN categories_json TEXT CHECK (categories_json IS NULL OR json_valid(categories_json));
ALTER TABLE trimmed_content ADD COLUMN categories_json TEXT;

-- Auto-mark-read after N days unread: NULL inherits library.auto_read_days, 0 = off for this feed.
ALTER TABLE feeds ADD COLUMN auto_read_days INTEGER CHECK (auto_read_days IS NULL OR auto_read_days BETWEEN 0 AND 365);

-- Per-device appearance profiles, keyed by the HttpOnly kipple_device cookie value. settings holds
-- overrides only (effective value = override, else the account default). last_seen_at is bumped at
-- most daily; the least recently seen device is evicted above 50, and devices unseen for 400 days
-- are purged nightly.
CREATE TABLE devices (
  id           TEXT PRIMARY KEY CHECK (length(id) BETWEEN 16 AND 32),
  name         TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 64),
  settings     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(settings) AND json_type(settings) = 'object' AND length(settings) <= 8192),
  user_agent   TEXT NOT NULL DEFAULT '',
  client       TEXT NOT NULL DEFAULT 'web',
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_devices_seen ON devices(last_seen_at);
