package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"hello world ", `"hello" AND "world"`, true},
		{"hello world", `"hello" AND "world"`, true}, // no implicit prefix without typing
		{"  hello   ", `"hello"`, true},
		{"foo* ", `"foo"*`, true},
		{"foo** ", `"foo"*`, true},
		{`say "hi there" `, `"say" AND "hi there"`, true},
		{`"exact phrase"`, `"exact phrase"`, true},
		{`"unterminated phrase`, `"unterminated phrase"`, true},
		{`"" ""`, "", false},
		{"title:foo ", `title : "foo"`, true},
		{"TITLE:foo ", `title : "foo"`, true},
		{`author:"jane doe" cats `, `author : "jane doe" AND "cats"`, true},
		{"body:foo ", `"body:foo"`, true}, // other columns are literal text
		{"content_text:foo ", `"content_text:foo"`, true},
		{"{title author}: x ", `"{title" AND "author}:" AND "x"`, true},
		{"title: ", `"title:"`, true},
		{"cats -dogs ", `("cats") NOT ("dogs")`, true},
		{"cats NOT dogs ", `("cats") NOT ("dogs")`, true},
		{"cats -dogs -title:mice ", `("cats") NOT ("dogs" OR title : "mice")`, true},
		{`cats -"big dogs" `, `("cats") NOT ("big dogs")`, true},
		{"-dogs ", "", false}, // exclusion alone matches nothing
		{"NOT dogs ", "", false},
		{"cats NOT ", `"cats"`, true},
		{"NOT foo OR bar ", `("OR" AND "bar") NOT ("foo")`, true},
		{"NEAR(a b) ", `"NEAR(a" AND "b)"`, true},
		{"^start ", `"^start"`, true},
		{"a\x00b ", `"a" AND "b"`, true},
		{"- + ( ) * ", "", false},
		{"", "", false},
		{"   ", "", false},
		{"naïve café ", `"naïve" AND "café"`, true},
		{"run", `"run"`, true},
		{"ru", `"ru"`, true},
		{"ru*", `"ru"`, true}, // an explicit prefix needs 3 runes: shorter is the literal word
		{"a* b* ", `"a" AND "b"`, true},
		{"aaa* bbb* ccc* ddd* ", `"aaa"* AND "bbb"* AND "ccc"* AND "ddd"`, true}, // at most 3 prefixes
		{"机* 机器* ", `"机" AND "机器"*`, true},
		{"cats -dogs", `("cats") NOT ("dogs")`, true}, // the last term is an exclusion: no prefix
		{"机器学习", `"机器学习"`, true},
	}
	for _, c := range cases {
		got, ok := BuildFTSQuery(c.in, false)
		require.Equal(t, c.ok, ok, c.in)
		require.Equal(t, c.want, got, c.in)
	}
	long, _ := BuildFTSQuery(strings.Repeat("word ", 100), false)
	require.Equal(t, maxSearchTerms, strings.Count(long, `"word"`))
	tok, _ := BuildFTSQuery(strings.Repeat("x", 500)+" ", false)
	require.Equal(t, `"`+strings.Repeat("x", maxSearchTokenLen)+`"`, tok)
}

// Search-as-you-type: the unfinished last word is a prefix only with typing set, and only when
// it is long enough and the text does not end in a space.
func TestBuildFTSQueryTyping(t *testing.T) {
	cases := map[string]string{
		"hello world":        `"hello" AND "world"*`,
		"hello world ":       `"hello" AND "world"`,
		"run":                `"run"*`,
		"ru":                 `"ru"`,
		"cats -dogs":         `("cats") NOT ("dogs")`,
		"机器":                 `"机器"*`,
		"机":                  `"机"`,
		"aaa* bbb* ccc* ddd": `"aaa"* AND "bbb"* AND "ccc"* AND "ddd"`, // the prefix cap holds while typing
	}
	for in, want := range cases {
		got, ok := BuildFTSQuery(in, true)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
}

func TestFallbackMatch(t *testing.T) {
	cases := map[string]string{
		"apple pie ":       `"apple"* OR "pie"*`,
		`"big dogs" cats `: `"big"* OR "dogs"* OR "cats"*`,
		"title:apple pie ": `title : "apple"* OR "pie"*`,
		"apple -mice ":     `("apple"*) NOT ("mice")`,
		"a apple e ":       `"apple"*`, // words under 3 runes are dropped, not ORed in
		"a b c ":           "",
		"aaa bbb ccc ddd ": `"aaa"* OR "bbb"* OR "ccc"* OR "ddd"`, // three prefixes at most
		"机 机器 ":            `"机器"*`,
		"-mice ":           "",
		"":                 "",
	}
	for in, want := range cases {
		require.Equal(t, want, ParseSearch(in, false).FallbackMatch(), in)
	}
	capped := ParseSearch(strings.Repeat("abc ", 40), false).FallbackMatch()
	require.Equal(t, maxSearchTerms, strings.Count(capped, `"abc"`))
	require.Equal(t, maxPrefixTerms, strings.Count(capped, `"abc"*`))
}

func TestSnippetHTMLEscapes(t *testing.T) {
	got := snippetHTML("a <script>alert(1)</script> " + snipOpen + "zebra" + snipClose + " &")
	require.Equal(t, "a &lt;script&gt;alert(1)&lt;/script&gt; <mark>zebra</mark> &amp;", got)
}

func TestRankCursorRoundTrip(t *testing.T) {
	c := Cursor{ID: 42, Rank: -1.2345678901234567e-6, ByRank: true}
	back, err := ParseCursor(c.Encode())
	require.NoError(t, err)
	require.Equal(t, c, back)
	c.Fallback = true
	back, err = ParseCursor(c.Encode())
	require.NoError(t, err)
	require.Equal(t, c, back)
	for _, d := range []Cursor{{SortAt: 7, ID: 9}, {SortAt: 7, ID: 9, Asc: true}, {SortAt: -7, ID: 9, Fallback: true}, {SortAt: 7, ID: 9, Asc: true, Fallback: true}} {
		back, err = ParseCursor(d.Encode())
		require.NoError(t, err)
		require.Equal(t, d, back)
	}
	for _, bad := range []string{"", "!!", "cg==", "cjF8"} {
		_, err := ParseCursor(bad)
		require.Error(t, err, bad)
	}
}

// A relevance cursor issued before schema 5 (the rank basis changed) is refused, not misread.
func TestOldRankCursorRejected(t *testing.T) {
	for _, raw := range []string{"r-1.2|5", "r0|9", "r1.5|123"} {
		_, err := ParseCursor(b64(raw))
		require.Error(t, err, raw)
	}
	_, err := ParseCursor(b64("s2|-1.5|5"))
	require.NoError(t, err)
	for _, raw := range []string{"s2|x|5", "s2-1.5|5", "s2|-1.5", "s2f-1.5|5"} {
		_, err = ParseCursor(b64(raw))
		require.Error(t, err, raw)
	}
}

func FuzzBuildFTSQuery(f *testing.F) {
	for _, s := range []string{
		"", "a", `"`, `""`, `"a b" -c title:d author:"e f" g* `, "NOT", "NOT NOT x", "-", "--x", "title:", `title:"`, `-title:"x`,
		"NEAR(a b)", "a OR b", "(", "))((", "^a", "a:b:c", "\x00\x01", "\xff\xfe", "机器学习 -x", strings.Repeat("(", 600),
		strings.Repeat(`"a `, 200), "*", "**a", `a"b`, "{title}: x", "a AND b", "col:x", "rank:1", "-title:a -author:b c",
	} {
		f.Add(s)
	}
	db, err := Open(context.Background(), Options{Path: filepath.Join(f.TempDir(), "kipple.db")})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = db.Close() })
	f.Fuzz(func(t *testing.T, raw string) {
		sq, sqt := ParseSearch(raw, false), ParseSearch(raw, true)
		m, _ := sq.Match()
		mt, _ := sqt.Match()
		for _, expr := range []string{m, mt, sq.FallbackMatch(), sqt.FallbackMatch()} {
			if expr == "" {
				continue
			}
			// The expression must never be an FTS syntax error on a live index.
			var n int
			err := db.Reader().QueryRow(`SELECT count(*) FROM items_fts WHERE items_fts MATCH ?`, expr).Scan(&n)
			require.NoError(t, err, "raw=%q match=%q", raw, expr)
		}
	})
}

// ---- behavior on a live index ----

type sitem struct {
	title, author, text string
}

func seedSearch(t *testing.T, e *env, items ...sitem) []int64 {
	t.Helper()
	fid := e.addFeed("https://ex.com/feed")
	var ids []int64
	for i, it := range items {
		id := int64(1_700_000_000_000_000 + i*1000)
		at := int64(1000 + i)
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author)
			VALUES (?,?,0,0,?,?,10,?,?,?,?,?,?)`, id, fid, at, at, fmt.Sprintf("g%d", id), "c", "t", fmt.Sprintf("https://x/%d", id), it.title, it.author)
		e.exec(`INSERT INTO item_content (item_id, content_html, content_text) VALUES (?,?,?)`, id, "<p>"+it.text+"</p>", it.text)
		ids = append(ids, id)
	}
	return ids
}

func searchIDs(t *testing.T, e *env, q string, opts ...func(*CardQuery)) ([]int64, bool) {
	t.Helper()
	cq := CardQuery{View: "all", Query: q, Limit: 100}
	for _, o := range opts {
		o(&cq)
	}
	cards, _, fb, err := e.db.ListCardsFB(e.ctx, cq)
	require.NoError(t, err)
	var ids []int64
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	return ids, fb
}

func TestSearchV2(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Running the marathon", "Ann", "she ran and runs every morning"},
		sitem{"Cooking pasta", "Bob Smith", "boil water; the running water is salted"},
		sitem{"Computers today", "Cy", "computing machines compute quickly"},
		sitem{"Café culture", "Dee", "naïve visitors love the café near the resume desk"},
		sitem{"Dogs and cats", "Eve", "the big dogs chase cats"},
		sitem{"Cats only", "Fay Jones", "quiet cats sleep"},
		sitem{"机器学习入门", "Gu", "我们讨论机器学习和深度学习"},
	)
	run, pasta, comp, cafe, dogs, cats, cjk := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5], ids[6]
	setWith := func(q string, typing bool) map[int64]bool {
		got, fb := searchIDs(t, e, q, func(c *CardQuery) { c.Typing = typing })
		m := map[int64]bool{}
		if fb {
			return m // only exact matches are compared here; the fallback has its own test
		}
		for _, id := range got {
			m[id] = true
		}
		return m
	}
	set := func(q string) map[int64]bool { return setWith(q, false) }
	// stemming: running, runs and run share the stem "run"
	require.Equal(t, map[int64]bool{run: true, pasta: true}, set("running "))
	require.Equal(t, map[int64]bool{run: true, pasta: true}, set("run "))
	require.Equal(t, map[int64]bool{run: true, pasta: true}, set("runs "))
	require.Equal(t, map[int64]bool{comp: true}, set("computer "))
	// diacritics fold both ways
	require.Equal(t, map[int64]bool{cafe: true}, set("cafe "))
	require.Equal(t, map[int64]bool{cafe: true}, set("café "))
	require.Equal(t, map[int64]bool{cafe: true}, set("naive "))
	// phrases
	require.Equal(t, map[int64]bool{dogs: true}, set(`"big dogs" `))
	require.Empty(t, set(`"dogs big" `))
	// NOT and -
	require.Equal(t, map[int64]bool{cats: true}, set("cats -dogs "))
	require.Equal(t, map[int64]bool{cats: true}, set("cats NOT dogs "))
	require.Equal(t, map[int64]bool{dogs: true, cats: true}, set("cats "))
	// column filters
	require.Equal(t, map[int64]bool{cats: true}, set("cats -title:dogs "))
	require.Equal(t, map[int64]bool{dogs: true, cats: true}, set("title:cats "))
	require.Equal(t, map[int64]bool{pasta: true}, set("author:smith "))
	require.Equal(t, map[int64]bool{cats: true}, set(`author:"fay jones" `))
	require.Empty(t, set("title:smith "))
	// as-you-type prefix on the last word only
	require.Equal(t, map[int64]bool{comp: true}, setWith("comput", true))
	require.Equal(t, map[int64]bool{comp: true}, setWith("compu", true))
	require.Empty(t, set("compu")) // no typing: "compu" is a whole word, not a prefix
	require.Equal(t, map[int64]bool{run: true, pasta: true}, setWith("running", true))
	require.Empty(t, setWith("runni", true)) // known limit: a prefix is matched against stems ("run"), so a mid-word prefix of an inflected form finds nothing
	// an unknown column is literal text, not a filter
	require.Empty(t, set("body:cats "))
	// CJK: a run is one token; the whole run and a prefix match, a middle substring does not
	require.Equal(t, map[int64]bool{cjk: true}, set("机器学习入门 "))
	require.Equal(t, map[int64]bool{cjk: true}, setWith("机器", true))
	require.Equal(t, map[int64]bool{cjk: true}, set("机器*"))
	// injection never errors and never filters
	for _, q := range []string{"title:", `"`, "NEAR(a b)", "a OR", "((", "*", "content_text:cats", `x" OR "y`, "-", "NOT"} {
		_, _ = searchIDs(t, e, q)
	}
}

func TestSearchFallback(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Apple orchards", "Ann", "apples grow in orchards"},
		sitem{"Pie recipes", "Bob", "a pie needs pastry"},
		sitem{"Both", "Cy", "apple pie is the best"},
	)
	got, fb := searchIDs(t, e, "apple pie ")
	require.Equal(t, []int64{ids[2]}, got)
	require.False(t, fb)
	// nothing has both: the partial match ORs the words and says so
	got, fb = searchIDs(t, e, "orchards pastry ")
	require.True(t, fb)
	require.ElementsMatch(t, []int64{ids[0], ids[1]}, got)
	// the fallback is a prefix match too
	got, fb = searchIDs(t, e, "orchar past ")
	require.True(t, fb)
	require.ElementsMatch(t, []int64{ids[0], ids[1]}, got)
	// exclusions still apply in fallback mode
	got, fb = searchIDs(t, e, "orchards pastry -pie ")
	require.True(t, fb)
	require.Equal(t, []int64{ids[0]}, got)
	// no match at all: empty, no flag
	got, fb = searchIDs(t, e, "zzzz qqqq ")
	require.Empty(t, got)
	require.False(t, fb)
	// a single word that matches exactly is not a fallback
	_, fb = searchIDs(t, e, "orchards ")
	require.False(t, fb)
	// scope-aware: exact matches outside the view do not suppress the fallback inside it
	e.exec("UPDATE items SET starred = 1 WHERE id = ?", ids[1])
	got, fb = searchIDs(t, e, "apple pie ", func(q *CardQuery) { q.View = "starred" })
	require.True(t, fb)
	require.Equal(t, []int64{ids[1]}, got)
}

func TestSearchFallbackPagesStayInFallback(t *testing.T) {
	e := newEnv(t)
	var items []sitem
	for i := 0; i < 7; i++ {
		items = append(items, sitem{title: fmt.Sprintf("t%d", i), text: "alpha only here"})
	}
	items = append(items, sitem{title: "u", text: "beta only there"})
	seedSearch(t, e, items...)
	for _, rank := range []bool{false, true} {
		var got int
		var cur *Cursor
		for pages := 0; ; pages++ {
			require.Less(t, pages, 10)
			cards, next, fb, err := e.db.ListCardsFB(e.ctx, CardQuery{View: "all", Query: "alpha beta ", Limit: 3, Rank: rank, Cursor: cur})
			require.NoError(t, err)
			require.True(t, fb, "every page reports the fallback (rank=%v page %d)", rank, pages)
			got += len(cards)
			if next == nil {
				break
			}
			require.True(t, next.Fallback)
			back, err := ParseCursor(next.Encode())
			require.NoError(t, err)
			cur = &back
		}
		require.Equal(t, 8, got)
	}
}

func TestSearchRankWeightsTitle(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Something else", "Ann", "kiwi kiwi kiwi kiwi in the body of a somewhat longer text"},
		sitem{"Kiwi", "Bob", "short"},
	)
	got, _ := searchIDs(t, e, "kiwi ", func(q *CardQuery) { q.Rank = true })
	require.Equal(t, []int64{ids[1], ids[0]}, got) // a title hit outweighs body repeats
}

// scope.q for mark-read chooses its expression exactly as the list does.
func TestMarkScopeUsesSameFallback(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Apple orchards", "Ann", "apples grow"},
		sitem{"Pie recipes", "Bob", "a pie needs pastry"},
		sitem{"Other", "Cy", "nothing relevant"},
	)
	mark := func(q string, unread bool) []int64 {
		var res StateResult
		require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			res, err = MarkScopeRead(ctx, tx, MarkScope{}, MarkFilter{Query: q, Unread: unread}, 1<<62, 1)
			return err
		}))
		return res.Changed
	}
	listed, fb := searchIDs(t, e, "orchards pastry ")
	require.True(t, fb)
	require.ElementsMatch(t, listed, mark("orchards pastry ", false))
	e.exec("UPDATE items SET read = 0")
	// exact matches exist: only they are marked
	require.Equal(t, []int64{ids[1]}, mark("pie ", true))
	// unsearchable text marks nothing
	require.Empty(t, mark("- ", false))
	require.Empty(t, mark("-pie ", false))
}

// FTS5's porter tokenizer stems a prefix query too ("running"* is "run"*), so a finished word never
// widens into its stem's prefix: "apple" does not find "application" (stem "applic" starts with
// the stem "appl"), while "apple*" and search-as-you-type do, by design (design §2.4).
func TestSearchPrefixIsStemPrefix(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Apple pie", "Ann", "an apple a day"},
		sitem{"Application form", "Bob", "please fill in the application"},
		sitem{"Police report", "Cy", "the police arrived"},
		sitem{"Policy paper", "Di", "a new policy on policies"},
		sitem{"Running late", "Ed", "he runs and ran"},
	)
	apple, application, police, policy, running := ids[0], ids[1], ids[2], ids[3], ids[4]
	list := func(q string, typing bool) []int64 {
		got, fb := searchIDs(t, e, q, func(c *CardQuery) { c.Typing = typing })
		require.False(t, fb, q)
		return got
	}
	require.ElementsMatch(t, []int64{apple}, list("apple ", false))
	require.ElementsMatch(t, []int64{apple}, list("apple", false), "no typing: a finished word never widens")
	require.ElementsMatch(t, []int64{police}, list("police", false))
	require.ElementsMatch(t, []int64{apple, application}, list("apple*", false), "an explicit prefix is stem*")
	require.ElementsMatch(t, []int64{apple, application}, list("apple", true), "typing: the last word is stem*")
	require.ElementsMatch(t, []int64{apple}, list("apple ", true), "a trailing space finishes the word")
	require.ElementsMatch(t, []int64{police, policy}, list("police*", false))
	// the prefix of an inflection and of its stem are the same query
	require.Equal(t, list("running*", false), list("run*", false))
	require.ElementsMatch(t, []int64{running}, list("running*", false))
}

// A saved search or mark-read scope is never a typing request, so they count the stemmed words
// only, even when the text does not end in a space.
func TestMarkScopeIsNeverPrefix(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Apple pie", "Ann", "an apple a day"},
		sitem{"Application form", "Bob", "please fill in the application"},
	)
	var res StateResult
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		res, err = MarkScopeRead(ctx, tx, MarkScope{}, MarkFilter{Query: "apple", Unread: true}, 1<<62, 1)
		return err
	}))
	require.Equal(t, []int64{ids[0]}, res.Changed)
}

// The list decides exact-vs-fallback at one moment and mark-read decides again in its own
// transaction. When an exact match arrives in between, a client that echoes the list's fallback
// flag still marks exactly what the list showed; one that does not gets the new decision.
func TestMarkScopeHonorsListFallback(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Apple orchards", "Ann", "apples grow"},
		sitem{"Pie recipes", "Bob", "a pie needs pastry"},
	)
	listed, listedFB := searchIDs(t, e, "orchards pastry ")
	require.True(t, listedFB)
	require.ElementsMatch(t, []int64{ids[0], ids[1]}, listed)
	asOf := ids[1] // the list's as_of: later arrivals are above it

	// The race window: an exact match for both words is committed after the list, before mark-read.
	var fid int64
	require.NoError(t, e.db.Reader().QueryRow("SELECT id FROM feeds LIMIT 1").Scan(&fid))
	late := asOf + 99_000
	e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author)
		VALUES (?,?,0,0,5000,5000,10,'gl','c','t','https://x/late','Late','Di')`, late, fid)
	e.exec(`INSERT INTO item_content (item_id, content_html, content_text) VALUES (?,?,?)`, late, "<p>x</p>", "orchards and pastry together")

	mark := func(fb *bool) []int64 {
		e.exec("UPDATE items SET read = 0")
		var res StateResult
		require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			res, err = MarkScopeRead(ctx, tx, MarkScope{}, MarkFilter{Query: "orchards pastry ", Unread: true, Fallback: fb}, asOf, 1)
			return err
		}))
		return res.Changed
	}
	yes, no := true, false
	require.ElementsMatch(t, []int64{ids[0], ids[1]}, mark(&yes), "the echoed fallback flag marks what the list showed")
	require.Empty(t, mark(&no), "an echoed exact list marks exact matches only (none at or below as_of)")
	require.Empty(t, mark(nil), "without the flag the fresh decision is exact and marks little or nothing: the old behavior")
}

// A search that outruns its budget answers ErrSearchTooBroad instead of holding a reader.
func TestSearchTooBroad(t *testing.T) {
	e := newEnv(t)
	// Enough matching rows that the search really is still running when its (already expired)
	// deadline is noticed: with one row it could finish first, which made this test flaky under load.
	items := make([]sitem, 0, 1500)
	for i := 0; i < 1500; i++ {
		items = append(items, sitem{fmt.Sprintf("Apple %d", i), "Ann", "apple pie with more apple and pastry text to rank"})
	}
	seedSearch(t, e, items...)
	old := searchBudget
	searchBudget = time.Nanosecond
	t.Cleanup(func() { searchBudget = old })
	_, _, _, err := e.db.ListCardsFB(e.ctx, CardQuery{View: "all", Query: "apple ", Limit: 10})
	require.ErrorIs(t, err, ErrSearchTooBroad)
}

// One page of a search walks at most searchScanLimit matches: over it the search is refused, on the
// first page and on any later one (a client-supplied cursor included), in both orders, in a feed or folder
// scope and in the partial-match (OR) mode; exactly the limit is answered.
func TestSearchScanLimit(t *testing.T) {
	e := newEnv(t)
	items := make([]sitem, 0, 8)
	for i := 0; i < 8; i++ {
		items = append(items, sitem{fmt.Sprintf("Apple %d", i), "Ann", "apple pie"})
	}
	ids := seedSearch(t, e, items...)
	old := searchScanLimit
	t.Cleanup(func() { searchScanLimit = old })
	list := func(q CardQuery) ([]Card, *Cursor, bool, error) {
		q.View, q.Limit = "all", 3
		return e.db.ListCardsFB(e.ctx, q)
	}

	for _, rank := range []bool{false, true} {
		searchScanLimit = 8
		cards, cur, _, err := list(CardQuery{Query: "apple ", Rank: rank})
		require.NoError(t, err)
		require.Len(t, cards, 3)
		require.NotNil(t, cur)
		// A later page of a search at the limit is answered (it walks what remains, or all of it by rank).
		cards, _, _, err = list(CardQuery{Query: "apple ", Rank: rank, Cursor: cur})
		require.NoError(t, err)
		require.NotEmpty(t, cards)

		searchScanLimit = 7
		_, _, _, err = list(CardQuery{Query: "apple ", Rank: rank})
		require.ErrorIs(t, err, ErrSearchTooBroad, "first page, rank=%v", rank)
		// A cursor that skips nothing (as a client can forge one) walks everything and is refused too.
		forged := &Cursor{SortAt: 1 << 40, ID: 1 << 60, ByRank: rank, Rank: -1e9}
		_, _, _, err = list(CardQuery{Query: "apple ", Rank: rank, Cursor: forged})
		require.ErrorIs(t, err, ErrSearchTooBroad, "forged cursor, rank=%v", rank)
	}
	// By date a real cursor leaves fewer than the limit to walk, so the later page of a series is answered.
	searchScanLimit = 8
	_, cur, _, err := list(CardQuery{Query: "apple "})
	require.NoError(t, err)
	searchScanLimit = 5
	cards, _, _, err := list(CardQuery{Query: "apple ", Cursor: cur})
	require.NoError(t, err)
	require.Len(t, cards, 3)

	// A feed or folder scope counts its own matches only.
	other := e.addFeed("https://ex.com/other")
	folder := e.mkFolder(0, "Scoped")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", folder, other)
	for _, id := range ids[:3] {
		e.exec("UPDATE items SET feed_id = ? WHERE id = ?", other, id)
	}
	searchScanLimit = 5
	_, _, _, err = list(CardQuery{Query: "apple "})
	require.ErrorIs(t, err, ErrSearchTooBroad)
	for _, scope := range []CardQuery{{FeedID: other}, {FolderID: folder}} {
		scope.Query = "apple "
		cards, _, _, err = list(scope)
		require.NoError(t, err, "%+v", scope)
		require.Len(t, cards, 3)
	}
	_, _, _, err = list(CardQuery{Query: "apple ", FeedID: 1})
	require.NoError(t, err, "the other feed holds five")

	// The partial-match mode (no item has both words, so they are ORed) is held to the limit as well.
	searchScanLimit = 8
	cards, _, fb, err := list(CardQuery{Query: "apple pear "})
	require.NoError(t, err)
	require.True(t, fb)
	require.Len(t, cards, 3)
	searchScanLimit = 7
	_, _, _, err = list(CardQuery{Query: "apple pear "})
	require.ErrorIs(t, err, ErrSearchTooBroad)
}
