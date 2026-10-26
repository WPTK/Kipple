-- Kipple schema v21: one picture setting for lists. "Thumbnails in Inbox" (client.inbox_thumbs, auto|off)
-- and the Digest's "Images" (client.digest_images, boolean) become client.list_images (boolean, default
-- true; docs/design.md section 7.1c). In every device profile (devices.settings) and in the defaults for new
-- devices (the ui.device_defaults setting), the two old keys are removed, and client.list_images is set to
-- false when either one hid pictures (inbox_thumbs "off", digest_images false); when both showed them the
-- new key is left at its default. The result is never larger than the row it replaces. Only rows that hold
-- an old key are touched. Data only. One-way: going back to schema 20 is a restore of the pre-migration
-- snapshot. Runs once, gated by user_version.
UPDATE devices
SET settings = CASE
  WHEN json_extract(settings, '$."client.inbox_thumbs"') = 'off' OR json_extract(settings, '$."client.digest_images"') = 0
  THEN json_set(json_remove(settings, '$."client.inbox_thumbs"', '$."client.digest_images"'), '$."client.list_images"', json('false'))
  ELSE json_remove(settings, '$."client.inbox_thumbs"', '$."client.digest_images"')
END
WHERE json_valid(settings) AND json_type(settings) = 'object'
  AND (instr(settings, '"client.inbox_thumbs"') > 0 OR instr(settings, '"client.digest_images"') > 0);

UPDATE settings
SET value = CASE
  WHEN json_extract(value, '$."client.inbox_thumbs"') = 'off' OR json_extract(value, '$."client.digest_images"') = 0
  THEN json_set(json_remove(value, '$."client.inbox_thumbs"', '$."client.digest_images"'), '$."client.list_images"', json('false'))
  ELSE json_remove(value, '$."client.inbox_thumbs"', '$."client.digest_images"')
END,
updated_at = unixepoch()
WHERE key = 'ui.device_defaults' AND json_valid(value) AND json_type(value) = 'object'
  AND (instr(value, '"client.inbox_thumbs"') > 0 OR instr(value, '"client.digest_images"') > 0);
