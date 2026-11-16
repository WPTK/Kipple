-- Kipple schema v22: one partial index. idx_items_starred_feed holds exactly the starred items in feed_id
-- order, so the feed list's per-feed starred count (SELECT feed_id, count(*) FROM items WHERE starred = 1
-- GROUP BY feed_id) is answered from the index alone. Additive: no row changes, and IF NOT EXISTS makes a
-- rerun a no-op. Runs once, in BEGIN IMMEDIATE, gated by user_version.
CREATE INDEX IF NOT EXISTS idx_items_starred_feed ON items(feed_id) WHERE starred = 1;
