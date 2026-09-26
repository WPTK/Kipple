-- Kipple schema v6: the favicon finder's bookkeeping (design §4.11). feed_icons (0001) holds the
-- icon itself and was only ever read; the finder now fills it. This table records every lookup,
-- successful or not, so a feed is looked up after its first successful fetch, then at most weekly,
-- and again as soon as its site changes, and a failing lookup backs off without touching the
-- feed's own health columns. site_url and feed_url are the feeds values the lookup used: when the
-- site's host (site_url's, else feed_url's; scheme, path and query ignored) or the feed's own host
-- no longer matches (store.IconSiteKey), the feed is due at once and failures starts over.
-- Additive: a new empty table, no rewrite. Runs once, in BEGIN IMMEDIATE,
-- gated by user_version.
CREATE TABLE feed_icon_checks (
  feed_id       INTEGER PRIMARY KEY REFERENCES feeds(id) ON DELETE CASCADE,
  site_url      TEXT NOT NULL,
  feed_url      TEXT NOT NULL,
  checked_at    INTEGER NOT NULL,
  next_check_at INTEGER NOT NULL,
  failures      INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
  last_error    TEXT
) STRICT;
