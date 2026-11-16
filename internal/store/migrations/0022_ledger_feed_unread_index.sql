-- Kipple schema v22: one partial index. idx_trimmed_feed_unread serves the ledger half of a mark-as-read
-- scoped to one feed or a folder (UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= ? AND feed_id = ?):
-- without it the statement walks every ledger row of the feed through the (feed_id, uid) key and filters on
-- read and id afterwards, so its cost grows with how many articles the feed has retired. The ledger is mostly
-- read, so the index stays small. Additive: no row changes, and IF NOT EXISTS makes a rerun a no-op. It is built
-- by one scan of the ledger inside the migration transaction. Runs once, in BEGIN IMMEDIATE, gated by
-- user_version.
CREATE INDEX IF NOT EXISTS idx_trimmed_feed_unread ON trimmed_items(feed_id, id) WHERE read = 0;
