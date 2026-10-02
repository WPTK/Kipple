-- Kipple schema v11: delete the settings rows nothing reads any more (the contract step of earlier
-- removals). security.open_lan went when open mode became one rule (0.7); ui.font_size, ui.font_ui,
-- ui.layouts and stats.api_single_read_is_open were removed from the registry in 0.6.0 and were only
-- ever ignored since; sys.legacy_port was stamped by 0010 for 0.5's pre-0.5-port fallback, which 0.6.0 removed.
-- Settings reads never listed them, so nothing changes for the user. Runs once, gated by user_version.
DELETE FROM settings WHERE key IN ('security.open_lan', 'ui.font_size', 'ui.font_ui', 'ui.layouts',
  'stats.api_single_read_is_open', 'sys.legacy_port');
