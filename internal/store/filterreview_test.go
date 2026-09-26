package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// Regression tests for the Opus review of migration 0004, the filter engine, the ingest hook and
// the filters API (retention of muted items, resumable delete, the pre-mute read state, the apply
// guard, categories, the cache bump and the prediction).

// ---- retention: a fetch never trims its own muted items ----

func TestTrimKeepsThisCommitsMutedItems(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	e.mkFilter(newFilter("mute", "noise"))
	spec := func(i int, title string) fspec {
		return fspec{guid: fmt.Sprintf("g%d", i), title: title, age: time.Duration(100-i) * time.Minute}
	}
	var specs []fspec
	for i := 0; i < 50; i++ {
		specs = append(specs, spec(i, fmt.Sprintf("real %d", i)))
	}
	e.fetchBody(id, frss(specs...))
	require.Equal(t, 50, e.count("SELECT count(*) FROM items"))

	// the feed sits at its cap; the next fetch brings 2 muted and 1 real item
	specs = append(specs, spec(50, "noise 50"), spec(51, "noise 51"), spec(52, "real 52"))
	info := e.fetchBody(id, frss(specs...))
	require.EqualValues(t, 3, info.Trimmed)
	require.Equal(t, 2, info.Muted)
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"), "the new muted items survive their own commit")
	require.Equal(t, 48, e.count("SELECT count(*) FROM items WHERE muted_by IS NULL"))
	require.Equal(t, 3, e.count("SELECT count(*) FROM trimmed_items WHERE read = 0"), "the three oldest real items went")
	e.assertMutedInvariant()

	// keep fetching: muted items keep a bounded share (n/5) of the cap, real items keep the rest
	for round := 0; round < 10; round++ {
		var more []fspec
		for j := 0; j < 6; j++ {
			title := fmt.Sprintf("real r%d j%d", round, j)
			if j%2 == 0 {
				title = fmt.Sprintf("noise r%d j%d", round, j)
			}
			more = append(more, fspec{guid: fmt.Sprintf("r%dj%d", round, j), title: title,
				age: time.Duration(45*60-(round*6+j)) * time.Second})
		}
		e.fetchBody(id, frss(more...))
		require.LessOrEqual(t, e.count("SELECT count(*) FROM items"), 50)
	}
	muted := e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL")
	require.Positive(t, muted, "a busy feed still has muted items to show")
	require.LessOrEqual(t, muted, 50/5, "and they stay within their bounded share of the cap")
	require.Equal(t, 50, e.count("SELECT count(*) FROM items"))
}

func TestMutedAllowance(t *testing.T) {
	for _, tc := range []struct{ n, real, muted, want int }{
		{50, 50, 2, 2},   // a few muted items: all kept (the cap is n/5 = 10)
		{50, 50, 30, 10}, // plenty of both: muted are bounded to n/5
		{50, 40, 30, 10}, // real just fits below n - n/5
		{50, 20, 60, 30}, // real items leave room: muted may use it
		{50, 0, 60, 50},  // nothing real: muted may fill the cap
		{50, 80, 0, 0},   // nothing muted
		{250, 300, 100, 50},
	} {
		require.Equal(t, tc.want, mutedAllowance(tc.n, tc.real, tc.muted), "%+v", tc)
	}
}

// ---- resumable delete ----

func TestDeleteFilterResumesAfterCancel(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(2200)
	rule := e.mkFilter(newFilter("mute", "spam"))
	_, err := e.db.ApplyFilter(e.ctx, rule.ID, false, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1100, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID))

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	changed, ok, err := e.db.DeleteFilter(ctx, rule.ID, UnmuteRead, func(StateResult) { cancel() })
	require.Error(t, err)
	require.True(t, ok)
	require.EqualValues(t, 500, changed)
	require.Equal(t, 600, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID), "the rest is still muted")
	require.Equal(t, 1, e.count("SELECT count(*) FROM filters WHERE id = ? AND enabled = 0", rule.ID),
		"the rule is only disabled until every item is restored, so a retry finds it")

	// the retry finishes the job and deletes the row last
	changed, ok, err = e.db.DeleteFilter(e.ctx, rule.ID, UnmuteRead, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 600, changed)
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
	require.Zero(t, e.count("SELECT count(*) FROM filters"))
	e.assertMutedInvariant()
}

func TestDeleteFilterProcessesOrphansOfAMissingRow(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(40)
	rule := e.mkFilter(newFilter("mute", "spam"))
	_, err := e.db.ApplyFilter(e.ctx, rule.ID, false, 0, nil, nil)
	require.NoError(t, err)
	_, ok, err := e.db.DeleteFilter(e.ctx, rule.ID, UnmuteKeep, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 20, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID), "orphans")

	// the row is gone, but ?unmute=read still restores what carries its muted_by
	changed, ok, err := e.db.DeleteFilter(e.ctx, rule.ID, UnmuteRead, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 20, changed)
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))

	// nothing left: an unknown id is still not found
	_, ok, err = e.db.DeleteFilter(e.ctx, rule.ID, UnmuteRead, nil)
	require.NoError(t, err)
	require.False(t, ok)
}

// ---- unmute=unread only touches what was unread before the mute ----

func TestUnmuteUnreadKeepsPriorReadState(t *testing.T) {
	// retroactive apply with include_read
	e := newEnv(t)
	e.seedRetro(20)
	e.exec("UPDATE items SET read = 1, read_at = 777 WHERE title IN ('spam 0', 'spam 2')")
	rule := e.mkFilter(newFilter("mute", "spam"))
	_, err := e.db.ApplyFilter(e.ctx, rule.ID, true, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 10, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID))
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE muted_by = ? AND muted_was_read = 1", rule.ID))
	require.Equal(t, 8, e.count("SELECT count(*) FROM items WHERE muted_by = ? AND muted_was_read = 0", rule.ID))

	var unread int
	_, ok, err := e.db.DeleteFilter(e.ctx, rule.ID, UnmuteUnread, func(r StateResult) { unread += len(r.MadeUnread) })
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 8, unread, "only the items that were unread when muted are reported as unread")
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE title LIKE 'spam%' AND read = 1"))
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE title IN ('spam 0', 'spam 2') AND read = 1 AND read_at = 777"), "read_at kept")
	require.Equal(t, 8, e.count("SELECT count(*) FROM items WHERE title LIKE 'spam%' AND read = 0 AND read_at IS NULL"))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE muted_was_read IS NOT NULL"))
	e.assertMutedInvariant()

	// ingest from the initial-read window
	e = newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET initial_read_before = ? WHERE id = ?", base.Add(-150*time.Second).Unix(), id)
	rule = e.mkFilter(newFilter("mute", "spam"))
	e.fetchBody(id, frss(
		fspec{guid: "a", title: "spam old", age: 10 * time.Minute},
		fspec{guid: "b", title: "spam new", age: time.Minute},
	))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'spam old' AND muted_was_read = 1 AND muted_by = ?", rule.ID))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'spam new' AND muted_was_read = 0 AND muted_by = ?", rule.ID))
	_, ok, err = e.db.DeleteFilter(e.ctx, rule.ID, UnmuteUnread, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'spam old' AND read = 1"), "read before the mute: stays read")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'spam new' AND read = 0"))
}

// ---- an apply stops writing when its rule goes away or changes ----

func TestApplyStopsWhenRuleIsDeletedOrChanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(e *env, f Filter)
	}{
		{"delete", func(e *env, f Filter) {
			_, _, err := e.db.DeleteFilter(e.ctx, f.ID, UnmuteKeep, nil)
			require.NoError(e.t, err)
		}},
		{"disable", func(e *env, f Filter) {
			_, _, err := e.db.UpdateFilter(e.ctx, f.ID, func(x *Filter) error { x.Enabled = false; return nil })
			require.NoError(e.t, err)
		}},
		{"edit", func(e *env, f Filter) {
			_, _, err := e.db.UpdateFilter(e.ctx, f.ID, func(x *Filter) error { x.Terms = []string{"ham"}; return nil })
			require.NoError(e.t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedRetro(2200)
			rule := e.mkFilter(newFilter("mute", "spam"))
			batches := 0
			res, err := e.db.ApplyFilter(e.ctx, rule.ID, false, 0, nil, func(ApplyBatch) {
				batches++
				if batches == 1 {
					tc.act(e, rule)
				}
			})
			require.ErrorIs(t, err, ErrFilterChanged)
			require.Equal(t, 1, batches)
			require.Equal(t, 500, res.Changed)
			require.Equal(t, 500, e.count("SELECT count(*) FROM items WHERE muted_by = ?", rule.ID), "nothing written after the change")
		})
	}
}

// ---- category rules ignore items without stored categories ----

func TestInvertedCategoryRuleSkipsItemsWithoutCategories(t *testing.T) {
	e := newEnv(t)
	id := e.seedRetro(10) // no categories at all: like every pre-0004 item
	inv := newFilter("mute", "news")
	inv.Fields = []string{"category"}
	inv.Invert = true

	pre, err := e.db.PreviewFilter(e.ctx, inv, true, time.Second)
	require.NoError(t, err)
	require.Zero(t, pre.Matches, "no stored categories: nothing to say about them")
	saved := e.mkFilter(inv)
	res, err := e.db.ApplyFilter(e.ctx, saved.ID, true, 0, nil, nil)
	require.NoError(t, err)
	require.Zero(t, res.Changed)
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))

	// at ingest: an item with categories that lack the term is muted, one without categories is not
	e.fetchBody(id, frss(
		fspec{guid: "n1", title: "with news", cats: []string{"News"}, age: time.Minute},
		fspec{guid: "s1", title: "with sports", cats: []string{"Sports"}, age: 2 * time.Minute},
		fspec{guid: "x1", title: "no cats", age: 3 * time.Minute},
	))
	require.Equal(t, []string{"with sports"}, e.titles("muted_by IS NOT NULL"))
}

// ---- the cache generation is bumped inside the write transaction ----

func TestFilterGenerationBumpedBeforeCommit(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(4)
	var seen []uint64
	e.db.testFilterTxHook = func() { seen = append(seen, e.db.filterGen.Load()) }
	before := e.db.filterGen.Load()

	f := e.mkFilter(newFilter("mute", "spam"))
	require.Equal(t, []uint64{before + 1}, seen, "create bumps before its transaction ends")
	_, _, err := e.db.UpdateFilter(e.ctx, f.ID, func(x *Filter) error { x.Name = "n"; return nil })
	require.NoError(t, err)
	require.Equal(t, before+2, seen[len(seen)-1])
	_, _, err = e.db.DeleteFilter(e.ctx, f.ID, UnmuteRead, nil)
	require.NoError(t, err)
	require.Equal(t, before+4, seen[len(seen)-1], "delete bumps on disable and again on removal")
	require.Equal(t, before+4, e.db.filterGen.Load(), "and never after the transactions")

	// a cancelled write does not leave a bump the hook could not see either way
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	_, err = e.db.CreateFilter(ctx, newFilter("mute", "x"))
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled) || err != nil)
}

// ---- prediction and commit agree on the feed title ----

func TestMutedUIDsUsesDocumentTitleLikeTheCommit(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/rss") // no "feed" in the URL, the last fallback of the feed title
	r := newFilter("mute", "Feed")
	r.Fields = []string{"feed"}
	e.mkFilter(r)
	items := []fetch.Item{{UID: "a", Title: "hello"}}

	// the stored title is still empty (first fetch of a new subscription)
	got, err := e.db.MutedUIDs(e.ctx, id, "Feed", items)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"a": true}, got, "the document title stands in, as it does at commit")
	got, err = e.db.MutedUIDs(e.ctx, id, "", items)
	require.NoError(t, err)
	require.Empty(t, got)

	info := e.fetchBody(id, frss(fspec{guid: "a", title: "hello"}))
	require.Equal(t, 1, info.Muted, "and the commit muted it")
}
