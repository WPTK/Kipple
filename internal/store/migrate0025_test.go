package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 0025 brings every stored sort_at that is past its crawl second (id / 1e6) down to it, in items and in the
// restore stubs. Rows dated at or before their fetch, and published_at itself, are left as they are.
func TestMigration0025SortAtNotAheadOfFetch(t *testing.T) {
	t.Parallel()
	const crawl = int64(1_800_000_000)
	path := filepath.Join(t.TempDir(), "kipple.db")
	raw, err := sql.Open("sqlite", buildDSN(path, "writer"))
	require.NoError(t, err)
	require.NoError(t, BuildSchema(t.Context(), raw, 24))
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := raw.Exec(q, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO feeds (id, url, url_key, host) VALUES (1, 'http://a.example/feed', 'a.example/feed', 'a.example')`)
	item := func(n, published, sortAt int64) {
		exec(`INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title) VALUES (?, 1, ?, ?, ?, 'h', 't', 'x')`,
			crawl*1_000_000+n, published, sortAt, "u"+string(rune('a'+n)))
	}
	item(1, crawl+3600, crawl+3600)    // an hour ahead of the fetch
	item(2, crawl+999999, crawl+86400) // far ahead: it had been held at a day ahead
	item(3, crawl-5000, crawl-5000)    // an honest, older article
	item(4, crawl, crawl)              // dated exactly at the fetch
	exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (?, 1, 'g1', 0, 1, 1), (?, 1, 'g2', 0, 1, 1)`,
		crawl*1_000_000+10, crawl*1_000_000+11)
	stub := func(n, published, sortAt int64) {
		exec(`INSERT INTO trimmed_content (id, published_at, sort_at, word_count, content_hash, text_hash, url, title, author, content_html, content_text)
			VALUES (?, ?, ?, 0, 'h', 't', '', 'x', '', '', '')`, crawl*1_000_000+n, published, sortAt)
	}
	stub(10, crawl+7200, crawl+7200)
	stub(11, crawl-60, crawl-60)
	require.NoError(t, raw.Close())

	db := reopen(t, path)
	sortAt := func(table string, n int64) int64 {
		return scalar[int64](t, db.Reader(), "SELECT sort_at FROM "+table+" WHERE id = ?", crawl*1_000_000+n)
	}
	require.Equal(t, crawl, sortAt("items", 1))
	require.Equal(t, crawl, sortAt("items", 2))
	require.Equal(t, crawl-5000, sortAt("items", 3))
	require.Equal(t, crawl, sortAt("items", 4))
	require.Equal(t, crawl, sortAt("trimmed_content", 10))
	require.Equal(t, crawl-60, sortAt("trimmed_content", 11))
	require.Equal(t, crawl+999999, scalar[int64](t, db.Reader(), "SELECT published_at FROM items WHERE id = ?", crawl*1_000_000+2), "published_at is untouched")
	requireCleanIntegrity(t, db.Reader())
}
