package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/filter"
)

// seedRetro loads n items (oldest first) with no rules in place: even k are "spam k", odd k "ham k".
func (e *env) seedRetro(n int) int64 {
	e.t.Helper()
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, frss(fnumbered(n, func(i int) string {
		if i%2 == 0 {
			return fmt.Sprintf("spam %d", i)
		}
		return fmt.Sprintf("ham %d", i)
	})...))
	return id
}

func TestPreviewCountsSamplesAndWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(100)
	e.exec("UPDATE items SET read = 1 WHERE title = 'spam 0'")
	e.exec("UPDATE items SET starred = 1 WHERE title = 'spam 2'")
	before := e.count("SELECT count(*) FROM items WHERE read = 1") + 1000*e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL")

	f := newFilter("mute", "spam")
	res, err := e.db.PreviewFilter(e.ctx, f, false, 5*time.Second)
	require.NoError(t, err)
	require.False(t, res.Truncated)
	require.Equal(t, 98, res.Scanned, "unread, non-starred candidates only")
	require.Equal(t, 48, res.Matches)
	require.Len(t, res.SampleIDs, PreviewSample)
	for i := 1; i < len(res.SampleIDs); i++ {
		require.Greater(t, res.SampleIDs[i-1], res.SampleIDs[i], "newest first")
	}

	res, err = e.db.PreviewFilter(e.ctx, f, true, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 100, res.Scanned)
	require.Equal(t, 49, res.Matches, "the read one counts with include_read; the starred one never does")

	// an invalid unsaved rule is a validation error, not a scan
	_, err = e.db.PreviewFilter(e.ctx, newFilter("mute"), false, time.Second)
	var fe *filter.Error
	require.ErrorAs(t, err, &fe)

	// nothing was written
	require.Equal(t, before, e.count("SELECT count(*) FROM items WHERE read = 1")+1000*e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
	require.Zero(t, e.count("SELECT count(*) FROM filters"))
}

func TestPreviewBudgetTruncates(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(3000)
	res, err := e.db.PreviewFilter(e.ctx, newFilter("mute", "spam"), false, time.Nanosecond)
	require.NoError(t, err)
	require.True(t, res.Truncated)
	require.Less(t, res.Scanned, 3000)
}

func TestPreviewRespectsPrecedenceAndFields(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(20)
	e.mkFilter(newFilter("star", "spam")) // saved star rule: it will star every "spam" item
	res, err := e.db.PreviewFilter(e.ctx, newFilter("mute", "spam"), false, time.Second)
	require.NoError(t, err)
	require.Zero(t, res.Matches, "a star rule beats the mute: nothing would actually be muted")

	res, err = e.db.PreviewFilter(e.ctx, newFilter("mark_read", "ham"), false, time.Second)
	require.NoError(t, err)
	require.Equal(t, 10, res.Matches)

	// a content rule reads the body
	c := newFilter("mark_read", "body")
	c.Fields = []string{"content"}
	res, err = e.db.PreviewFilter(e.ctx, c, false, time.Second)
	require.NoError(t, err)
	require.Equal(t, 20, res.Matches)
}

func TestApplyMuteBatchesAndRetroSemantics(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(1300)
	e.exec("UPDATE items SET read = 1 WHERE title IN ('spam 0', 'spam 2')")
	e.exec("UPDATE items SET starred = 1 WHERE title = 'spam 4'")
	rule := e.mkFilter(newFilter("mute", "spam"))

	var batches []int
	var progress []ApplyProgress
	res, err := e.db.ApplyFilter(e.ctx, rule.ID, false, 1297, func(p ApplyProgress) { progress = append(progress, p) },
		func(b ApplyBatch) {
			require.Equal(t, filter.ActionMute, b.Action)
			batches = append(batches, len(b.Res.Changed))
		})
	require.NoError(t, err)
	// 650 spam items minus the 2 read and the 1 starred one (default scope: unread, non-starred)
	require.Equal(t, 647, res.Changed)
	require.Equal(t, 1297, res.Scanned)
	require.Equal(t, []int{500, 147}, batches, "writes go in batches of at most 500 ids")
	require.NotEmpty(t, progress)
	require.Equal(t, res.Scanned, progress[len(progress)-1].Done)
	require.Equal(t, 647, progress[len(progress)-1].Changed)
	e.assertMutedInvariant()
	require.Equal(t, 647, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE title LIKE 'spam%' AND read = 0 AND starred = 0"))
	require.Equal(t, 647, e.count("SELECT hits FROM filters WHERE id = ?", rule.ID))
	require.Equal(t, 1, e.count("SELECT starred FROM items WHERE title = 'spam 4'"), "starred items are never muted")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'spam 4' AND read = 0 AND muted_by IS NULL"))

	// include_read reaches the two read items that were left alone; a re-apply then changes nothing
	res, err = e.db.ApplyFilter(e.ctx, rule.ID, true, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 2, res.Changed)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'spam 0' AND read = 1 AND muted_by IS NOT NULL"))
	e.assertMutedInvariant()
	res, err = e.db.ApplyFilter(e.ctx, rule.ID, true, 0, nil, nil)
	require.NoError(t, err)
	require.Zero(t, res.Changed)
}

func TestApplyStarAndMarkRead(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(20)
	mute := e.mkFilter(newFilter("mute", "spam"))
	_, err := e.db.ApplyFilter(e.ctx, mute.ID, false, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 10, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))

	// a star rule applied with include_read stars a matching muted item and un-mutes it
	star := e.mkFilter(newFilter("star", "spam 4"))
	res, err := e.db.ApplyFilter(e.ctx, star.ID, true, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Changed)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'spam 4' AND starred = 1 AND read = 1 AND muted_by IS NULL"))
	e.assertMutedInvariant()

	mr := e.mkFilter(newFilter("mark_read", "ham"))
	res, err = e.db.ApplyFilter(e.ctx, mr.ID, false, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 10, res.Changed)
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE title LIKE 'ham%' AND read = 0"))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE title LIKE 'ham%' AND muted_by IS NOT NULL"), "mark_read does not mute")
}

func TestApplyRefusesWhatCannotApply(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(4)
	var fe *filter.Error
	hl := e.mkFilter(newFilter("highlight", "spam"))
	_, _, err := e.db.RetroTotal(e.ctx, hl.ID, false)
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "action", fe.Field)

	off := newFilter("mute", "spam")
	off.Enabled = false
	off = e.mkFilter(off)
	_, _, err = e.db.RetroTotal(e.ctx, off.ID, false)
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "enabled", fe.Field)

	_, _, err = e.db.RetroTotal(e.ctx, 999, false)
	require.ErrorIs(t, err, ErrFilterNotFound)
	_, err = e.db.ApplyFilter(e.ctx, 999, false, 0, nil, nil)
	require.ErrorIs(t, err, ErrFilterNotFound)
}

// seedMixed loads four unread items, all titled "spam N": 0 has "keepme" in its body, 1 has the
// category "vip", 2 has "keepme" about 20 KiB into its body (past the 8 KiB a regex reads, inside the
// 32 KiB a text rule reads), 3 has none of these.
func (e *env) seedMixed() int64 {
	e.t.Helper()
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	filler := strings.Repeat("lorem ipsum ", 20<<10/12)
	e.fetchBody(id, frss(
		fspec{guid: "g0", title: "spam 0", body: "<p>please keepme</p>", age: 4 * time.Minute},
		fspec{guid: "g1", title: "spam 1", cats: []string{"vip"}, age: 3 * time.Minute},
		fspec{guid: "g2", title: "spam 2", body: "<p>" + filler + "keepme</p>", age: 2 * time.Minute},
		fspec{guid: "g3", title: "spam 3", age: time.Minute},
	))
	return id
}

// A retroactive run loads every field the rules it evaluates read, not only the applied rule's:
// a saved star rule on content, on categories, or inverted, must see the same item as at ingest.
func TestRetroLoadsTheFieldsOfTheStarRules(t *testing.T) {
	muteSpam := func(e *env) Filter { return newFilter("mute", "spam") } // title only
	star := func(e *env, mut func(*Filter)) {
		f := newFilter("star", "keepme")
		mut(&f)
		e.mkFilter(f)
	}
	muted := func(e *env) []string { return e.titles("muted_by IS NOT NULL") }
	for _, tc := range []struct {
		name      string
		saved     func(*env)
		wantMuted []string
	}{
		{"star on content", func(e *env) { star(e, func(f *Filter) { f.Fields = []string{"content"} }) }, []string{"spam 1", "spam 3"}},
		{"inverted star on content", func(e *env) {
			star(e, func(f *Filter) { f.Fields, f.Invert = []string{"content"}, true })
		}, []string{"spam 0", "spam 2"}},
		{"star on category", func(e *env) {
			star(e, func(f *Filter) { f.Terms, f.Fields = []string{"vip"}, []string{"category"} })
		}, []string{"spam 0", "spam 2", "spam 3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedMixed()
			tc.saved(e)
			pre, err := e.db.PreviewFilter(e.ctx, muteSpam(e), false, 5*time.Second)
			require.NoError(t, err)
			require.Equal(t, len(tc.wantMuted), pre.Matches, "the preview counts what the apply changes")
			rule := e.mkFilter(muteSpam(e))
			res, err := e.db.ApplyFilter(e.ctx, rule.ID, false, 0, nil, nil)
			require.NoError(t, err)
			require.Equal(t, len(tc.wantMuted), res.Changed)
			require.Equal(t, tc.wantMuted, muted(e))
			e.assertMutedInvariant()
		})
	}

	// A regex rule reads 8 KiB of content, but a text star rule evaluated beside it reads 32 KiB: the
	// scan loads the larger, so the star rule still sees "keepme" 20 KiB in.
	e := newEnv(t)
	e.seedMixed()
	star(e, func(f *Filter) { f.Fields = []string{"content"} })
	re := newFilter("mute", `^spam \d`)
	re.Kind, re.Fields = "regex", []string{"title", "content"}
	pre, err := e.db.PreviewFilter(e.ctx, re, false, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 2, pre.Matches)
	rule := e.mkFilter(re)
	_, err = e.db.ApplyFilter(e.ctx, rule.ID, false, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"spam 1", "spam 3"}, muted(e))
}

// The `feed` field is the same title at ingest and in a retroactive run: custom title, else stored
// title, else (both blank) the feed URL.
func TestFeedFieldAgreesBetweenIngestAndRetro(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://untitled.example/feed")
	rule := newFilter("mute", "untitled.example")
	rule.Fields, rule.WholeWord = []string{"feed"}, false
	e.mkFilter(rule)
	// A document with no title: nothing stored, nothing from the document, so the URL is the name.
	e.fetchBody(id, []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>  </title><link>https://ex.com/</link>`+
		`<item><guid>u1</guid><title>one</title><link>https://ex.com/u1</link></item></channel></rss>`))
	require.Equal(t, []string{"one"}, e.titles("muted_by IS NOT NULL"), "ingest matches the URL of an untitled feed")

	// The retroactive preview sees the same name.
	e.exec("UPDATE items SET muted_by = NULL, read = 0, muted_was_read = NULL")
	res, err := e.db.PreviewFilter(e.ctx, rule, false, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, res.Matches)

	// Whitespace-only custom and stored titles count as blank on both paths too.
	e.exec("UPDATE feeds SET custom_title = ' ', title = char(9) WHERE id = ?", id)
	res, err = e.db.PreviewFilter(e.ctx, rule, false, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, res.Matches)
}

func TestDeleteFilterUnmuteModes(t *testing.T) {
	for _, tc := range []struct {
		mode                    string
		wantRead, wantMutedLeft int
	}{
		{UnmuteRead, 1100, 0},
		{UnmuteUnread, 0, 0},
		{UnmuteKeep, 1100, 1100},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			e := newEnv(t)
			e.seedRetro(2200)
			rule := e.mkFilter(newFilter("mute", "spam"))
			other := e.mkFilter(newFilter("mute", "ham 1"))
			_, err := e.db.ApplyFilter(e.ctx, rule.ID, false, 0, nil, nil)
			require.NoError(t, err)
			_, err = e.db.ApplyFilter(e.ctx, other.ID, false, 0, nil, nil)
			require.NoError(t, err)
			require.Equal(t, 1100, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID))

			var batches []int
			changed, ok, err := e.db.DeleteFilter(e.ctx, rule.ID, tc.mode, func(r StateResult) { batches = append(batches, len(r.Changed)) })
			require.NoError(t, err)
			require.True(t, ok)
			require.Zero(t, e.count("SELECT count(*) FROM filters WHERE id = ?", rule.ID))
			require.Equal(t, tc.wantMutedLeft, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID))
			if tc.mode == UnmuteKeep {
				require.Zero(t, changed)
				require.Empty(t, batches)
			} else {
				require.EqualValues(t, 1100, changed)
				require.Equal(t, []int{500, 500, 100}, batches)
			}
			require.Equal(t, tc.wantRead, e.count("SELECT count(*) FROM items WHERE title LIKE 'spam%' AND read = 1"))
			require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE muted_by = ? AND title = 'ham 1'", other.ID), "other rules' mutes are untouched")
			e.assertMutedInvariant()
		})
	}
	e := newEnv(t)
	_, ok, err := e.db.DeleteFilter(e.ctx, 5, UnmuteRead, nil)
	require.NoError(t, err)
	require.False(t, ok)
	_, _, err = e.db.DeleteFilter(e.ctx, 5, "bogus", nil)
	require.Error(t, err)
}
