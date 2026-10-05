-- Kipple schema v16: a feed or folder list overrides its layout, order and view in one place.
-- The device profile key client.layout_overrides ({"feed":{id:layout},"folder":{id:layout}}) becomes
-- client.list_overrides ({"feed":{id:{"layout":…}},"folder":{id:{"layout":…}}}), whose entries may also
-- carry "order" and "view" (docs/design.md §7.1c). Every stored layout override is carried over as the
-- "layout" field of its entry, in each device profile (devices.settings) and in the defaults for new
-- devices (the ui.device_defaults setting); the old key is removed in the same statement, so no row holds
-- both. Only rows that hold the old key are touched; a value that is not a string is dropped (the API
-- never stored one). A device profile has a CHECK of 8192 bytes, and each entry grows by its "layout"
-- wrapper: a profile that would no longer fit loses its layout overrides instead of failing the
-- migration (that takes well over a hundred overrides on one device). Data only and
-- small (the devices table holds at most a few hundred rows). One-way: going back to schema 15 is a
-- restore of the pre-migration snapshot. Runs once, gated by user_version.
UPDATE devices
SET settings = CASE WHEN length(c.next) <= 8192 THEN c.next
                    ELSE json_remove(devices.settings, '$."client.layout_overrides"') END
FROM (
  SELECT d.id AS id,
         json_remove(json_set(d.settings, '$."client.list_overrides"', json_object(
           'feed', json((SELECT json_group_object(e.key, json_object('layout', e.value))
                         FROM json_each(d.settings, '$."client.layout_overrides".feed') AS e WHERE e.type = 'text')),
           'folder', json((SELECT json_group_object(e.key, json_object('layout', e.value))
                           FROM json_each(d.settings, '$."client.layout_overrides".folder') AS e WHERE e.type = 'text')))),
           '$."client.layout_overrides"') AS next
  FROM devices AS d
  WHERE json_type(d.settings, '$."client.layout_overrides"') IS NOT NULL
) AS c
WHERE devices.id = c.id;

UPDATE settings
SET value = json_remove(json_set(value, '$."client.list_overrides"', json_object(
      'feed', json((SELECT json_group_object(e.key, json_object('layout', e.value))
                    FROM json_each(settings.value, '$."client.layout_overrides".feed') AS e WHERE e.type = 'text')),
      'folder', json((SELECT json_group_object(e.key, json_object('layout', e.value))
                      FROM json_each(settings.value, '$."client.layout_overrides".folder') AS e WHERE e.type = 'text')))),
      '$."client.layout_overrides"'),
    updated_at = unixepoch()
WHERE key = 'ui.device_defaults'
  AND json_type(value) = 'object'
  AND json_type(value, '$."client.layout_overrides"') IS NOT NULL;
