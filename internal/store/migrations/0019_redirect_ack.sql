-- Kipple schema v19: feeds.redirect_ack is the address of a permanent redirect the user chose to live
-- with: the feed redirects to a feed they already have and they kept both (Feed Health, docs/design.md
-- section 4.7). The fetch commit leaves that redirect unrecorded, so the feed stops showing as Moved and
-- stops being listed as needing attention. NULL means no choice was made. One-way: going back to schema 18
-- is a restore of the pre-migration snapshot. Runs once, gated by user_version.
ALTER TABLE feeds ADD COLUMN redirect_ack TEXT;
