-- Kipple schema v22: idx_items_unread_sort carries the columns a list page reads to test an item, so the page
-- is picked from the index alone. The Unread list (and its Digest cut by reading length) walks this index in
-- keyset order and tests each candidate: the feed is not being deleted (feed_id), and the reading length
-- (word_count, and fulltext_mode to decide which word count is shown). With only (sort_at, id) in the index,
-- every candidate row was fetched from the table to read those columns, so a page walked and fetched rows it
-- then dropped. The index keeps its name and its WHERE read = 0; only its columns grow. Additive in effect: no
-- row changes. The index is rebuilt by one scan of the unread rows inside the migration transaction. Runs once,
-- in BEGIN IMMEDIATE, gated by user_version.
DROP INDEX IF EXISTS idx_items_unread_sort;
CREATE INDEX idx_items_unread_sort ON items(sort_at, id, feed_id, fulltext_mode, word_count) WHERE read = 0;
