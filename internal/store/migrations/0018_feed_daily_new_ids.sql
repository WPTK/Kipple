-- Kipple schema v18: feed_daily_new keeps the first and last item id it counted per feed and day, and is
-- keyed by day first (docs/design.md §2.2a, §8 Read rate, issue #37).
-- Item ids are allocation times in microseconds and only increase (store.IDAlloc), so the counted items
-- of one feed and day lie between first_item and last_item. The read rate counts a read only when its
-- item lies in such a span of its feed inside the window, so reads of a feed's first document or of
-- items that arrived before the window no longer count against the window's arrivals.
-- The key (local_date, feed_id) makes the read rate's window one range of the key (it reads every column
-- it needs from the table itself) and the first day one seek, so idx_feed_daily_date is gone.
-- Rows written under schema 17 carry no ids and cannot take part, so they are not copied: counting
-- starts again from this migration, and the read rate's window starts the day after the first new row.
-- One-way: going back to schema 17 is a restore of the pre-migration snapshot. Runs once, gated by
-- user_version.
CREATE TABLE feed_daily_new_v18 (
  local_date TEXT    NOT NULL,
  feed_id    INTEGER NOT NULL,
  new_items  INTEGER NOT NULL CHECK (new_items > 0),
  first_item INTEGER NOT NULL,
  last_item  INTEGER NOT NULL CHECK (last_item >= first_item),
  PRIMARY KEY (local_date, feed_id)
) STRICT, WITHOUT ROWID;

DROP TABLE feed_daily_new;
ALTER TABLE feed_daily_new_v18 RENAME TO feed_daily_new;
