package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
)

// BenchmarkApplyItemsRefetch measures the steady-state refetch of a 500-item
// document (two 250-item chunks) where half the uids are live items and half are
// trimmed tombstones: the two uid lookups applyItems does per chunk.
func BenchmarkApplyItemsRefetch(b *testing.B) {
	clk := clock.NewFake(base)
	db, err := Open(context.Background(), Options{Path: filepath.Join(b.TempDir(), "kipple.db"), Clock: clk})
	require.NoError(b, err)
	b.Cleanup(func() { _ = db.Close() })
	e := &env{t: b, db: db, clk: clk, ctx: context.Background()}
	id, err := db.AddFeed(e.ctx, NewFeed{URL: "http://bench.example/feed", AllowPrivateNet: true})
	require.NoError(b, err)
	e.exec(`UPDATE feeds SET retention = 100 WHERE id = ?`, id)
	body := rss(numbered(200)...)
	res := func() {
		snap := e.snap(id)
		r := e.okResult(snap, body)
		_, err = db.CommitFetch(e.ctx, r)
		require.NoError(b, err)
	}
	res() // 200 new, 100 trimmed
	require.Equal(b, 100, scalar[int](b, db.Reader(), `SELECT count(*) FROM trimmed_items`))
	b.ResetTimer()
	for b.Loop() {
		res()
	}
}

// BenchmarkUIDLookup compares the two uid lookups applyItems needs, as two
// queries (the old shape) and as the single UNION ALL query it now uses, on a
// 200-uid chunk (100 live items, 100 tombstones).
func BenchmarkUIDLookup(b *testing.B) {
	clk := clock.NewFake(base)
	db, err := Open(context.Background(), Options{Path: filepath.Join(b.TempDir(), "kipple.db"), Clock: clk})
	require.NoError(b, err)
	b.Cleanup(func() { _ = db.Close() })
	e := &env{t: b, db: db, clk: clk, ctx: context.Background()}
	id, err := db.AddFeed(e.ctx, NewFeed{URL: "http://bench.example/feed", AllowPrivateNet: true})
	require.NoError(b, err)
	e.exec(`UPDATE feeds SET retention = 100 WHERE id = ?`, id)
	_, err = db.CommitFetch(e.ctx, e.okResult(e.snap(id), rss(numbered(200)...)))
	require.NoError(b, err)
	uids := make([]string, 200)
	for i := range uids {
		uids[i] = fmt.Sprintf("g%d", i)
	}
	js, err := jsonText(uids)
	require.NoError(b, err)
	drain := func(rows *sql.Rows, err error) {
		require.NoError(b, err)
		for rows.Next() {
		}
		require.NoError(b, rows.Close())
	}
	b.Run("two_queries", func(b *testing.B) {
		for b.Loop() {
			drain(db.reader.QueryContext(e.ctx, `SELECT id, uid, content_hash, text_hash, title, author, url, COALESCE(image_url,''), word_count
				FROM items WHERE feed_id = ? AND uid IN (SELECT value FROM json_each(?))`, id, js))
			drain(db.reader.QueryContext(e.ctx, `SELECT uid FROM trimmed_items WHERE feed_id = ? AND uid IN (SELECT value FROM json_each(?))`, id, js))
		}
	})
	b.Run("one_query", func(b *testing.B) {
		for b.Loop() {
			drain(db.reader.QueryContext(e.ctx, `WITH u(uid) AS MATERIALIZED (SELECT value FROM json_each(?2))
				SELECT 0, id, uid, content_hash, text_hash, title, author, url, COALESCE(image_url,''), word_count
				  FROM items WHERE feed_id = ?1 AND uid IN (SELECT uid FROM u)
				UNION ALL
				SELECT 1, 0, uid, '', '', '', '', '', '', 0
				  FROM trimmed_items WHERE feed_id = ?1 AND uid IN (SELECT uid FROM u)`, id, js))
		}
	})
}
