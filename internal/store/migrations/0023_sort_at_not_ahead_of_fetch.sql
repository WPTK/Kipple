-- Kipple schema v23: an article's list position (sort_at) is never later than the time it was fetched.
-- Until now a feed date up to 24 hours ahead of the fetch kept its date as sort_at and sat above the
-- articles fetched after it until that date passed. sort_at is now min(published_at, crawl second), the
-- crawl second being id / 1e6. This brings stored rows to the same rule: every item, and every restore
-- stub, whose sort_at is past its crawl second takes the crawl second. published_at is untouched, and so
-- is every row dated at or before its fetch. Data only. One-way: going back to schema 22 is a restore of
-- the pre-migration snapshot. Runs once, gated by user_version.
UPDATE items SET sort_at = id / 1000000 WHERE sort_at > id / 1000000;
UPDATE trimmed_content SET sort_at = id / 1000000 WHERE sort_at > id / 1000000;
