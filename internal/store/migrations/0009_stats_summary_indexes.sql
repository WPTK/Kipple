-- Kipple schema v9: covering indexes for the stats summary (design section 8, GET /api/stats/summary).
-- The summary reads opens, read_time and scroll rows by local date range. Each partial index holds
-- exactly the columns its query needs, so the queries never touch the wide table rows (which carry
-- the snapshotted titles and URLs): a month at one million events reads in tens of milliseconds
-- instead of seconds. The queries name the indexes (INDEXED BY), so the planner cannot fall back to
-- idx_stats_kind_ts when it has no statistics; the flip side is that a missing index is a hard
-- error. Any future migration that rebuilds stats_events (a table recreate) MUST recreate these three
-- indexes; TestStatsSummaryPlans fails if one is missing or unused. Read time and scroll rows are
-- never inferred, so their indexes carry no inferred column. Not O(1): each index is built by one
-- scan of stats_events inside the migration transaction (about 2 s per million rows for the three, measured;
-- about 60 MB of index per million events; the pre-migration snapshot and the WAL need room too,
-- see docs/deploy.md). Runs once, in BEGIN IMMEDIATE, gated by user_version.
CREATE INDEX idx_stats_open_cov ON stats_events(local_date, local_hour, feed_id, item_id, session_key, ts, inferred) WHERE kind = 'open';
CREATE INDEX idx_stats_rt_cov ON stats_events(local_date, local_hour, feed_id, session_key, value) WHERE kind = 'read_time';
CREATE INDEX idx_stats_scroll_cov ON stats_events(local_date, session_key, value) WHERE kind = 'scroll';
