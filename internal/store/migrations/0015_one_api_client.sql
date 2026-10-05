-- Kipple schema v15: one stats client value for every Reader API client (docs/design.md §2.2a, §8).
-- stats_events.client allowed three values named after particular apps beside 'api'. Every Reader API
-- client is now treated the same, so the stored rows carrying those values become 'api' and the CHECK
-- allows only 'web', 'pwa' and 'api'. A CHECK cannot be changed in place, so the table is rebuilt: every
-- row keeps its id and every other column, the sqlite_sequence high-water mark is carried over (stats ids
-- are never reused), and the seven indexes are recreated exactly as 0001, 0008 and 0009 made them (the
-- stats summary names three of them with INDEXED BY, so a missing one is a hard error). stats_events has
-- no triggers, no views read it, and no foreign key points to or from it, so foreign keys stay on.
--
-- Not O(1): one copy of every stats row plus the seven index builds, inside the migration transaction
-- (measured on one million seeded events: the copy 4 s, the seven indexes 6 s, the commit 2 s; 200,000
-- events take about 2 s). The table is written twice over meanwhile, so the pre-migration snapshot and
-- the WAL need room too, see docs/deploy.md. One-way: the old values are not kept, so going back to
-- schema 14 is a restore of the pre-migration snapshot. Runs once, in BEGIN IMMEDIATE, gated by
-- user_version.
CREATE TABLE stats_events_new (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  ts            INTEGER NOT NULL,
  local_date    TEXT NOT NULL,
  local_hour    INTEGER NOT NULL CHECK (local_hour BETWEEN 0 AND 23),
  local_weekday INTEGER NOT NULL CHECK (local_weekday BETWEEN 0 AND 6),
  kind          TEXT NOT NULL CHECK (kind IN ('open','read_time','scroll','star','unstar','open_original','share')),
  client        TEXT NOT NULL CHECK (client IN ('web','pwa','api')),
  inferred      INTEGER NOT NULL DEFAULT 0 CHECK (inferred IN (0,1)),
  item_id       INTEGER NOT NULL,
  feed_id       INTEGER NOT NULL,
  feed_title    TEXT NOT NULL,
  folder_id     INTEGER,
  folder_name   TEXT,
  item_title    TEXT,
  item_url      TEXT,
  value         INTEGER,
  session_key   TEXT,
  event_id      TEXT
) STRICT;
INSERT INTO stats_events_new (id, ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id,
                              feed_id, feed_title, folder_id, folder_name, item_title, item_url, value,
                              session_key, event_id)
  SELECT id, ts, local_date, local_hour, local_weekday, kind,
         CASE WHEN client IN ('web','pwa') THEN client ELSE 'api' END,
         inferred, item_id, feed_id, feed_title, folder_id, folder_name, item_title, item_url, value,
         session_key, event_id
  FROM stats_events ORDER BY id;
-- The copy made a sequence row only if there were rows; an empty table still keeps the old mark.
INSERT INTO sqlite_sequence (name, seq)
  SELECT 'stats_events_new', 0 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'stats_events_new');
UPDATE sqlite_sequence
  SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'), 0))
  WHERE name = 'stats_events_new';
DROP TABLE stats_events;
ALTER TABLE stats_events_new RENAME TO stats_events;

CREATE INDEX idx_stats_kind_ts ON stats_events(kind, ts);
CREATE INDEX idx_stats_feed    ON stats_events(feed_id, kind, ts);
CREATE INDEX idx_stats_session ON stats_events(session_key, kind) WHERE session_key IS NOT NULL;
CREATE UNIQUE INDEX idx_stats_event ON stats_events(event_id) WHERE event_id IS NOT NULL;
CREATE INDEX idx_stats_open_cov ON stats_events(local_date, local_hour, feed_id, item_id, session_key, ts, inferred) WHERE kind = 'open';
CREATE INDEX idx_stats_rt_cov ON stats_events(local_date, local_hour, feed_id, session_key, value) WHERE kind = 'read_time';
CREATE INDEX idx_stats_scroll_cov ON stats_events(local_date, session_key, value) WHERE kind = 'scroll';
