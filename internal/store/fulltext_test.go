package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/fetch"
)

func TestFulltextErrorClassRoundTripAndURLGuard(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://ex.com/feed")
	e.fetchBody(id, rss(numbered(2)...))
	item := int64(scalar[int](t, e.db.Reader(), "SELECT id FROM items WHERE feed_id = ? ORDER BY id LIMIT 1", id))
	url := scalar[string](t, e.db.Reader(), "SELECT url FROM items WHERE id = ?", item)

	require.NoError(t, e.db.SaveFulltext(e.ctx, item, 1000, FulltextSave{Error: "the page answered HTTP 503", ErrorTransient: true}))
	it, ok, err := e.db.GetFulltextItem(e.ctx, item)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, it.HasRow)
	require.True(t, it.ErrorTransient)
	require.EqualValues(t, 1000, it.AttemptedAt)

	require.NoError(t, e.db.SaveFulltext(e.ctx, item, 2000, FulltextSave{Error: "the page answered HTTP 404"}))
	it, _, _ = e.db.GetFulltextItem(e.ctx, item)
	require.False(t, it.ErrorTransient)
	require.EqualValues(t, 2000, it.AttemptedAt)

	e.exec("UPDATE feeds SET fulltext = 1 WHERE id = ?", id)
	// A background result for a URL the item no longer has is dropped.
	written, err := e.db.SaveFulltextIfURL(e.ctx, item, "https://other/", 3000, FulltextSave{HTML: "<p>x</p>", Text: "x", WordCount: 1})
	require.NoError(t, err)
	require.False(t, written)
	written, err = e.db.SaveFulltextIfURL(e.ctx, item, url, 3000, FulltextSave{HTML: "<p>x</p>", Text: "x", WordCount: 1})
	require.NoError(t, err)
	require.True(t, written)
	it, _, _ = e.db.GetFulltextItem(e.ctx, item)
	require.Equal(t, "<p>x</p>", it.HTML)
	require.Empty(t, it.Error)
	require.False(t, it.ErrorTransient)
	require.Nil(t, scalar[*string](t, e.db.Reader(), "SELECT error_class FROM item_fulltext WHERE item_id = ?", item))

	// Full text switched off meanwhile (feed or item) drops a background save, for
	// successes and failures; an item forced on still saves under a feed that is off.
	e.exec("DELETE FROM item_fulltext WHERE item_id = ?", item)
	e.exec("UPDATE feeds SET fulltext = 0 WHERE id = ?", id)
	for _, sv := range []FulltextSave{{HTML: "<p>y</p>", Text: "y", WordCount: 1}, {Error: "boom", ErrorTransient: true}} {
		written, err = e.db.SaveFulltextIfURL(e.ctx, item, url, 5000, sv)
		require.NoError(t, err)
		require.False(t, written, "feed off")
	}
	e.exec("UPDATE feeds SET fulltext = 1 WHERE id = ?", id)
	e.exec("UPDATE items SET fulltext_mode = 0 WHERE id = ?", item)
	written, err = e.db.SaveFulltextIfURL(e.ctx, item, url, 5000, FulltextSave{HTML: "<p>y</p>", Text: "y", WordCount: 1})
	require.NoError(t, err)
	require.False(t, written, "item forced off")
	e.exec("UPDATE feeds SET fulltext = 0 WHERE id = ?", id)
	e.exec("UPDATE items SET fulltext_mode = 1 WHERE id = ?", item)
	written, err = e.db.SaveFulltextIfURL(e.ctx, item, url, 5000, FulltextSave{HTML: "<p>y</p>", Text: "y", WordCount: 1})
	require.NoError(t, err)
	require.True(t, written, "item forced on")
	require.Zero(t, scalar[int](t, e.db.Reader(), "SELECT count(*) FROM item_fulltext WHERE error IS NOT NULL"))

	// A deleted item is not resurrected as an orphan row.
	e.exec("DELETE FROM items WHERE id = ?", item)
	written, err = e.db.SaveFulltextIfURL(e.ctx, item, url, 4000, FulltextSave{Error: "x"})
	require.NoError(t, err)
	require.False(t, written)
}

// Migration 0003 adds error_class; an error row stored before it stays valid
// and reads as unclassified (permanent).
func TestMigration0003KeepsExistingFulltextErrors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")
	clk := clock.NewFake(base)
	db, err := Open(ctx, Options{Path: path, Clock: clk})
	require.NoError(t, err)
	e := &env{t: t, db: db, clk: clk, ctx: ctx}
	fid := e.addFeed("https://ex.com/feed")
	e.fetchBody(fid, rss(numbered(1)...))
	item := int64(scalar[int](t, db.Reader(), "SELECT id FROM items WHERE feed_id = ?", fid))
	e.exec(`INSERT INTO item_fulltext (item_id, extracted_at, error) VALUES (?, 5, 'the page answered HTTP 500')`, item)
	// Back to schema v2 (undo 0004, then 0003).
	downgradeTo3(t, e)
	e.exec(`ALTER TABLE item_fulltext DROP COLUMN error_class`)
	e.exec(`PRAGMA user_version = 2`)
	require.NoError(t, db.Close())

	db, err = Open(ctx, Options{Path: path, Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	v, err := db.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	it, ok, err := db.GetFulltextItem(ctx, item)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "the page answered HTTP 500", it.Error)
	require.False(t, it.ErrorTransient, "an old error of unknown class is permanent")
	require.EqualValues(t, 5, it.AttemptedAt)
	require.Nil(t, scalar[*string](t, db.Reader(), "SELECT error_class FROM item_fulltext WHERE item_id = ?", item))
	require.Error(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE item_fulltext SET error_class = 'bogus' WHERE item_id = ?", item)
		return err
	}), "error_class is constrained")
}

// Article extraction resolves the User-Agent like a feed fetch does (mode,
// remembered fallback, per-feed override).
func TestGetFulltextItemResolvesUserAgent(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://ex.com/feed")
	e.fetchBody(id, rss(numbered(1)...))
	item := int64(scalar[int](t, e.db.Reader(), "SELECT id FROM items WHERE feed_id = ?", id))
	get := func() FulltextItem {
		it, ok, err := e.db.GetFulltextItem(e.ctx, item)
		require.NoError(t, err)
		require.True(t, ok)
		return it
	}
	it := get() // default mode is browser_on_failure
	require.Equal(t, "", it.UserAgent)
	require.Equal(t, fetch.BrowserUserAgent, it.RetryUserAgent)

	e.exec("UPDATE feeds SET ua_fallback = 1 WHERE id = ?", id)
	it = get()
	require.Equal(t, fetch.BrowserUserAgent, it.UserAgent)
	require.Empty(t, it.RetryUserAgent)

	e.exec("UPDATE feeds SET ua_fallback = 0 WHERE id = ?", id)
	e.exec(`INSERT INTO settings (key, value) VALUES ('fetch.user_agent_mode', '"browser_always"')`)
	require.Equal(t, fetch.BrowserUserAgent, get().UserAgent)

	e.exec("UPDATE settings SET value = '\"default\"' WHERE key = 'fetch.user_agent_mode'")
	it = get()
	require.Empty(t, it.UserAgent+it.RetryUserAgent)

	e.exec("UPDATE feeds SET user_agent = 'Feed/2' WHERE id = ?", id)
	require.Equal(t, "Feed/2", get().UserAgent)
}
