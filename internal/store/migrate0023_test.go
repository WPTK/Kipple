package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 0023 adds the per-feed unread ledger index and changes no row: a schema-22 database (the index dropped
// and user_version stepped back) upgrades to the same objects as a fresh one.
func TestMigration0023AddsTheLedgerIndex(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("https://a.example/feed")
	for i := int64(1); i <= 40; i++ {
		e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			VALUES (?1, ?2, 't' || ?1, ?1 % 4 != 0, 1, 1)`, 1000+i, feed)
	}
	fresh := schemaObjects(t, e.db.Reader())
	e.exec("DROP INDEX idx_trimmed_feed_unread")
	e.exec("PRAGMA user_version = 22")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db := reopen(t, path)
	r := db.Reader()
	require.Equal(t, "CREATE INDEX idx_trimmed_feed_unread ON trimmed_items(feed_id, id) WHERE read = 0",
		scalar[string](t, r, "SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'idx_trimmed_feed_unread'"))
	require.Equal(t, fresh, schemaObjects(t, r))
	require.Equal(t, 40, scalar[int](t, r, "SELECT count(*) FROM trimmed_items"))
	require.Equal(t, 10, scalar[int](t, r, "SELECT count(*) FROM trimmed_items WHERE read = 0"))
	requireCleanIntegrity(t, r)
}
