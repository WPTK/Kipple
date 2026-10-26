-- Kipple schema v20: the Coffee break layout ("coffee") becomes the Digest layout with reading-time sections
-- (client.digest_sections = "length", docs/design.md section 7.15). In every device profile (devices.settings)
-- and in the defaults for new devices (the ui.device_defaults setting), a client.layout of "coffee" and a
-- "layout":"coffee" in client.list_overrides become "digest". client.digest_sections becomes "length" when
-- the layout was "coffee", or when a list override was and the layout (the profile's own, else the
-- built-in Editorial) is not the Digest. A row whose value would pass 8192 bytes (the devices CHECK, and the
-- cap the API holds ui.device_defaults to) keeps the layout change and goes without the digest_sections
-- key. Only rows that hold "coffee" are touched. Data only. One-way: going back to schema 19 is a restore of
-- the pre-migration snapshot. Runs once, gated by user_version.
CREATE TEMP TABLE m0020 AS
  WITH src (kind, id, val) AS (
    SELECT 'device', id, json(settings) FROM devices
    WHERE json_valid(settings) AND instr(settings, '"coffee"') > 0
    UNION ALL
    SELECT 'defaults', key, json(value) FROM settings
    WHERE key = 'ui.device_defaults' AND json_valid(value) AND json_type(value) = 'object' AND instr(value, '"coffee"') > 0
  ), conv AS (
    SELECT kind, id,
           replace(replace(val, '"client.layout":"coffee"', '"client.layout":"digest"'), '"layout":"coffee"', '"layout":"digest"') AS next,
           (json_extract(val, '$."client.layout"') = 'coffee'
            OR (instr(coalesce(json_extract(val, '$."client.list_overrides"'), ''), '"layout":"coffee"') > 0
                AND coalesce(json_extract(val, '$."client.layout"'), 'magazine') <> 'digest')) AS reading
    FROM src
  )
  SELECT kind, id, next, reading,
         json_set(next, '$."client.digest_sections"', 'length') AS withsections
  FROM conv;

UPDATE m0020
SET next = withsections
WHERE reading AND length(CAST(withsections AS BLOB)) <= 8192;

UPDATE devices
SET settings = m.next
FROM m0020 AS m
WHERE m.kind = 'device' AND devices.id = m.id;

UPDATE settings
SET value = m.next, updated_at = unixepoch()
FROM m0020 AS m
WHERE m.kind = 'defaults' AND settings.key = m.id;

DROP TABLE m0020;
