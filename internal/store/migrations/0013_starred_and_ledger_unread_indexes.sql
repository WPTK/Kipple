-- Kipple schema v13: two partial indexes. idx_items_starred_sort serves the Starred view's keyset
-- (sort_at, id) the way idx_items_unread_sort serves Unread: without it a page past the first walks
-- idx_items_sort over every item older than the cursor to find the few starred ones.
-- idx_trimmed_unread serves the ledger half of a mark-all-as-read (UPDATE trimmed_items SET read = 1
-- WHERE read = 0 AND id <= ?), which otherwise scans the whole ledger; the ledger is mostly read, so
-- the index stays small. Additive: no row changes, and IF NOT EXISTS makes a rerun a no-op. Each index
-- is built by one scan of its table inside the migration transaction. Runs once, in BEGIN IMMEDIATE,
-- gated by user_version.
CREATE INDEX IF NOT EXISTS idx_items_starred_sort ON items(sort_at, id) WHERE starred = 1;
CREATE INDEX IF NOT EXISTS idx_trimmed_unread ON trimmed_items(id) WHERE read = 0;
