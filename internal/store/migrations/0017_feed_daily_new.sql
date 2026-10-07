-- Kipple schema v17: how many new, unread items each feed brought in per local day (docs/design.md §2.2a,
-- issue #37). The Stats screen divides a feed's opens by this to get a read rate. fetch_log cannot serve:
-- it is capped per feed and cascades away on unsubscribe, and retention trims the items themselves.
-- Like stats_events it has no foreign key, so it survives trims and unsubscribes, and it is never trimmed.
-- Each chunk of a fetch commit adds its items in its own transaction, to the local date (`tz` zone) of
-- the commit's first chunk; the last chunk takes back the items its trim removed. Not counted, as none
-- was a choice to open: the first successful fetch at a URL (a new subscription's, or the first after a
-- URL edit: a backlog), items that arrive already read (the initial-read cutoff, a rekey, a filter that
-- marks read) or muted, and items the same commit trims.
-- The opens (stats_events) have gaps this table does not: days before the first timed event, days while
-- statistics were off, and ranges the stats delete removed (no record of the range is kept). The read rate
-- bounds its range by the summary's covered_from (read_rate_from) and never turns such a gap into 0%: no
-- data shows a dash.
-- Days before this migration have no row. Additive and one-way: going back to schema 16 is a restore of
-- the pre-migration snapshot. Runs once, gated by user_version.
CREATE TABLE feed_daily_new (
  feed_id    INTEGER NOT NULL,
  local_date TEXT    NOT NULL,
  new_items  INTEGER NOT NULL CHECK (new_items > 0),
  PRIMARY KEY (feed_id, local_date)
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_feed_daily_date ON feed_daily_new(local_date);

-- feeds.url_succeeded: a fetch of the feed's current URL has succeeded. Set by every successful fetch,
-- cleared by a URL edit; a redirect migration keeps it (the same document moved). It is not
-- last_success_at, which says when the feed last succeeded at any URL (Feed Health, and the first-success
-- custom title rule). A feed starts at 1 when it has succeeded and still holds the body hash of that
-- success: a successful fetch stores body_hash and a URL edit clears it, so a URL edited but not yet
-- fetched starts at 0.
ALTER TABLE feeds ADD COLUMN url_succeeded INTEGER NOT NULL DEFAULT 0 CHECK (url_succeeded IN (0,1));
UPDATE feeds SET url_succeeded = 1 WHERE last_success_at IS NOT NULL AND body_hash IS NOT NULL;
