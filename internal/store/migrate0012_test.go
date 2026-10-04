package store

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// undo0012 turns a fresh current database (only the default folder, no feeds or filters, so the drop's
// implicit delete has nothing to cascade) into a schema-11 one: the folders table of 0001, no view.
const undo0012 = `DROP VIEW folder_paths;
CREATE TABLE folders_old (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (length(trim(name)) > 0),
  position   INTEGER NOT NULL DEFAULT 0,
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  created_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;
INSERT INTO folders_old (id, name, position, is_default, created_at) SELECT id, name, position, is_default, created_at FROM folders;
DROP TABLE folders;
ALTER TABLE folders_old RENAME TO folders;
CREATE UNIQUE INDEX idx_folders_one_default ON folders(is_default) WHERE is_default = 1;
CREATE TRIGGER folders_keep_default BEFORE DELETE ON folders WHEN old.is_default = 1
BEGIN SELECT RAISE(ABORT, 'the default folder cannot be deleted'); END;
PRAGMA user_version = 11`

// schema11Library builds a populated schema-11 database: folders (one named "AC/DC", two at the same
// position, a deleted one with the highest id), feeds in them, folder and feed filters, favorites and
// saved-search scopes. It returns the database's path.
func schema11Library(t *testing.T) string {
	t.Helper()
	e := newEnv(t)
	e.exec(undo0012)
	require.False(t, columnNames(t, e.db.Reader(), "folders")["parent_id"])
	e.exec(`INSERT INTO folders (id, name, position, created_at) VALUES (2, 'News', 3, 100), (3, 'AC/DC', 1, 101),
		(4, 'beta', 2, 102), (5, 'Alpha', 2, 103), (9, 'Gone', 9, 104)`)
	e.exec("DELETE FROM folders WHERE id = 9") // sqlite_sequence stays at 9
	for i, folder := range []int64{2, 3, 4, 5, 1} {
		e.exec(`INSERT INTO feeds (id, folder_id, url, url_key, host, title, position, next_fetch_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
			i+1, folder, "https://f"+strconv.Itoa(i)+".example/feed", "f"+strconv.Itoa(i)+".example/feed", "f"+strconv.Itoa(i)+".example", "Feed "+strconv.Itoa(i), i)
	}
	e.exec(`INSERT INTO filters (id, name, scope, folder_id, kind, terms, action) VALUES (1, 'folder rule', 'folder', 3, 'text', '["x"]', 'mute')`)
	e.exec(`INSERT INTO filters (id, name, scope, feed_id, kind, terms, action) VALUES (2, 'feed rule', 'feed', 1, 'text', '["y"]', 'star')`)
	e.exec(`INSERT INTO settings (key, value) VALUES ('library.favorites', '[{"t":"folder","id":"3"},{"t":"feed","id":"2"}]'),
		('library.saved_searches', '[{"id":"s","name":"S","q":"x","scope":{"folder_id":"5"}}]')`)
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())
	return path
}

// 0012 rebuilds folders without changing a row: same ids, names, positions and timestamps, every
// folder at the top level, so every Reader API label is the same string. Feeds, filters, favorites
// and saved searches still point at the same folders, and the schema equals a fresh install's.
func TestMigration0012KeepsEveryFolder(t *testing.T) {
	db := reopen(t, schema11Library(t))
	r := db.Reader()
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(db.path), "backup", "pre-migration-11-*.db"))
	require.Len(t, snaps, 1, "a pre-migration snapshot")

	type row struct {
		id, position, def, created int64
		name, path                 string
		parent                     *int64
	}
	rows, err := r.Query(`SELECT fo.id, fo.position, fo.is_default, fo.created_at, fo.name, fp.path, fo.parent_id
		FROM folders fo JOIN folder_paths fp ON fp.id = fo.id ORDER BY fo.id`)
	require.NoError(t, err)
	var got []row
	for rows.Next() {
		var x row
		require.NoError(t, rows.Scan(&x.id, &x.position, &x.def, &x.created, &x.name, &x.path, &x.parent))
		got = append(got, x)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, got, 5)
	for _, x := range got {
		require.Nil(t, x.parent, x.name)
		require.Equal(t, x.name, x.path, "the path of a top-level folder is its name")
	}
	require.Equal(t, row{id: 3, position: 1, created: 101, name: "AC/DC", path: "AC/DC"}, got[2], "a '/' in a name is kept, never split")
	require.Equal(t, int64(1), got[0].def)

	// Display order is unchanged: position, then name ignoring case.
	names, err := db.FolderNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"Uncategorized", "AC/DC", "Alpha", "beta", "News"}, names)

	require.Equal(t, "2,3,4,5,1", scalar[string](t, r, "SELECT group_concat(folder_id, ',') FROM (SELECT folder_id FROM feeds ORDER BY id)"), "feeds keep their folders")
	require.Equal(t, int64(3), scalar[int64](t, r, "SELECT folder_id FROM filters WHERE id = 1"))
	require.Equal(t, 2, scalar[int](t, r, "SELECT count(*) FROM filters"))
	require.Contains(t, scalar[string](t, r, "SELECT value FROM settings WHERE key = 'library.favorites'"), `"id":"3"`)
	require.Contains(t, scalar[string](t, r, "SELECT value FROM settings WHERE key = 'library.saved_searches'"), `"folder_id":"5"`)
	require.Zero(t, scalar[int](t, r, "SELECT count(*) FROM pragma_foreign_key_check"))

	// The id high-water mark survives the rebuild: the deleted folder 9's id is never reused.
	require.Equal(t, int64(9), scalar[int64](t, r, "SELECT seq FROM sqlite_sequence WHERE name = 'folders'"))
	f, err := db.CreateFolder(t.Context(), "New", 0, -1)
	require.NoError(t, err)
	require.Equal(t, int64(10), f.ID)

	// The default folder is still protected by its trigger and its unique index.
	_, err = db.writer.Exec("DELETE FROM folders WHERE id = 1")
	require.ErrorContains(t, err, "the default folder cannot be deleted")
	_, err = db.writer.Exec("INSERT INTO folders (name, is_default) VALUES ('Second', 1)")
	require.ErrorContains(t, err, "UNIQUE")
	_, err = db.writer.Exec("DELETE FROM folders WHERE id = ?", f.ID)
	require.NoError(t, err)

	// The migrated schema is exactly a fresh install's.
	fresh, _ := openTest(t)
	schema := func(d *DB) string {
		return scalar[string](t, d.Reader(), `SELECT group_concat(type || ' ' || name || ': ' || COALESCE(sql, ''), char(10))
			FROM (SELECT * FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name)`)
	}
	require.Equal(t, schema(fresh), schema(db))
	checkFolderInvariants(t, r)
}

// A schema-11 database with only the default folder migrates too.
func TestMigration0012EmptyLibrary(t *testing.T) {
	e := newEnv(t)
	e.exec(undo0012)
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())
	db := reopen(t, path)
	require.Equal(t, "Uncategorized", scalar[string](t, db.Reader(), "SELECT path FROM folder_paths"))
	checkFolderInvariants(t, db.Reader())
}
