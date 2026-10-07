-- Kipple schema v18: feed_daily_new holds one row per run of counted items, a feed's new items that
-- arrived one after another with no uncounted item of that feed between them, keyed by day first
-- (docs/design.md §2.2a, §8 Read rate, issue #37).
-- Item ids are allocation times in microseconds and only increase (store.IDAlloc), so every item of the
-- feed whose id lies in first_item..last_item of a row was counted, and every counted item lies in one
-- row. The read rate counts a read only when its item lies in a row of its feed inside the window, so
-- its reads and its new items are the same items.
-- The key (local_date, feed_id, first_item) makes the window of all feeds one range of the key, which
-- holds every column it reads; idx_feed_daily_feed serves the window of one feed.
-- Rows written under schema 17 carry no ids and cannot take part, so they are not copied: counting
-- starts again from this migration. One-way: going back to schema 17 is a restore of the
-- pre-migration snapshot. Runs once, gated by user_version.
CREATE TABLE feed_daily_new_v18 (
  local_date TEXT    NOT NULL,
  feed_id    INTEGER NOT NULL,
  first_item INTEGER NOT NULL,
  last_item  INTEGER NOT NULL CHECK (last_item >= first_item),
  new_items  INTEGER NOT NULL CHECK (new_items > 0),
  PRIMARY KEY (local_date, feed_id, first_item)
) STRICT, WITHOUT ROWID;

DROP TABLE feed_daily_new;
ALTER TABLE feed_daily_new_v18 RENAME TO feed_daily_new;
CREATE INDEX idx_feed_daily_feed ON feed_daily_new(feed_id, local_date);
