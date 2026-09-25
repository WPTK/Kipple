-- Kipple schema v2: per-feed memory of "this feed only loads with a browser User-Agent"
-- (setting fetch.user_agent_mode = browser_on_failure, design §4.4), and drop the two
-- reading settings replaced by the single ui.reading_density preset.
ALTER TABLE feeds ADD COLUMN ua_fallback INTEGER NOT NULL DEFAULT 0 CHECK (ua_fallback IN (0, 1));
DELETE FROM settings WHERE key IN ('ui.line_height', 'ui.content_width');
