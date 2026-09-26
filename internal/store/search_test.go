package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"hello world ", `"hello" AND "world"`, true},
		{"hello world", `"hello" AND ("world" OR "world"*)`, true}, // search-as-you-type on the last word
		{"  hello   ", `"hello"`, true},
		{"foo* ", `("foo" OR "foo"*)`, true},
		{"foo** ", `("foo" OR "foo"*)`, true},
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
		{"run", `("run" OR "run"*)`, true},
		{"ru", `"ru"`, true}, // too short for the implicit prefix
		{"ru*", `("ru" OR "ru"*)`, true},
		{"cats -dogs", `("cats") NOT ("dogs")`, true}, // the last term is an exclusion: no prefix
		{"机器学习", `("机器学习" OR "机器学习"*)`, true},
	}
	for _, c := range cases {
		got, ok := BuildFTSQuery(c.in)
		require.Equal(t, c.ok, ok, c.in)
		require.Equal(t, c.want, got, c.in)
	}
	long, _ := BuildFTSQuery(strings.Repeat("word ", 100))
	require.Equal(t, maxSearchTerms, strings.Count(long, `"word"`))
	tok, _ := BuildFTSQuery(strings.Repeat("x", 500) + " ")
	require.Equal(t, `"`+strings.Repeat("x", maxSearchTokenLen)+`"`, tok)
}

func TestFallbackMatch(t *testing.T) {
	cases := map[string]string{
		"apple pie ":       `("apple" OR "apple"*) OR ("pie" OR "pie"*)`,
		`"big dogs" cats `: `("big" OR "big"*) OR ("dogs" OR "dogs"*) OR ("cats" OR "cats"*)`,
		"title:apple pie ": `title : ("apple" OR "apple"*) OR ("pie" OR "pie"*)`,
		"apple -mice ":     `(("apple" OR "apple"*)) NOT ("mice")`,
		"-mice ":           "",
		"":                 "",
	}
	for in, want := range cases {
		require.Equal(t, want, ParseSearch(in).FallbackMatch(), in)
	}
	capped := ParseSearch(strings.Repeat("a ", 40)).FallbackMatch()
	require.Equal(t, maxSearchTerms, strings.Count(capped, `"a"*`))
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
		sq := ParseSearch(raw)
		m, _ := sq.Match()
		for _, expr := range []string{m, sq.FallbackMatch()} {
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
	set := func(q string) map[int64]bool {
		got, fb := searchIDs(t, e, q)
		m := map[int64]bool{}
		if fb {
			return m // only exact matches are compared here; the fallback has its own test
		}
		for _, id := range got {
			m[id] = true
		}
		return m
	}
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
	require.Equal(t, map[int64]bool{comp: true}, set("comput"))
	require.Equal(t, map[int64]bool{run: true, pasta: true}, set("running")) // full word: stemmed alternative of the OR pair
	require.Empty(t, set("runni"))                                           // known limit: a prefix is matched against stems ("run"), so a mid-word prefix of an inflected form finds nothing
	// an unknown column is literal text, not a filter
	require.Empty(t, set("body:cats "))
	// CJK: a run is one token; the whole run and a prefix match, a middle substring does not
	require.Equal(t, map[int64]bool{cjk: true}, set("机器学习入门 "))
	require.Equal(t, map[int64]bool{cjk: true}, set("机器"))
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
