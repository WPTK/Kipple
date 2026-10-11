package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/filter"
)

// A rule saved from an article ("Mute similar...") mutes that article in the same transaction when
// it matches it, with the same write as an apply, and leaves it alone otherwise.

func (e *env) itemID(title string) int64 {
	e.t.Helper()
	return int64(e.count("SELECT id FROM items WHERE title = ?", title))
}

func TestCreateFilterFromMutesTheMatchingSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedRetro(6)
	unread, read := e.itemID("spam 0"), e.itemID("spam 2")
	e.exec("UPDATE items SET read = 1, read_at = 777 WHERE id = ?", read)

	f, src, err := e.db.CreateFilterFrom(e.ctx, newFilter("mute", "spam"), unread)
	require.NoError(t, err)
	require.Equal(t, SourceMuted, src.Mute)
	require.Equal(t, []int64{unread}, src.Changed.Changed)
	require.EqualValues(t, 1, f.Hits, "the returned rule counts the hit")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND read = 1 AND muted_by = ? AND muted_was_read = 0", unread, f.ID))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"), "only the source article, not the rest of the feed")

	g, src, err := e.db.CreateFilterFrom(e.ctx, newFilter("mute", "spam 2"), read)
	require.NoError(t, err)
	require.Equal(t, SourceMuted, src.Mute)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND read_at = 777 AND muted_by = ? AND muted_was_read = 1", read, g.ID))
	e.assertMutedInvariant()

	// the existing restore path puts back exactly the read state each had
	for _, id := range []int64{f.ID, g.ID} {
		_, ok, err := e.db.DeleteFilter(e.ctx, id, UnmuteUnread, nil)
		require.NoError(t, err)
		require.True(t, ok)
	}
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND read = 0 AND muted_by IS NULL", unread))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND read = 1 AND read_at = 777 AND muted_by IS NULL", read))
}

func TestCreateFilterFromLeavesWhatItDoesNotMute(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedRetro(10)
	spam0, spam2, spam4, spam6 := e.itemID("spam 0"), e.itemID("spam 2"), e.itemID("spam 4"), e.itemID("spam 6")
	e.exec("UPDATE items SET starred = 1, starred_at = 1 WHERE id = ?", spam2)
	other := e.mkFilter(newFilter("mute", "spam 4"))
	_, err := e.db.ApplyFilter(e.ctx, other.ID, false, 0, nil, nil)
	require.NoError(t, err)
	e.mkFilter(newFilter("star", "spam 6"))
	unchanged := func() int {
		return e.count("SELECT count(*) FROM items WHERE muted_by IS NULL AND read = 0")
	}
	before := unchanged()

	cases := []struct {
		name  string
		rule  Filter
		id    int64
		want  SourceMute
		saved bool
	}{
		{"the edited rule no longer matches", newFilter("mute", "ham"), spam0, SourceNoMatch, true},
		{"starred: a star beats a mute", newFilter("mute", "spam 2"), spam2, SourceStarred, true},
		{"a star filter matches it", newFilter("mute", "spam 6"), spam6, SourceStarred, true},
		{"another rule muted it already", newFilter("mute", "spam 4", "other"), spam4, SourceAlreadyMuted, true},
		{"not a mute rule", newFilter("mark_read", "spam"), spam0, "", true},
	}
	for _, c := range cases {
		f, src, err := e.db.CreateFilterFrom(e.ctx, c.rule, c.id)
		require.NoError(t, err, c.name)
		require.Equal(t, c.want, src.Mute, c.name)
		require.Empty(t, src.Changed.Changed, c.name)
		require.NotZero(t, f.ID, c.name, "the rule is saved")
		require.Zero(t, f.Hits, c.name)
	}
	off := newFilter("mute", "spam")
	off.Enabled = false
	_, src, err := e.db.CreateFilterFrom(e.ctx, off, spam0)
	require.NoError(t, err)
	require.Empty(t, src.Mute, "a disabled rule mutes nothing")

	require.Equal(t, before, unchanged())
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND starred = 1 AND muted_by IS NULL", spam2))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND muted_by = ?", spam4, other.ID), "it stays with its rule")
	e.assertMutedInvariant()
}

func TestCreateFilterFromSourceInAnotherFeed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.seedRetro(2)
	b := e.addFeed("http://b.example/feed")
	e.fetchBody(b, frss(fspec{guid: "b1", title: "spam elsewhere", age: time.Minute}))
	elsewhere := e.itemID("spam elsewhere")

	// a rule scoped to feed a does not match an article of feed b
	scoped := newFilter("mute", "spam")
	scoped.Scope, scoped.FeedID = "feed", &a
	_, src, err := e.db.CreateFilterFrom(e.ctx, scoped, elsewhere)
	require.NoError(t, err)
	require.Equal(t, SourceNoMatch, src.Mute)

	// a global rule does
	_, src, err = e.db.CreateFilterFrom(e.ctx, newFilter("mute", "elsewhere"), elsewhere)
	require.NoError(t, err)
	require.Equal(t, SourceMuted, src.Mute)

	// an archived feed is skipped by every rule, so its article is not muted either
	e.fetchBody(b, frss(fspec{guid: "b2", title: "spam archived", age: time.Minute}))
	e.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'archive' WHERE id = ?", b)
	_, src, err = e.db.CreateFilterFrom(e.ctx, newFilter("mute", "archived"), e.itemID("spam archived"))
	require.NoError(t, err)
	require.Equal(t, SourceNoMatch, src.Mute)
}

func TestCreateFilterFromUnknownArticleStoresNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedRetro(2)
	for _, action := range []string{"mute", "mark_read"} {
		_, _, err := e.db.CreateFilterFrom(e.ctx, newFilter(action, "spam"), 99999)
		var fe *filter.Error
		require.ErrorAs(t, err, &fe)
		require.Equal(t, "source_item_id", fe.Field)
	}
	require.Zero(t, e.count("SELECT count(*) FROM filters"))
}
