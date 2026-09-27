-- Kipple schema v8: per-event client ids for stats events (design section 8 rule 4).
-- The web sender tags every event it sends with a random event_id; a retried flush or a
-- sendBeacon that repeats an already-stored batch carries the same ids and is dropped instead
-- of counted twice. NULL = no id (older clients, and every event the server records itself).
-- One nullable column (no table rewrite) and one partial unique index. Not O(1): the index
-- build scans stats_events once inside the migration transaction (fast at expected sizes; the
-- index stays empty because every existing row has event_id NULL). Runs once, in BEGIN
-- IMMEDIATE, gated by user_version.
ALTER TABLE stats_events ADD COLUMN event_id TEXT;
CREATE UNIQUE INDEX idx_stats_event ON stats_events(event_id) WHERE event_id IS NOT NULL;
