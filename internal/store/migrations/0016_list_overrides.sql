-- Kipple schema v16: a feed or folder list overrides its layout, order and view in one place.
-- The device profile key client.layout_overrides ({"feed":{id:layout},"folder":{id:layout}}) becomes
-- client.list_overrides ({"feed":{id:{"layout":…}},"folder":{id:{"layout":…}}}), whose entries may also
-- carry "order" and "view" (docs/design.md §7.1c). Every stored layout override is carried over as the
-- "layout" field of its entry, in each device profile (devices.settings) and in the defaults for new
-- devices (the ui.device_defaults setting); the old key is removed in the same statement, so no row holds
-- both. Only rows that hold the old key are touched; a value that is not a string is dropped (the API
-- never stored one).
--
-- Each entry grows by its "layout" wrapper. A row whose new key would pass its budget (4096 bytes,
-- store.MaxListOverridesBytes, which the API holds the key to) or whose whole value would pass 8192
-- bytes (the devices CHECK, and the cap the API holds ui.device_defaults to) loses its layout overrides
-- instead of failing the migration and the start; that takes well over a hundred overrides on one
-- device. Each such loss is counted in temp.migration_notice, which the runner logs as a warning.
-- Data only and small (the devices table holds at most a few hundred rows). One-way: going back to
-- schema 15 is a restore of the pre-migration snapshot. Runs once, gated by user_version.
CREATE TEMP TABLE migration_notice (message TEXT NOT NULL);

-- The converted value of each row that holds the old key: `lists` is the new key alone, `next` the
-- whole row with the new key in place of the old one.
CREATE TEMP TABLE m0016 AS
  WITH src (kind, id, val) AS (
    SELECT 'device', id, settings FROM devices
    WHERE json_type(settings, '$."client.layout_overrides"') IS NOT NULL
    UNION ALL
    SELECT 'defaults', key, value FROM settings
    WHERE key = 'ui.device_defaults' AND json_type(value) = 'object'
      AND json_type(value, '$."client.layout_overrides"') IS NOT NULL
  ), conv AS (
    SELECT kind, id, val, json_object(
             'feed', json((SELECT json_group_object(e.key, json_object('layout', e.value))
                           FROM json_each(src.val, '$."client.layout_overrides".feed') AS e WHERE e.type = 'text')),
             'folder', json((SELECT json_group_object(e.key, json_object('layout', e.value))
                             FROM json_each(src.val, '$."client.layout_overrides".folder') AS e WHERE e.type = 'text'))) AS lists
    FROM src
  )
  SELECT kind, id, lists,
         json_remove(json_set(val, '$."client.list_overrides"', json(lists)), '$."client.layout_overrides"') AS next
  FROM conv;

-- A row that does not fit keeps everything but the old key. Sizes are bytes (the API measures its caps in
-- bytes of JSON); for a profile that is also at least the characters the devices CHECK counts.
UPDATE m0016
SET next = json_remove(next, '$."client.list_overrides"')
WHERE length(CAST(lists AS BLOB)) > 4096 OR length(CAST(next AS BLOB)) > 8192;

INSERT INTO migration_notice (message)
  SELECT 'migration 0016: ' || count(*) || ' device profile(s) lost their per-feed and per-folder layouts: too large for the new override format'
  FROM m0016 WHERE kind = 'device' AND json_type(next, '$."client.list_overrides"') IS NULL
  HAVING count(*) > 0;
INSERT INTO migration_notice (message)
  SELECT 'migration 0016: the defaults for new devices lost their per-feed and per-folder layouts: too large for the new override format'
  FROM m0016 WHERE kind = 'defaults' AND json_type(next, '$."client.list_overrides"') IS NULL;

UPDATE devices
SET settings = m.next
FROM m0016 AS m
WHERE m.kind = 'device' AND devices.id = m.id;

UPDATE settings
SET value = m.next, updated_at = unixepoch()
FROM m0016 AS m
WHERE m.kind = 'defaults' AND settings.key = m.id;

DROP TABLE m0016;
