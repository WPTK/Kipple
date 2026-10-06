-- Kipple schema v17: how many new, unread items each feed brought in per local day (docs/design.md §2.2a,
-- issue #37). The Stats screen divides a feed's opens by this to get a read rate. fetch_log cannot serve:
-- it is capped per feed and cascades away on unsubscribe, and retention trims the items themselves.
-- Like stats_events it has no foreign key, so it survives trims and unsubscribes, and it is never trimmed.
-- Each chunk of a fetch commit adds its items in its own transaction, to the local date (`tz` zone) of
-- the commit's first chunk; the last chunk takes back the items its trim removed. Not counted, as none
-- was a choice to open: a feed's first successful fetch, also the first after a URL edit (a backlog),
-- items that arrive already read (the initial-read cutoff, a rekey, a filter that marks read) or muted,
-- and items the same commit trims.
-- The opens (stats_events) have gaps this table does not: days before the first timed event, days while
-- statistics were off, and ranges the stats delete removed (no record of the range is kept). A read rate
-- must bound its range by sys.stats_timed_since and must not turn such a gap into 0%: no data shows a dash.
-- Days before this migration have no row. Additive and one-way: going back to schema 16 is a restore of
-- the pre-migration snapshot. Runs once, gated by user_version.
CREATE TABLE feed_daily_new (
  feed_id    INTEGER NOT NULL,
  local_date TEXT    NOT NULL,
  new_items  INTEGER NOT NULL CHECK (new_items > 0),
  PRIMARY KEY (feed_id, local_date)
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_feed_daily_date ON feed_daily_new(local_date);
