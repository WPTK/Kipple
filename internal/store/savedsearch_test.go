package store

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSavedSearchNormalize(t *testing.T) {
	ok := func(s SavedSearch) SavedSearch {
		t.Helper()
		n, err := s.Normalize()
		require.NoError(t, err)
		return n
	}
	n := ok(SavedSearch{Name: "  Go  ", Q: " title:go  ", Scope: &SavedSearchScope{FeedID: "007"}, Order: "rank"})
	require.Equal(t, "Go", n.Name)
	require.Equal(t, "title:go", n.Q)
	require.Equal(t, "7", n.Scope.FeedID)
	ok(SavedSearch{Name: "n", Q: "q", Scope: &SavedSearchScope{View: "starred"}})
	ok(SavedSearch{Name: "n", Q: "q"})
	ok(SavedSearch{Name: "Café x", Q: "title:\"a b\" é ☃"}) // spaces, accents and symbols are fine

	long := strings.Repeat("x", MaxSavedSearchName+1)
	for _, tc := range []struct {
		name  string
		s     SavedSearch
		field string
	}{
		{"empty name", SavedSearch{Name: " ", Q: "q"}, "name"},
		{"long name", SavedSearch{Name: long, Q: "q"}, "name"},
		{"newline name", SavedSearch{Name: "a\nb", Q: "q"}, "name"},
		{"NEL name", SavedSearch{Name: "a\u0085b", Q: "q"}, "name"},
		{"C1 control name", SavedSearch{Name: "a\u009fb", Q: "q"}, "name"},
		{"line separator name", SavedSearch{Name: "a b", Q: "q"}, "name"},
		{"paragraph separator name", SavedSearch{Name: "a b", Q: "q"}, "name"},
		{"tab name", SavedSearch{Name: "a\tb", Q: "q"}, "name"},
		{"NEL q", SavedSearch{Name: "n", Q: "a\u0085b"}, "q"},
		{"line separator q", SavedSearch{Name: "n", Q: "a b"}, "q"},
		{"paragraph separator q", SavedSearch{Name: "n", Q: "a b"}, "q"},
		{"DEL q", SavedSearch{Name: "n", Q: "a\x7fb"}, "q"},
		{"empty q", SavedSearch{Name: "n", Q: "  "}, "q"},
		{"long q", SavedSearch{Name: "n", Q: strings.Repeat("q", MaxSavedSearchQ+1)}, "q"},
		{"order", SavedSearch{Name: "n", Q: "q", Order: "newest"}, "order"},
		{"feed id", SavedSearch{Name: "n", Q: "q", Scope: &SavedSearchScope{FeedID: "abc"}}, "scope.feed_id"},
		{"folder id zero", SavedSearch{Name: "n", Q: "q", Scope: &SavedSearchScope{FolderID: "0"}}, "scope.folder_id"},
		{"view", SavedSearch{Name: "n", Q: "q", Scope: &SavedSearchScope{View: "muted"}}, "scope.view"},
		{"two scopes", SavedSearch{Name: "n", Q: "q", Scope: &SavedSearchScope{FeedID: "1", View: "all"}}, "scope"},
		{"empty scope", SavedSearch{Name: "n", Q: "q", Scope: &SavedSearchScope{}}, "scope"},
	} {
		_, err := tc.s.Normalize()
		var ve *SavedSearchError
		require.ErrorAs(t, err, &ve, tc.name)
		require.Equal(t, tc.field, ve.Field, tc.name)
	}
}

func TestNormalizeSavedSearchesList(t *testing.T) {
	good := SavedSearch{ID: "a1", Name: "n", Q: "q"}
	_, err := NormalizeSavedSearches([]SavedSearch{good, {ID: "a1", Name: "m", Q: "q"}})
	require.Error(t, err, "duplicate id")
	_, err = NormalizeSavedSearches([]SavedSearch{{ID: "bad id", Name: "n", Q: "q"}})
	require.Error(t, err)
	_, err = NormalizeSavedSearches([]SavedSearch{{Name: "n", Q: "q"}})
	require.Error(t, err, "an id is required")
	many := make([]SavedSearch, MaxSavedSearches+1)
	_, err = NormalizeSavedSearches(many)
	require.Error(t, err)
	out, err := NormalizeSavedSearches(nil)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestEditSavedSearchesLosesNoUpdate(t *testing.T) {
	db, _ := openTest(t)
	ctx := context.Background()
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss := SavedSearch{ID: NewSavedSearchID(), Name: "s", Q: "q" + strings.Repeat("x", i)}
			_, err := db.EditSavedSearches(ctx, func(l []SavedSearch) ([]SavedSearch, error) { return append(l, ss), nil })
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	list, err := db.SavedSearches(ctx)
	require.NoError(t, err)
	require.Len(t, list, n)

	// A rejected edit writes nothing.
	_, err = db.EditSavedSearches(ctx, func(l []SavedSearch) ([]SavedSearch, error) {
		return append(l, SavedSearch{ID: "zz", Name: "", Q: "q"}), nil
	})
	require.Error(t, err)
	list, err = db.SavedSearches(ctx)
	require.NoError(t, err)
	require.Len(t, list, n)
}

func TestDeletingAFeedOrFolderDropsOnlyTheScope(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	other := e.addFeed("https://b/f")
	fo, err := e.db.CreateFolder(ctx, "News", 5)
	require.NoError(t, err)
	fid := fo.ID
	list := []SavedSearch{
		{ID: "a", Name: "in feed", Q: "q", Scope: &SavedSearchScope{FeedID: itoa(e.feed)}},
		{ID: "b", Name: "in other", Q: "q", Scope: &SavedSearchScope{FeedID: itoa(other)}},
		{ID: "c", Name: "in folder", Q: "q", Scope: &SavedSearchScope{FolderID: itoa(fid)}},
		{ID: "d", Name: "starred", Q: "q", Scope: &SavedSearchScope{View: "starred"}},
		{ID: "e", Name: "same number, other kind", Q: "q", Scope: &SavedSearchScope{FolderID: itoa(e.feed)}},
	}
	_, err = e.db.EditSavedSearches(ctx, func([]SavedSearch) ([]SavedSearch, error) { return list, nil })
	require.NoError(t, err)

	require.NoError(t, e.db.DeleteFeed(ctx, e.feed, false))
	got, err := e.db.SavedSearches(ctx)
	require.NoError(t, err)
	require.Len(t, got, 5, "no search is deleted")
	require.Nil(t, got[0].Scope, "the feed's scope is dropped")
	require.Equal(t, itoa(other), got[1].Scope.FeedID)
	require.Equal(t, itoa(fid), got[2].Scope.FolderID)
	require.Equal(t, itoa(e.feed), got[4].Scope.FolderID, "a folder with the deleted feed's number is a different thing")

	_, err = e.db.DeleteFolder(ctx, fid)
	require.NoError(t, err)
	got, _ = e.db.SavedSearches(ctx)
	require.Nil(t, got[2].Scope)
	require.Equal(t, "in folder", got[2].Name)
	require.Equal(t, "starred", got[3].Scope.View)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A scope is checked in the transaction that stores it: one naming a feed or folder that is gone
// (deleted after any earlier check, dropSavedSearchScope having already run) is refused, and
// nothing is stored.
func TestEditSavedSearchesRefusesAScopeForAGoneFeedOrFolder(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	other := e.addFeed("https://b/f")
	fo, err := e.db.CreateFolder(ctx, "News", 5)
	require.NoError(t, err)
	add := func(sc *SavedSearchScope) error {
		_, err := e.db.EditSavedSearches(ctx, func(l []SavedSearch) ([]SavedSearch, error) {
			return append(l, SavedSearch{ID: NewSavedSearchID(), Name: "n", Q: "q", Scope: sc}), nil
		})
		return err
	}
	require.NoError(t, add(&SavedSearchScope{FeedID: itoa(other)}))
	require.NoError(t, add(&SavedSearchScope{FolderID: itoa(fo.ID)}))
	require.NoError(t, add(&SavedSearchScope{View: "unread"}))
	require.NoError(t, add(nil))

	// The feed and the folder are deleted (their own transactions drop existing scopes) ...
	require.NoError(t, e.db.DeleteFeed(ctx, other, false))
	_, err = e.db.DeleteFolder(ctx, fo.ID)
	require.NoError(t, err)
	// ... and a create that had checked before that now commits inside the transaction: refused.
	for _, sc := range []*SavedSearchScope{{FeedID: itoa(other)}, {FolderID: itoa(fo.ID)}} {
		var ve *SavedSearchError
		require.ErrorAs(t, add(sc), &ve)
		require.Equal(t, "scope", ve.Field)
	}
	got, err := e.db.SavedSearches(ctx)
	require.NoError(t, err)
	require.Len(t, got, 4, "nothing was stored")
	for _, g := range got {
		require.True(t, g.Scope == nil || g.Scope.View != "", "no dead scope: %+v", g.Scope)
	}

	// A patch that moves a scope onto a gone feed is refused; one that leaves the scope alone is not
	// checked, so a stale dangling scope (raw delete) never blocks an unrelated edit.
	_, err = e.db.EditSavedSearches(ctx, func(l []SavedSearch) ([]SavedSearch, error) {
		l[3].Scope = &SavedSearchScope{FeedID: itoa(other)}
		return l, nil
	})
	require.Error(t, err)
	live := e.addFeed("https://c/f")
	require.NoError(t, add(&SavedSearchScope{FeedID: itoa(live)}))
	e.exec("DELETE FROM feeds WHERE id = ?", live) // no drop: the scope dangles
	_, err = e.db.EditSavedSearches(ctx, func(l []SavedSearch) ([]SavedSearch, error) {
		l[0].Name = "renamed"
		return l, nil
	})
	require.NoError(t, err)
}

// PATCH /api/settings goes through SetSettings, which checks new scopes the same way.
func TestSetSettingsRefusesAScopeForAGoneFeed(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	list := []any{map[string]any{"id": "a", "name": "n", "q": "q", "scope": map[string]any{"feed_id": "99999"}}}
	err := e.db.SetSettings(ctx, map[string]any{SettingSavedSearches: list})
	var ve *SavedSearchError
	require.ErrorAs(t, err, &ve)
	got, err := e.db.SavedSearches(ctx)
	require.NoError(t, err)
	require.Empty(t, got)
	list[0].(map[string]any)["scope"] = map[string]any{"feed_id": itoa(e.feed)}
	require.NoError(t, e.db.SetSettings(ctx, map[string]any{SettingSavedSearches: list}))
}
