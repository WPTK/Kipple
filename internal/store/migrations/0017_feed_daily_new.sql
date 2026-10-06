-- Kipple schema v17: how many new, unread items each feed brought in per local day (docs/design.md §2.2,
-- issue #37). The Stats screen divides a feed's opens by this to get a read rate. fetch_log cannot serve:
-- it is capped per feed and cascades away on unsubscribe, and retention trims the items themselves.
-- Like stats_events it has no foreign key, so it survives trims and unsubscribes, and it is never trimmed.
-- A row is written by the fetch commit that inserts the items (the local date is in the `tz` zone at that
-- moment). Items that arrive already read (the initial-read cutoff, a rekey, a filter that marks read) or
-- muted are not counted: they were never a choice to open. Days before this migration have no row; the
-- Stats screen shows "no data" for them, never zero. Additive and one-way: going back to schema 16 is a
-- restore of the pre-migration snapshot. Runs once, gated by user_version; IF NOT EXISTS makes a rerun a no-op.
CREATE TABLE IF NOT EXISTS feed_daily_new (
  feed_id    INTEGER NOT NULL,
  local_date TEXT    NOT NULL,
  new_items  INTEGER NOT NULL CHECK (new_items > 0),
  PRIMARY KEY (feed_id, local_date)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_feed_daily_date ON feed_daily_new(local_date);
