-- kipple:foreign-keys-off
-- Kipple schema v12: nested folders (docs/design.md §2.2a). folders gains parent_id (NULL = top level). The
-- column-level UNIQUE on name cannot be dropped in place, so the table is rebuilt; feeds and filters keep
-- pointing at folders(id), and the runner's foreign_key_check verifies them before the commit. No data
-- changes: every folder becomes top level with its id, name, position and created_at unchanged, so every
-- Reader API label stays the same string. The sqlite_sequence high-water mark is carried over, so a deleted
-- folder's id is never handed out again.
--
-- Rules the database enforces: sibling names are unique ignoring ASCII case (idx_folders_sibling_name), the
-- default folder is top level, a folder is not its own parent, and deleting a folder deletes its subfolders
-- (ON DELETE CASCADE; their feeds fall back to folder 1 through feeds.folder_id ON DELETE SET DEFAULT).
-- The rules a CHECK cannot express (unique full paths, no cycles, at most 8 levels, no subfolders under the
-- default folder) are kept by the single writer in internal/store/folders.go.
--
-- The view is dropped first because the rename below re-checks every view: the store tests build an older
-- schema from the current one and replay the later migrations over it, as every migration since 0004 allows.
DROP VIEW IF EXISTS folder_paths;
CREATE TABLE folders_new (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  parent_id  INTEGER REFERENCES folders(id) ON DELETE CASCADE,
  name       TEXT NOT NULL COLLATE NOCASE CHECK (length(trim(name)) > 0),
  position   INTEGER NOT NULL DEFAULT 0,
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  created_at INTEGER NOT NULL DEFAULT (unixepoch()),
  CHECK (is_default = 0 OR parent_id IS NULL),
  CHECK (parent_id IS NOT id)
) STRICT;
INSERT INTO folders_new (id, parent_id, name, position, is_default, created_at)
  SELECT id, NULL, name, position, is_default, created_at FROM folders;
UPDATE sqlite_sequence
  SET seq = max(seq, COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'folders'), 0))
  WHERE name = 'folders_new';
DROP TABLE folders;
ALTER TABLE folders_new RENAME TO folders;

CREATE UNIQUE INDEX idx_folders_one_default ON folders(is_default) WHERE is_default = 1;
CREATE TRIGGER folders_keep_default BEFORE DELETE ON folders WHEN old.is_default = 1
BEGIN SELECT RAISE(ABORT, 'the default folder cannot be deleted'); END;
CREATE UNIQUE INDEX idx_folders_sibling_name ON folders(ifnull(parent_id, 0), name COLLATE NOCASE);
CREATE INDEX idx_folders_parent ON folders(parent_id) WHERE parent_id IS NOT NULL;

-- One row per folder: its full path (names joined with '/', the Reader API label), its depth (1 = top
-- level) and a sort key that lists the tree in pre-order with siblings by (position, name), the order the
-- flat folder list always had.
CREATE VIEW folder_paths (id, parent_id, name, path, depth, sort_key) AS
  WITH RECURSIVE
    ranked(id, parent_id, name, rk) AS (
      SELECT id, parent_id, name,
             row_number() OVER (PARTITION BY ifnull(parent_id, 0) ORDER BY position, name COLLATE NOCASE)
      FROM folders),
    t(id, parent_id, name, path, depth, sort_key) AS (
      SELECT id, parent_id, name, name, 1, printf('%010d', rk) FROM ranked WHERE parent_id IS NULL
      UNION ALL
      SELECT r.id, r.parent_id, r.name, t.path || '/' || r.name, t.depth + 1, t.sort_key || '.' || printf('%010d', r.rk)
      FROM ranked r JOIN t ON r.parent_id = t.id)
  SELECT id, parent_id, name, path, depth, sort_key FROM t;
