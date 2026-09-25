-- Kipple schema v3: remember whether a stored extraction failure is transient
-- (timeout, connection error, 5xx, 429) or permanent, so POST /api/items/{id}/fulltext
-- can retry a transient one after an hour (design §7.5). item_fulltext.extracted_at is
-- the time of the last attempt. NULL = an error stored before this column existed,
-- treated as permanent.
ALTER TABLE item_fulltext ADD COLUMN error_class TEXT CHECK (error_class IN ('transient', 'permanent'));
