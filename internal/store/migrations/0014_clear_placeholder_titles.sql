-- Kipple schema v14: a feed added from a sync client used to store its host as its title until its
-- first successful fetch. A new feed now stores no title until then (its display name falls back to its
-- URL), so an OPML export never writes the placeholder and a re-import cannot keep it as a custom name.
-- Only feeds that never fetched successfully are touched: their stored title can only be that
-- placeholder. The next successful fetch stores the title the feed gives itself, as before.
-- Runs once, gated by user_version.
UPDATE feeds SET title = ''
WHERE title = host AND last_success_at IS NULL AND disabled_reason IS NOT 'archive';
