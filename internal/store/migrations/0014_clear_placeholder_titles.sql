-- Kipple schema v14: a feed added from a sync app used to store its host as its title until a fetch
-- found a title in the document; a feed whose document has no title kept that host for good. A new
-- feed now stores no title until then (its display name falls back to its URL), so an OPML export never
-- writes the placeholder and a re-import cannot keep it as a custom name. This clears every stored
-- title equal to the feed's host, or to the host of the URL it had before a redirect moved it
-- (url_original), whether or not the feed has fetched. It also clears those feeds' HTTP validators and
-- body hash, so their next fetch is a full one and stores the title the document gives itself again
-- (a feed whose own title really is its host, such as a site named after its domain, gets it back
-- then). The archive feed is never touched. Runs once, gated by user_version.
WITH rest(id, r) AS (
  SELECT id, substr(url_original, instr(url_original, '://') + 3) FROM feeds WHERE url_original IS NOT NULL
), hostport(id, hp) AS (
  SELECT id, CASE WHEN instr(r, '/') > 0 THEN substr(r, 1, instr(r, '/') - 1) ELSE r END FROM rest
), earlier(id, host) AS (
  SELECT id, lower(CASE
    WHEN hp LIKE '[%' THEN substr(hp, 2, instr(hp, ']') - 2)
    WHEN instr(hp, ':') > 0 THEN substr(hp, 1, instr(hp, ':') - 1)
    ELSE hp END) FROM hostport
)
UPDATE feeds SET title = '', etag = NULL, last_modified = NULL, body_hash = NULL
WHERE disabled_reason IS NOT 'archive' AND title != ''
  AND (title = host OR title IN (SELECT host FROM earlier WHERE earlier.id = feeds.id));
