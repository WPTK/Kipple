package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/filter"
)

type fspec struct {
	guid, title, body, author string
	cats                      []string
	age                       time.Duration
}

// frss is rss() with authors and categories.
func frss(specs ...fspec) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title><link>https://ex.com/</link>`)
	for i, s := range specs {
		if s.age == 0 {
			s.age = time.Duration(len(specs)-i) * time.Minute // document order is publication order
		}
		if s.title == "" {
			s.title = "title " + s.guid
		}
		if s.body == "" {
			s.body = "<p>body " + s.guid + "</p>"
		}
		fmt.Fprintf(&b, `<item><guid>%s</guid><title>%s</title><link>https://ex.com/%s</link><description><![CDATA[%s]]></description><pubDate>%s</pubDate>`,
			s.guid, s.title, s.guid, s.body, base.Add(-s.age).Format(time.RFC1123Z))
		if s.author != "" {
			fmt.Fprintf(&b, `<author>a@ex.com (%s)</author>`, s.author)
		}
		for _, c := range s.cats {
			fmt.Fprintf(&b, `<category>%s</category>`, c)
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

func fnumbered(n int, title func(i int) string) []fspec {
	out := make([]fspec, n)
	for i := range out {
		out[i] = fspec{guid: fmt.Sprintf("g%d", i), title: title(i), age: time.Duration(n-i) * time.Minute}
	}
	return out
}

// newFilter is a stored-filter value with the API defaults.
func newFilter(action string, terms ...string) Filter {
	return Filter{Enabled: true, Scope: "global", Kind: "text", Terms: terms, Fields: []string{"title"},
		WholeWord: true, FoldDiacritics: true, Action: action}
}

func (e *env) mkFilter(f Filter) Filter {
	e.t.Helper()
	out, err := e.db.CreateFilter(e.ctx, f)
	require.NoError(e.t, err)
	return out
}

// assertMutedInvariant is the design 1.4 invariant: a muted item is never unread.
func (e *env) assertMutedInvariant() {
	e.t.Helper()
	require.Zero(e.t, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL AND (read = 0 OR starred = 1)"),
		"muted_by implies read = 1 and starred = 0")
}

func (e *env) titles(where string, args ...any) []string {
	e.t.Helper()
	rows, err := e.db.Reader().Query("SELECT title FROM items WHERE "+where+" ORDER BY id", args...)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(e.t, rows.Scan(&s))
		out = append(out, s)
	}
	return out
}

func TestIngestWithoutFiltersChangesNothing(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	info := e.fetchBody(id, frss(fspec{guid: "a", cats: []string{"Go", "News"}}, fspec{guid: "b"}))
	require.Equal(t, 2, info.New)
	require.Empty(t, info.MutedIDs)
	require.Zero(t, info.Muted+info.MarkedRead+info.Starred)
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE read = 0 AND starred = 0 AND muted_by IS NULL"))
	require.Zero(t, e.count("SELECT count(*) FROM fetch_log WHERE note LIKE '%filters:%'"))
	// categories are stored for every new item, filters or not
	require.Equal(t, `["Go","News"]`, scalar[string](t, e.db.Reader(), "SELECT categories_json FROM item_content c JOIN items i ON i.id = c.item_id WHERE i.title = 'title a'"))
	require.Zero(t, e.count("SELECT count(*) FROM item_content c JOIN items i ON i.id = c.item_id WHERE i.title = 'title b' AND c.categories_json IS NOT NULL"))
}

func TestIngestMuteMarkReadStar(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	mute := e.mkFilter(newFilter("mute", "sponsored"))
	both := e.mkFilter(newFilter("mute", "sponsored", "ad")) // a second mute rule: muted_by stays the lowest id
	markRead := e.mkFilter(newFilter("mark_read", "weekly"))
	star := e.mkFilter(newFilter("star", "urgent"))
	hl := e.mkFilter(newFilter("highlight", "release"))
	_ = hl

	info := e.fetchBody(id, frss(
		fspec{guid: "p", title: "Plain post"},
		fspec{guid: "m", title: "Sponsored: buy now"},
		fspec{guid: "w", title: "Weekly roundup"},
		fspec{guid: "u", title: "Urgent release notes"},
		fspec{guid: "su", title: "Urgent sponsored thing"}, // star beats mute
		fspec{guid: "mw", title: "Weekly sponsored"},       // muted and mark_read: counted as muted
	))
	require.Equal(t, 6, info.New)
	require.Equal(t, 2, info.Muted, "the starred sponsored item is not muted")
	require.Equal(t, 1, info.MarkedRead, "only the plain mark_read item; the muted one counts as muted")
	require.Equal(t, 2, info.Starred)

	e.assertMutedInvariant()
	require.Equal(t, []string{"Sponsored: buy now", "Weekly sponsored"}, e.titles("muted_by = ?", mute.ID))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE muted_by = ?", both.ID), "muted_by is the lowest matching rule id")
	require.Equal(t, []string{"Weekly roundup"}, e.titles("read = 1 AND muted_by IS NULL AND starred = 0"))
	require.Equal(t, []string{"Urgent release notes", "Urgent sponsored thing"}, e.titles("starred = 1"))
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE starred = 1 AND read = 0"), "a star does not read the item")
	require.Equal(t, []string{"Plain post"}, e.titles("read = 0 AND starred = 0"))
	require.NotZero(t, e.count("SELECT count(*) FROM items WHERE read = 1 AND read_at = ?", e.clk.Now().Unix()))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE read = 0 AND read_at IS NOT NULL"))
	require.Len(t, info.MutedIDs, 2)

	// hits: a match counts only when its action took effect
	hits := func(f Filter) int { return e.count("SELECT hits FROM filters WHERE id = ?", f.ID) }
	require.Equal(t, 2, hits(mute), "the star-cancelled match is not a hit")
	require.Equal(t, 2, hits(both), "every mute rule that matched a muted item counts")
	require.Equal(t, 2, hits(markRead))
	require.Equal(t, 2, hits(star))
	require.Zero(t, hits(hl), "a highlight has no stored effect")
	require.Equal(t, 1, e.count("SELECT count(*) FROM filters WHERE id = ? AND last_hit_at = ?", mute.ID, e.clk.Now().Unix()))

	require.Equal(t, "filters: muted 2, marked_read 1, starred 2",
		scalar[string](t, e.db.Reader(), "SELECT note FROM fetch_log ORDER BY id DESC LIMIT 1"))
	require.Zero(t, e.count("SELECT keep FROM fetch_log ORDER BY id DESC LIMIT 1"))
}

func TestIngestFilterScopesAndFields(t *testing.T) {
	e := newEnv(t)
	a := e.addFeed("http://a.example/feed")
	b := e.addFeed("http://b.example/feed")
	e.exec("UPDATE feeds SET folder_id = 1 WHERE id IN (?, ?)", a, b)
	e.exec("INSERT INTO folders (id, name, position) VALUES (9, 'Other', 3)")
	e.exec("UPDATE feeds SET folder_id = 9 WHERE id = ?", b)

	feedRule := newFilter("mute", "alpha")
	feedRule.Scope, feedRule.FeedID = "feed", &a
	e.mkFilter(feedRule)
	folderRule := newFilter("mute", "beta")
	nine := int64(9)
	folderRule.Scope, folderRule.FolderID = "folder", &nine
	e.mkFilter(folderRule)
	authorRule := newFilter("mute", "jane doe")
	authorRule.Fields = []string{"author"}
	e.mkFilter(authorRule)
	catRule := newFilter("mute", "gossip")
	catRule.Fields = []string{"category"}
	e.mkFilter(catRule)

	body := frss(
		fspec{guid: "1", title: "alpha news"}, fspec{guid: "2", title: "beta news"},
		fspec{guid: "3", title: "gamma", author: "Jane Doe"},
		fspec{guid: "4", title: "delta", cats: []string{"Celebrity Gossip", "gossip"}},
		fspec{guid: "5", title: "epsilon", cats: []string{"gossiping"}}, // whole word: no match
	)
	ia := e.fetchBody(a, body)
	ib := e.fetchBody(b, body)
	// feed a: alpha (feed rule), no beta (folder 9 rule is for feed b's folder), jane, gossip
	require.Equal(t, 3, ia.Muted)
	// feed b: no alpha (feed rule is feed a's), beta, jane, gossip
	require.Equal(t, 3, ib.Muted)
	require.Equal(t, []string{"alpha news", "gamma", "delta"}, e.titles("feed_id = ? AND muted_by IS NOT NULL", a))
	require.Equal(t, []string{"beta news", "gamma", "delta"}, e.titles("feed_id = ? AND muted_by IS NOT NULL", b))
	e.assertMutedInvariant()
}

func TestIngestInvertedRuleIsOnlyShowMatching(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	f := newFilter("mute", "golang")
	f.Invert = true
	f.Scope, f.FeedID = "feed", &id
	e.mkFilter(f)
	info := e.fetchBody(id, frss(fspec{guid: "1", title: "Golang 1.30"}, fspec{guid: "2", title: "Cooking"}, fspec{guid: "3", title: ""}))
	// guid 3 has no title; the parser gives it "title 3" from frss, so it is muted too
	require.Equal(t, 2, info.Muted)
	require.Equal(t, []string{"Golang 1.30"}, e.titles("muted_by IS NULL"))
	e.assertMutedInvariant()
}

func TestFilterCacheFollowsWrites(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	fs := func(guid string) []byte { return frss(fspec{guid: guid, title: "spam " + guid}) }

	require.Zero(t, e.fetchBody(id, fs("a")).Muted, "no rules yet: the empty set is cached")
	f := e.mkFilter(newFilter("mute", "spam"))
	require.Equal(t, 1, e.fetchBody(id, fs("b")).Muted, "a create invalidates the cached set")

	f2, ok, err := e.db.UpdateFilter(e.ctx, f.ID, func(x *Filter) error { x.Enabled = false; return nil })
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, f2.Enabled)
	require.Zero(t, e.fetchBody(id, fs("c")).Muted, "a disabled rule is not evaluated")

	_, _, err = e.db.UpdateFilter(e.ctx, f.ID, func(x *Filter) error { x.Enabled = true; x.Terms = []string{"spam"}; return nil })
	require.NoError(t, err)
	require.Equal(t, 1, e.fetchBody(id, fs("d")).Muted)

	changed, ok, err := e.db.DeleteFilter(e.ctx, f.ID, UnmuteKeep, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, changed)
	require.Zero(t, e.fetchBody(id, fs("e")).Muted, "a delete invalidates the cached set")

	// a feed delete cascades its filter and the cache follows
	other := e.addFeed("http://b.example/feed")
	rule := newFilter("mute", "spam")
	rule.Scope, rule.FeedID = "feed", &other
	e.mkFilter(rule)
	require.Equal(t, 1, e.fetchBody(other, fs("f")).Muted)
	require.NoError(t, e.db.DeleteFeed(e.ctx, other, true))
	require.Zero(t, e.count("SELECT count(*) FROM filters"))
	third := e.addFeed("http://c.example/feed")
	require.Zero(t, e.fetchBody(third, fs("g")).Muted)
}

func TestUnmutePaths(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.mkFilter(newFilter("mute", "spam"))
	e.fetchBody(id, frss(fspec{guid: "1", title: "spam 1"}, fspec{guid: "2", title: "spam 2"}, fspec{guid: "3", title: "spam 3"}))
	ids := func() []int64 {
		rows, err := e.db.Reader().Query("SELECT id FROM items ORDER BY id")
		require.NoError(t, err)
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var x int64
			require.NoError(t, rows.Scan(&x))
			out = append(out, x)
		}
		return out
	}()
	require.Len(t, ids, 3)
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL AND read = 1"))

	// marking unread is the un-mute
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := SetRead(ctx, tx, ids[:1], false, 100)
		require.Equal(t, []int64{ids[0]}, res.Changed)
		return err
	}))
	require.Equal(t, 0, e.count("SELECT read FROM items WHERE id = ?", ids[0]))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE id = ? AND muted_by IS NOT NULL", ids[0]))

	// starring is the un-mute too, and the item stays read
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := SetStarred(ctx, tx, ids[1:2], true, 100)
		require.Equal(t, []int64{ids[1]}, res.Changed)
		return err
	}))
	require.Equal(t, 1, e.count("SELECT read FROM items WHERE id = ?", ids[1]))
	require.Equal(t, 1, e.count("SELECT starred FROM items WHERE id = ?", ids[1]))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE id = ? AND muted_by IS NOT NULL", ids[1]))
	e.assertMutedInvariant()

	// marking read again does not re-mute
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := SetRead(ctx, tx, ids[:1], true, 100)
		return err
	}))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE id = ? AND muted_by IS NOT NULL", ids[0]))
}

func TestTrimTakesMutedFirst(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	// oldest to newest: 40 real, then 30 muted (the muted ones are NEWER)
	e.mkFilter(newFilter("mute", "noise"))
	specs := fnumbered(70, func(i int) string {
		if i >= 40 {
			return fmt.Sprintf("noise %d", i)
		}
		return fmt.Sprintf("real %d", i)
	})
	e.fetchBody(id, frss(specs...))
	require.Equal(t, 30, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))

	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	n, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 20, n)
	// N counts real items before noise: all 40 real items stay, plus the 10 newest muted ones
	require.Equal(t, 40, e.count("SELECT count(*) FROM items WHERE muted_by IS NULL"))
	require.Equal(t, 10, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL AND title >= 'noise 60'"))
	require.Equal(t, 10, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
	e.assertMutedInvariant()
	// the ledger keeps the read flag of what went
	require.Equal(t, 20, e.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))
}

func TestCategoriesCappedAndDeduped(t *testing.T) {
	var cats []string
	for i := 0; i < 30; i++ {
		cats = append(cats, fmt.Sprintf("cat%d", i))
	}
	cats = append(cats, "CAT0", "  ", strings.Repeat("x", 300))
	got := fetchCats(t, cats)
	require.Len(t, got, fetch.MaxCategories)
	require.Equal(t, "cat0", got[0])

	long := fetchCats(t, []string{strings.Repeat("y", 300)})
	require.Len(t, []rune(long[0]), fetch.MaxCategoryRunes)
}

func fetchCats(t *testing.T, cats []string) []string {
	t.Helper()
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, frss(fspec{guid: "1", cats: cats}))
	var out []string
	require.NoError(t, json.Unmarshal([]byte(scalar[string](t, e.db.Reader(), "SELECT categories_json FROM item_content")), &out))
	return out
}

func TestPredictedMutesAreSkippedByFulltextPick(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	empty, err := e.db.MutedUIDs(e.ctx, id, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	e.mkFilter(newFilter("mute", "spam"))
	star := newFilter("star", "vip")
	e.mkFilter(star)
	items := []fetch.Item{{UID: "a", Title: "spam here"}, {UID: "b", Title: "fine"}, {UID: "c", Title: "vip spam"}}
	got, err := e.db.MutedUIDs(e.ctx, id, items)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"a": true}, got, "a starred item is not muted, so it is still extracted")
}

func TestIngestPerformanceBudget(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	for i := 0; i < 45; i++ {
		e.mkFilter(newFilter([]string{"mute", "mark_read", "star"}[i%3], fmt.Sprintf("term%da", i), fmt.Sprintf("term%db", i), "two words"))
	}
	for i := 0; i < 5; i++ {
		f := newFilter("mute", fmt.Sprintf(`zz%d[a-z]+q`, i))
		f.Kind = "regex"
		f.Fields = []string{"title", "content"}
		e.mkFilter(f)
	}
	body := frss(fnumbered(500, func(i int) string {
		if i%50 == 0 {
			return "the term3a and two words happen"
		}
		return fmt.Sprintf("An ordinary headline number %d about nothing", i)
	})...)
	start := time.Now()
	info := e.fetchBody(id, body)
	elapsed := time.Since(start)
	require.Equal(t, 500, info.New)
	require.NotZero(t, info.Muted+info.MarkedRead+info.Starred)
	// The whole two-chunk commit, filters included; generous so a loaded CI runner or -race passes.
	ceiling := 3 * time.Second
	if raceEnabled {
		ceiling = 30 * time.Second
	}
	require.Less(t, elapsed, ceiling, "500 items against 50 rules took %s", elapsed)
	t.Logf("500 items x 50 rules committed in %s", elapsed)
}

func TestFilterCRUDValidationAndLimits(t *testing.T) {
	e := newEnv(t)
	bad := newFilter("mute")
	_, err := e.db.CreateFilter(e.ctx, bad)
	var fe *filter.Error
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "terms", fe.Field)

	missing := int64(99)
	f := newFilter("mute", "x")
	f.Scope, f.FeedID = "feed", &missing
	_, err = e.db.CreateFilter(e.ctx, f)
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "feed_id", fe.Field)

	arch := newFilter("mute", "x")
	arch.Kind = "regex"
	arch.Terms = []string{"a*"}
	_, err = e.db.CreateFilter(e.ctx, arch)
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "terms[0]", fe.Field)

	// the set-wide cap: 25 enabled regex rules
	for i := 0; i < filter.MaxRegexRules; i++ {
		r := newFilter("mute", fmt.Sprintf("q%d+", i))
		r.Kind = "regex"
		e.mkFilter(r)
	}
	over := newFilter("mute", "zz+")
	over.Kind = "regex"
	_, err = e.db.CreateFilter(e.ctx, over)
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "kind", fe.Field)

	// nothing was stored by the failures, and a failed update leaves the row alone
	require.Equal(t, filter.MaxRegexRules, e.count("SELECT count(*) FROM filters"))
	all, err := e.db.ListFilters(e.ctx)
	require.NoError(t, err)
	_, _, err = e.db.UpdateFilter(e.ctx, all[0].ID, func(x *Filter) error { x.Terms = []string{"a*"}; return nil })
	require.ErrorAs(t, err, &fe)
	got, ok, err := e.db.GetFilter(e.ctx, all[0].ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, all[0].Terms, got.Terms)
	_, ok, err = e.db.UpdateFilter(e.ctx, 12345, func(*Filter) error { return nil })
	require.NoError(t, err)
	require.False(t, ok)
}
