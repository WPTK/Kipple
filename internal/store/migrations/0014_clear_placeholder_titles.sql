-- Kipple schema v14: a feed added from a sync app used to store its host as its title until a fetch
-- found a title in the document; a feed whose document has no title kept that host for good. A new
-- feed now stores no title until then (its display name falls back to its URL), so an OPML export never
-- writes the placeholder and a re-import cannot keep it as a custom name.
--
-- This clears a stored title equal to the feed's host, or to the host of the URL it had before a redirect
-- moved it (url_original; scheme, user info, port, path, query and fragment stripped), on:
--   - a feed that never fetched successfully, enabled or not: its title can only be the placeholder;
--   - an enabled feed that has fetched successfully: its next fetch refills the title. Its HTTP
--     validators and body hash are cleared too, so that fetch is a full one and stores the title the
--     document gives itself again (a feed whose own title really is its domain gets it back then).
-- A disabled, gone or archive feed that has fetched keeps its title: it will not fetch again, and its
-- title may really be its domain. Runs once, gated by user_version.
WITH rest(id, r) AS (
  SELECT id, substr(url_original, instr(url_original, '://') + 3) FROM feeds WHERE url_original IS NOT NULL
), authority(id, a) AS (
  -- Up to the first '/', '?' or '#'.
  SELECT id, substr(r, 1, min(
    CASE WHEN instr(r, '/') > 0 THEN instr(r, '/') ELSE length(r) + 1 END,
    CASE WHEN instr(r, '?') > 0 THEN instr(r, '?') ELSE length(r) + 1 END,
    CASE WHEN instr(r, '#') > 0 THEN instr(r, '#') ELSE length(r) + 1 END) - 1) FROM rest
), hostport(id, hp) AS (
  -- User info ends at the last '@'.
  SELECT id, a FROM authority
  UNION ALL
  SELECT id, substr(hp, instr(hp, '@') + 1) FROM hostport WHERE instr(hp, '@') > 0
), earlier(id, host) AS (
  SELECT id, lower(CASE
    WHEN hp LIKE '[%' THEN substr(hp, 2, instr(hp, ']') - 2)
    WHEN instr(hp, ':') > 0 THEN substr(hp, 1, instr(hp, ':') - 1)
    ELSE hp END) FROM hostport WHERE instr(hp, '@') = 0
), placeholder(id, refetch) AS (
  SELECT f.id, f.last_success_at IS NOT NULL FROM feeds f
  WHERE f.disabled_reason IS NOT 'archive' AND f.title != ''
    AND (f.title = f.host OR f.title IN (SELECT host FROM earlier WHERE earlier.id = f.id))
    AND (f.last_success_at IS NULL OR (f.enabled = 1 AND f.disabled_reason IS NULL))
)
UPDATE feeds SET title = '',
  etag = CASE WHEN p.refetch THEN NULL ELSE etag END,
  last_modified = CASE WHEN p.refetch THEN NULL ELSE last_modified END,
  body_hash = CASE WHEN p.refetch THEN NULL ELSE body_hash END
FROM placeholder p WHERE p.id = feeds.id;
