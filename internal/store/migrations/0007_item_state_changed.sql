-- Kipple schema v7: when an item's read or starred state last changed (unix seconds), for the
-- Reader API ot query with greader.ot_includes_user_changes on (design §6.5). read_at and
-- starred_at are cleared by mark-unread and unstar, so those changes left no trace; this column is
-- set by every state change after ingest (read, unread, star, unstar, mark-all, auto-read, restore,
-- filter mute and unmute-to-unread) and never by ingest itself. NULL = no change recorded.
-- Existing rows are backfilled from the later of read_at and starred_at, which is exactly what the
-- ot query matched before, so a client syncing across the upgrade loses nothing.
-- Additive: one nullable column (no table rewrite for the ALTER), one partial index. Runs once, in
-- BEGIN IMMEDIATE, gated by user_version.
ALTER TABLE items ADD COLUMN state_changed_at INTEGER;
UPDATE items SET state_changed_at = max(COALESCE(read_at, starred_at), COALESCE(starred_at, read_at))
  WHERE read_at IS NOT NULL OR starred_at IS NOT NULL;
CREATE INDEX idx_items_state_changed ON items(state_changed_at) WHERE state_changed_at IS NOT NULL;
