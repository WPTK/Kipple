package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 0014 clears the host placeholder an earlier subscribe stored as title, only on feeds that never
// fetched successfully; a fetched feed whose own title equals its host keeps it.
func TestMigration0014ClearsPlaceholderTitles(t *testing.T) {
	e := newEnv(t)
	placeholder := e.addFeed("https://a.example/feed")
	fetched := e.addFeed("https://xkcd.example/atom.xml")
	named := e.addFeed("https://b.example/feed")
	e.exec("UPDATE feeds SET title = host WHERE id IN (?, ?)", placeholder, fetched)
	e.exec("UPDATE feeds SET last_success_at = 1 WHERE id = ?", fetched)
	e.exec("UPDATE feeds SET title = 'B News' WHERE id = ?", named)
	e.exec("PRAGMA user_version = 13")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	r := reopen(t, path).Reader()
	title := func(id int64) string { return scalar[string](t, r, "SELECT title FROM feeds WHERE id = ?", id) }
	require.Equal(t, "", title(placeholder))
	require.Equal(t, "xkcd.example", title(fetched))
	require.Equal(t, "B News", title(named))
}
