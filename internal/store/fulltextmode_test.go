package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func iptr(n int) *int { return &n }

func TestEffectiveFulltextTable(t *testing.T) {
	for _, tc := range []struct {
		item      *int
		feed, all bool
		want      int
	}{
		{nil, false, false, 0},
		{nil, true, false, 1},
		{nil, false, true, 1}, // the global switch beats a feed that is off
		{nil, true, true, 1},
		{iptr(0), true, false, 0},
		{iptr(0), true, true, 0}, // an item forced off still wins
		{iptr(1), false, false, 1},
		{iptr(1), false, true, 1},
	} {
		require.Equal(t, tc.want, EffectiveFulltext(tc.item, tc.feed, tc.all), fmt.Sprint(tc))
	}
}

// The SQL builder must agree with the Go function for every input.
func TestFulltextModeSQLMatchesGo(t *testing.T) {
	e := newEnv(t)
	for _, all := range []bool{false, true} {
		for _, item := range []any{nil, 0, 1} {
			for _, feed := range []int{0, 1} {
				var got int
				require.NoError(t, e.db.reader.QueryRow("SELECT "+FulltextModeSQL("?1", "?2", all), item, feed).Scan(&got))
				var im *int
				if item != nil {
					im = iptr(item.(int))
				}
				require.Equal(t, EffectiveFulltext(im, feed == 1, all), got, fmt.Sprint(all, item, feed))
			}
		}
	}
}

// ftFixture is one feed with two items: a (mode NULL) and b (mode forced 0).
func ftFixture(t *testing.T, e *env, feedFT bool) (feed, a, b int64) {
	t.Helper()
	feed = e.addFeed("https://ex.com/feed")
	e.fetchBody(feed, rss(numbered(2)...))
	e.exec("UPDATE feeds SET fulltext = ? WHERE id = ?", feedFT, feed)
	var ids []int64
	rows, err := e.db.reader.Query("SELECT id FROM items WHERE feed_id = ? ORDER BY id", feed)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Close())
	require.Len(t, ids, 2)
	e.exec("UPDATE items SET fulltext_mode = 0 WHERE id = ?", ids[1])
	return feed, ids[0], ids[1]
}

func (e *env) setAll(v any) {
	e.t.Helper()
	require.NoError(e.t, e.db.SetSettings(e.ctx, map[string]any{SettingFulltextAll: v}))
}

func TestFulltextAllCacheFlipsImmediately(t *testing.T) {
	e := newEnv(t)
	require.False(t, e.db.FulltextAll(e.ctx))
	e.setAll(true)
	require.True(t, e.db.FulltextAll(e.ctx))
	e.setAll(nil) // null resets to the default
	require.False(t, e.db.FulltextAll(e.ctx))
	e.setAll(true)
	require.True(t, e.db.FulltextAll(e.ctx))
	e.setAll(false)
	require.False(t, e.db.FulltextAll(e.ctx))
}

func TestFulltextAllGetFulltextItemAndSnapshot(t *testing.T) {
	e := newEnv(t)
	feed, a, b := ftFixture(t, e, false)
	eff := func(id int64) int {
		it, ok, err := e.db.GetFulltextItem(e.ctx, id)
		require.NoError(t, err)
		require.True(t, ok)
		return it.Effective
	}
	require.Equal(t, 0, eff(a))
	require.False(t, e.snap(feed).Fulltext)
	e.setAll(true)
	require.Equal(t, 1, eff(a), "follows the switch (feed is off)")
	require.Equal(t, 0, eff(b), "item override 0 wins")
	require.True(t, e.snap(feed).Fulltext, "ingest picks new items for a feed that is off")
	e.exec("UPDATE items SET fulltext_mode = 1 WHERE id = ?", a)
	e.setAll(false)
	require.Equal(t, 1, eff(a), "item override 1 wins")
	require.False(t, e.snap(feed).Fulltext)
	// Flipping the switch backfills nothing.
	e.setAll(true)
	require.Equal(t, 0, e.count("SELECT count(*) FROM item_fulltext"))
	require.Equal(t, 0, e.count("SELECT count(*) FROM items WHERE fulltext_mode IS NULL"), "no stored mode was touched")
}

func TestFulltextAllGuardedSave(t *testing.T) {
	e := newEnv(t)
	_, a, b := ftFixture(t, e, false)
	url := func(id int64) string {
		return scalar[string](t, e.db.Reader(), "SELECT url FROM items WHERE id = ?", id)
	}
	save := FulltextSave{HTML: "<p>x</p>", Text: "x", WordCount: 1}
	ok, err := e.db.SaveFulltextIfURL(e.ctx, a, url(a), 10, save)
	require.NoError(t, err)
	require.False(t, ok, "feed off, switch off: dropped")
	e.setAll(true)
	ok, err = e.db.SaveFulltextIfURL(e.ctx, a, url(a), 10, save)
	require.NoError(t, err)
	require.True(t, ok, "switch on: saved")
	ok, err = e.db.SaveFulltextIfURL(e.ctx, b, url(b), 10, save)
	require.NoError(t, err)
	require.False(t, ok, "item forced off: dropped even with the switch on")
}

func TestFulltextAllItemDetailAndBootstrap(t *testing.T) {
	e := newEnv(t)
	_, a, b := ftFixture(t, e, false)
	e.exec("INSERT INTO item_fulltext (item_id, content_html, content_text, word_count, extracted_at) VALUES (?, '<p>FULL</p>', 'FULL', 1, 1)", a)
	e.exec("INSERT INTO item_fulltext (item_id, content_html, content_text, word_count, extracted_at) VALUES (?, '<p>FULLB</p>', 'FULLB', 1, 1)", b)
	now := e.clk.Now().Unix()
	det, ok, err := e.db.GetItem(e.ctx, a, now)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 0, det.Fulltext.Effective)
	require.True(t, det.Fulltext.Available)
	require.NotEqual(t, "<p>FULL</p>", det.ContentHTML)

	e.setAll(true)
	det, _, _ = e.db.GetItem(e.ctx, a, now)
	require.Equal(t, 1, det.Fulltext.Effective)
	require.Nil(t, det.Fulltext.Mode)
	require.Equal(t, "<p>FULL</p>", det.ContentHTML, "content selection follows the switch")
	det, _, _ = e.db.GetItem(e.ctx, b, now)
	require.Equal(t, 0, det.Fulltext.Effective, "item override 0")
	require.NotEqual(t, "<p>FULLB</p>", det.ContentHTML)

	feeds, err := e.db.UIFeeds(e.ctx, StatusEnv{Now: e.clk.Now()})
	require.NoError(t, err)
	require.Len(t, feeds, 1)
	require.False(t, feeds[0].Fulltext, "the feed's own flag is unchanged")
	require.True(t, feeds[0].FulltextEffective)
	e.setAll(false)
	feeds, _ = e.db.UIFeeds(e.ctx, StatusEnv{Now: e.clk.Now()})
	require.False(t, feeds[0].FulltextEffective)
}

func TestFulltextAllStubEffective(t *testing.T) {
	e := newEnv(t)
	feed, _, _ := ftFixture(t, e, false)
	e.fetchBody(feed, rss(numbered(70)...))
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", feed)
	_, err0 := e.db.TrimOnly(e.ctx, feed, "retention")
	require.NoError(t, err0)
	now := e.clk.Now().Unix()
	require.Positive(t, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ?", feed))
	a := scalar[int64](t, e.db.Reader(), "SELECT id FROM trimmed_items WHERE feed_id = ? LIMIT 1", feed)
	det, ok, err := e.db.GetItem(e.ctx, a, now)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, det.Trimmed)
	require.Equal(t, 0, det.Fulltext.Effective)
	e.setAll(true)
	det, _, _ = e.db.GetItem(e.ctx, a, now)
	require.Equal(t, 1, det.Fulltext.Effective)
}

func TestFulltextAllHold(t *testing.T) {
	e := newEnv(t)
	feed, a, b := ftFixture(t, e, false)
	list := func() map[int64]bool {
		got := map[int64]bool{}
		_, _, err := e.db.StreamIDs(e.ctx, StreamFilter{FeedID: feed, HoldCut: 1}, IDPage{N: 100}, func(id int64) error { got[id] = true; return nil })
		require.NoError(t, err)
		return got
	}
	content := func() map[int64]bool {
		got := map[int64]bool{}
		require.NoError(t, e.db.StreamItems(e.ctx, []int64{a, b}, true, 1, func(r *ContentRow) error { got[r.ID] = true; return nil }))
		return got
	}
	unread := func() int64 {
		rows, err := e.db.UnreadCounts(e.ctx, 1)
		require.NoError(t, err)
		var n int64
		for _, r := range rows {
			n += r.Count
		}
		return n
	}
	e.db.MarkFulltextPending(a, b) // both were queued by the ingest pool
	// Hold cut of 1: every item counts as younger than the hold window, so only
	// items that are not full text are visible.
	require.Equal(t, map[int64]bool{a: true, b: true}, list())
	require.Equal(t, map[int64]bool{a: true, b: true}, content())
	require.EqualValues(t, 2, unread())

	e.setAll(true)
	require.Equal(t, map[int64]bool{b: true}, list(), "a is held now (b is forced off)")
	require.Equal(t, map[int64]bool{b: true}, content())
	require.EqualValues(t, 1, unread())

	// Mark-all-as-read skips the held item too.
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkAllRead(ctx, tx, MarkScope{FeedID: feed, HoldCut: 1, HoldPending: e.db.HoldPending()}, 1<<62, e.clk.Now().Unix())
		return err
	}))
	require.Equal(t, 1, e.count("SELECT read FROM items WHERE id = ?", b))
	require.Equal(t, 0, e.count("SELECT read FROM items WHERE id = ?", a))

	// A stored result releases the hold.
	e.exec("INSERT INTO item_fulltext (item_id, content_html, extracted_at) VALUES (?, '<p>x</p>', 1)", a)
	require.Equal(t, map[int64]bool{a: true, b: true}, list())
}

func TestFavoritesDroppedWithFolderAndFeed(t *testing.T) {
	e := newEnv(t)
	fo, err := e.db.CreateFolder(e.ctx, "News", 0, 5)
	require.NoError(t, err)
	fo2, err := e.db.CreateFolder(e.ctx, "Other", 0, 6)
	require.NoError(t, err)
	f1 := e.addFeed("https://ex.com/one")
	f2 := e.addFeed("https://ex.com/two")
	s := func(n int64) string { return strconv.FormatInt(n, 10) }
	favs := []any{
		map[string]any{"t": "folder", "id": s(fo.ID)}, map[string]any{"t": "folder", "id": s(fo2.ID)},
		map[string]any{"t": "feed", "id": s(f1)}, map[string]any{"t": "feed", "id": s(f2)},
	}
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{SettingFavorites: favs}))
	get := func() string {
		return scalar[string](t, e.db.Reader(), "SELECT value FROM settings WHERE key = ?", SettingFavorites)
	}

	_, err = e.db.DeleteFolder(e.ctx, fo.ID)
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf(`[{"t":"folder","id":"%d"},{"t":"feed","id":"%d"},{"t":"feed","id":"%d"}]`, fo2.ID, f1, f2), get())

	require.NoError(t, e.db.DeleteFeed(e.ctx, f1, false))
	_, err = e.db.Unsubscribe(e.ctx, []FeedRef{{ID: f2}})
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf(`[{"t":"folder","id":"%d"}]`, fo2.ID), get())

	// No stored row: deleting still works and writes nothing.
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{SettingFavorites: nil}))
	f3 := e.addFeed("https://ex.com/three")
	require.NoError(t, e.db.DeleteFeed(e.ctx, f3, false))
	require.Equal(t, 0, e.count("SELECT count(*) FROM settings WHERE key = ?", SettingFavorites))
}

func TestFulltextAllCancelledContextDoesNotPoisonCache(t *testing.T) {
	e := newEnv(t)
	e.setAll(true) // invalidates: the next read is a cache miss
	cctx, cancel := context.WithCancel(e.ctx)
	cancel()
	// The miss loads under a context that survives the caller's cancellation, so
	// even a dead request gets the real value...
	require.True(t, e.db.FulltextAll(cctx))
	// ...and whatever it cached is the truth for the next, healthy caller.
	require.True(t, e.db.FulltextAll(e.ctx))
}

func TestBoolCacheSkipsFailedLoads(t *testing.T) {
	var c boolCache
	calls := 0
	fail := func() (bool, error) { calls++; return false, errors.New("boom") }
	require.False(t, c.get(fail))
	require.False(t, c.get(fail))
	require.Equal(t, 2, calls, "a failed load is retried, never cached")
	require.True(t, c.get(func() (bool, error) { return true, nil }))
	require.True(t, c.get(fail), "a good value is cached")
}

func TestNormalizeFavoriteID(t *testing.T) {
	for in, want := range map[string]string{"7": "7", "007": "7", "9223372036854775807": "9223372036854775807"} {
		got, ok := NormalizeFavoriteID(in)
		require.True(t, ok, in)
		require.Equal(t, want, got)
	}
	for _, in := range []string{"", "0", "000", "-1", "+5", "1a", "9223372036854775808", "9999999999999999999"} {
		_, ok := NormalizeFavoriteID(in)
		require.False(t, ok, in)
	}
}

// Every path that deletes a folder or feed cleans its favorite in the same
// transaction: the Reader API label rename-merge and disable-tag, too.
func TestFavoritesDroppedByReaderLabelPaths(t *testing.T) {
	e := newEnv(t)
	a, err := e.db.CreateFolder(e.ctx, "A", 0, 5)
	require.NoError(t, err)
	b, err := e.db.CreateFolder(e.ctx, "B", 0, 6)
	require.NoError(t, err)
	c, err := e.db.CreateFolder(e.ctx, "C", 0, 7)
	require.NoError(t, err)
	s := func(n int64) string { return strconv.FormatInt(n, 10) }
	// "0" + s(id) is a legacy spelling written before ids were normalised.
	favs := []any{
		map[string]any{"t": "folder", "id": "0" + s(a.ID)}, map[string]any{"t": "folder", "id": s(b.ID)},
		map[string]any{"t": "folder", "id": s(c.ID)}, map[string]any{"t": "feed", "id": s(a.ID)},
	}
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{SettingFavorites: favs}))
	get := func() string {
		return scalar[string](t, e.db.Reader(), "SELECT value FROM settings WHERE key = ?", SettingFavorites)
	}

	// Merge A into B: A is deleted, so its favorite (legacy spelling) goes.
	require.NoError(t, renameLabel(e.ctx, e.db, a.ID, "B"))
	require.JSONEq(t, fmt.Sprintf(`[{"t":"folder","id":"%d"},{"t":"folder","id":"%d"},{"t":"feed","id":"%d"}]`, b.ID, c.ID, a.ID), get())

	// A plain rename deletes nothing.
	require.NoError(t, renameLabel(e.ctx, e.db, b.ID, "B2"))
	require.Contains(t, get(), fmt.Sprintf(`"id":"%d"`, b.ID))

	_, err = e.db.DeleteFolder(e.ctx, c.ID) // disable-tag's delete
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf(`[{"t":"folder","id":"%d"},{"t":"feed","id":"%d"}]`, b.ID, a.ID), get())

	// The default folder is never deleted, so its favorite stays.
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{SettingFavorites: []any{map[string]any{"t": "folder", "id": "1"}}}))
	_, err = e.db.DeleteFolder(e.ctx, 1)
	require.ErrorIs(t, err, ErrDefaultFolder)
	require.JSONEq(t, `[{"t":"folder","id":"1"}]`, get())
}

func TestMergedSettingsNormalizesFavorites(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, SettingFavorites,
		`[{"t":"feed","id":"007"},{"t":"feed","id":"7"},{"t":"feed","id":"0"},{"t":"tag","id":"3"}]`)
	m, err := e.db.MergedSettings(e.ctx)
	require.NoError(t, err)
	require.Equal(t, []any{map[string]any{"t": "feed", "id": "7"}}, m[SettingFavorites])
}

func TestHoldIgnoresItemsTheQueueNeverAccepted(t *testing.T) {
	e := newEnv(t)
	feed, a, b := ftFixture(t, e, true) // a follows the feed (full text); b is forced off
	require.NotZero(t, b)
	held := func() bool {
		got := false
		require.NoError(t, e.db.StreamItems(e.ctx, []int64{a}, true, 1, func(r *ContentRow) error { got = true; return nil }))
		return !got
	}
	_ = feed
	require.False(t, held(), "never queued: not held")
	e.db.MarkFulltextPending(a)
	require.True(t, held())
	e.db.ClearFulltextPending(a)
	require.False(t, held())
}

func TestFeedFulltextNowUsesCurrentState(t *testing.T) {
	e := newEnv(t)
	feed := e.addFeed("https://ex.com/feed")
	on, err := e.db.FeedFulltextNow(e.ctx, feed)
	require.NoError(t, err)
	require.False(t, on)
	e.exec("UPDATE feeds SET fulltext = 1 WHERE id = ?", feed)
	on, _ = e.db.FeedFulltextNow(e.ctx, feed)
	require.True(t, on)
	e.exec("UPDATE feeds SET fulltext = 0 WHERE id = ?", feed)
	e.setAll(true)
	on, _ = e.db.FeedFulltextNow(e.ctx, feed)
	require.True(t, on)
}
