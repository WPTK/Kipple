package filter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func text(terms ...string) Rule { return NewRule(ScopeGlobal, KindText, ActionMute, terms...) }

func fires(t *testing.T, r Rule, it Item) bool {
	t.Helper()
	c, err := CompileRule(r)
	require.NoError(t, err)
	return c.Fires(it)
}

func TestTextMatching(t *testing.T) {
	tests := []struct {
		name string
		rule func(*Rule)
		term string
		item Item
		want bool
	}{
		{"word", nil, "go", Item{Title: "Learning Go today"}, true},
		{"word not inside another word", nil, "go", Item{Title: "Going places, mango"}, false},
		{"word before punctuation", nil, "go", Item{Title: "Go, go, go!"}, true},
		{"substring when whole_word is off", func(r *Rule) { r.WholeWord = false }, "go", Item{Title: "mango"}, true},
		{"case-insensitive by default", nil, "GOLANG", Item{Title: "golang 1.27"}, true},
		{"case-sensitive", func(r *Rule) { r.CaseSensitive = true }, "Go", Item{Title: "go"}, false},
		{"case-sensitive hit", func(r *Rule) { r.CaseSensitive = true }, "Go", Item{Title: "Go"}, true},
		{"phrase with any whitespace", nil, "breaking news", Item{Title: "Breaking\n\t  news: x"}, true},
		{"phrase keeps order", nil, "breaking news", Item{Title: "news breaking"}, false},
		{"phrase whole word", nil, "new york", Item{Title: "in new yorker"}, false},
		{"diacritics folded", nil, "cafe", Item{Title: "Le Café"}, true},
		{"diacritics folded in the term", nil, "café", Item{Title: "the cafe"}, true},
		{"diacritics kept when fold is off", func(r *Rule) { r.FoldDiacritics = false }, "cafe", Item{Title: "Café"}, false},
		{"compat forms folded", nil, "file", Item{Title: "ﬁle"}, true},
		{"punctuation term", nil, "c++", Item{Title: "Why C++ still matters"}, true},
		{"punctuation term boundary on the word side", nil, "c++", Item{Title: "abc++"}, false},
		{"dotted term", nil, "node.js", Item{Title: "Node.js 30 released"}, true},
		{"CJK matches as a substring", nil, "中国", Item{Title: "我爱中国人民"}, true},
		{"CJK miss", nil, "日本", Item{Title: "我爱中国人民"}, false},
		{"Latin word glued to CJK", nil, "iphone", Item{Title: "新iPhone发布"}, true},
		{"digits count as word characters", nil, "3", Item{Title: "top 30 tips"}, false},
		{"digit word", nil, "30", Item{Title: "top 30 tips"}, true},
		{"underscore is not a word rune", nil, "foo", Item{Title: "foo_bar"}, true},
		{"empty haystack", nil, "x", Item{}, false},
		{"author", func(r *Rule) { r.Fields = []Field{FieldAuthor} }, "alice", Item{Author: "Alice Smith"}, true},
		{"author not scanned by default", nil, "alice", Item{Author: "Alice Smith"}, false},
		{"url", func(r *Rule) { r.Fields = []Field{FieldURL}; r.WholeWord = false }, "example.com/sponsored", Item{URL: "https://example.com/sponsored/x"}, true},
		{"feed title", func(r *Rule) { r.Fields = []Field{FieldFeed} }, "hacker news", Item{FeedTitle: "Hacker News"}, true},
		{"content", func(r *Rule) { r.Fields = []Field{FieldContent} }, "giveaway", Item{Content: "enter our giveaway now"}, true},
		{"any field of several", func(r *Rule) { r.Fields = []Field{FieldTitle, FieldContent} }, "giveaway", Item{Title: "x", Content: "a giveaway"}, true},
		{"category", func(r *Rule) { r.Fields = []Field{FieldCategory} }, "sponsored", Item{Categories: []string{"news", "Sponsored"}}, true},
		{"phrase does not span categories", func(r *Rule) { r.Fields = []Field{FieldCategory} }, "news sponsored", Item{Categories: []string{"news", "sponsored"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := text(tt.term)
			if tt.rule != nil {
				tt.rule(&r)
			}
			require.Equal(t, tt.want, fires(t, r, tt.item))
		})
	}
}

func TestAnyOfSeveralTerms(t *testing.T) {
	require.True(t, fires(t, text("zzz", "hell", "hello"), Item{Title: "hello"}))
	require.False(t, fires(t, text("zzz", "hell"), Item{Title: "hello"}))
}

func TestInvert(t *testing.T) {
	r := text("rust")
	r.Invert = true
	require.True(t, fires(t, r, Item{Title: "Go news"}))
	require.False(t, fires(t, r, Item{Title: "Rust news"}))
	require.True(t, fires(t, r, Item{}), "an inverted rule fires on empty fields")
	r.Fields = []Field{FieldTitle, FieldContent}
	require.False(t, fires(t, r, Item{Title: "Go", Content: "rust inside"}), "any field matching stops the inverted rule")
}

func TestRegex(t *testing.T) {
	re := func(p string, mut ...func(*Rule)) Rule {
		r := NewRule(ScopeGlobal, KindRegex, ActionMute, p)
		for _, m := range mut {
			m(&r)
		}
		return r
	}
	require.True(t, fires(t, re(`^\[ad\]`), Item{Title: "[AD] buy now"}), "case-insensitive by default")
	require.False(t, fires(t, re(`^\[ad\]`, func(r *Rule) { r.CaseSensitive = true }), Item{Title: "[AD] buy now"}))
	require.True(t, fires(t, re(`\d{4}-\d{2}`), Item{Title: "issue 2026-09"}))
	require.False(t, fires(t, re(`^\[ad\]`), Item{Title: "not [ad]"}))
	multi := re(`zzz`)
	multi.Terms = []string{`zzz`, `new+s`}
	require.True(t, fires(t, multi, Item{Title: "newws"}))
	require.True(t, fires(t, re(`sponsored`, func(r *Rule) { r.Fields = []Field{FieldCategory} }), Item{Categories: []string{"a", "Sponsored"}}))
	// The regex sees only the first 8 KiB of content.
	long := strings.Repeat("x ", MaxRegexContentScan/2) + "needle"
	require.False(t, fires(t, re(`needle`, func(r *Rule) { r.Fields = []Field{FieldContent} }), Item{Content: long}))
	require.True(t, fires(t, re(`needle`, func(r *Rule) { r.Fields = []Field{FieldContent} }), Item{Content: "needle" + long}))
	// Text rules see the first 32 KiB.
	t32 := strings.Repeat("x ", MaxTextContentScan/2) + "needle"
	require.False(t, fires(t, text("needle").with(func(r *Rule) { r.Fields = []Field{FieldContent} }), Item{Content: t32}))
	t31 := strings.Repeat("x ", MaxTextContentScan/2-10) + "needle"
	require.True(t, fires(t, text("needle").with(func(r *Rule) { r.Fields = []Field{FieldContent} }), Item{Content: t31}))
}

func (r Rule) with(f func(*Rule)) Rule { f(&r); return r }

func TestValidateRejections(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name  string
		rule  Rule
		field string
	}{
		{"no terms", text(), "terms"},
		{"empty term", text(""), "terms[0]"},
		{"blank term", text("ok", "  \t"), "terms[1]"},
		{"only combining marks", text("́"), "terms[0]"},
		{"long term", text(strings.Repeat("é", MaxTermRunes+1)), "terms[0]"},
		{"51 terms", text(make([]string, 51)...), "terms"},
		{"NUL", text("a\x00b"), "terms[0]"},
		{"invalid UTF-8", text("a\xffb"), "terms[0]"},
		{"bad scope", NewRule("nope", KindText, ActionMute, "a"), "scope"},
		{"folder scope without a folder", NewRule(ScopeFolder, KindText, ActionMute, "a"), "scope"},
		{"global with a feed", text("a").with(func(r *Rule) { r.FeedID = 3 }), "scope"},
		{"feed and folder", NewRule(ScopeFeed, KindText, ActionMute, "a").with(func(r *Rule) { r.FeedID, r.FolderID = 1, 1 }), "scope"},
		{"bad kind", NewRule(ScopeGlobal, "glob", ActionMute, "a"), "kind"},
		{"bad action", NewRule(ScopeGlobal, KindText, "delete", "a"), "action"},
		{"highlight regex", NewRule(ScopeGlobal, KindRegex, ActionHighlight, "a"), "action"},
		{"bad field", text("a").with(func(r *Rule) { r.Fields = []Field{FieldTitle, "body"} }), "fields[1]"},
		{"regex syntax", NewRule(ScopeGlobal, KindRegex, ActionMute, "a("), "terms[0]"},
		{"regex backreference", NewRule(ScopeGlobal, KindRegex, ActionMute, `(a)\1`), "terms[0]"},
		{"regex \\C", NewRule(ScopeGlobal, KindRegex, ActionMute, `a\Cb`), "terms[0]"},
		{"regex lookahead", NewRule(ScopeGlobal, KindRegex, ActionMute, `a(?=b)`), "terms[0]"},
		{"regex matches empty", NewRule(ScopeGlobal, KindRegex, ActionMute, `a*`), "terms[0]"},
		{"regex matches empty (anchors)", NewRule(ScopeGlobal, KindRegex, ActionMute, `^$`), "terms[0]"},
		{"regex too long", NewRule(ScopeGlobal, KindRegex, ActionMute, long(257)), "terms[0]"},
		{"regex too many patterns", NewRule(ScopeGlobal, KindRegex, ActionMute, "a", "b", "c", "d", "e", "f"), "terms"},
		{"regex too big a program", NewRule(ScopeGlobal, KindRegex, ActionMute, `[a-z]{1000}[a-z]{1000}[a-z]{1000}[a-z]{1000}[a-z]{1000}[a-z]{1000}`), "terms[0]"},
		{"regex nested repeat", NewRule(ScopeGlobal, KindRegex, ActionMute, `(a{100}){100}`), "terms[0]"},
		{"regex second pattern bad", NewRule(ScopeGlobal, KindRegex, ActionMute, "ok", "(("), "terms[1]"},
		{"long name", text("a").with(func(r *Rule) { r.Name = long(MaxNameBytes + 1) }), "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.rule)
			require.Error(t, err)
			var fe *Error
			require.ErrorAs(t, err, &fe)
			require.Equal(t, tt.field, fe.Field, fe.Message)
			require.NotEmpty(t, fe.Message)
		})
	}
	require.NoError(t, Validate(text("ok")))
	require.NoError(t, Validate(NewRule(ScopeGlobal, KindRegex, ActionStar, `\bgo(lang)?\b`)))
	require.NoError(t, Validate(NewRule(ScopeGlobal, KindRegex, ActionStar, `[a-z]{200}`)), "a large but bounded repeat is fine")
	// A blank field list means title only; duplicates collapse.
	c, err := CompileRule(text("a").with(func(r *Rule) { r.Fields = []Field{FieldURL, FieldURL} }))
	require.NoError(t, err)
	require.Equal(t, []Field{FieldURL}, c.Rule().Fields)
	c, err = CompileRule(text("a").with(func(r *Rule) { r.Fields = nil }))
	require.NoError(t, err)
	require.Equal(t, []Field{FieldTitle}, c.Rule().Fields)
}

func rule(id int64, action Action, term string) Rule {
	r := NewRule(ScopeGlobal, KindText, action, term)
	r.ID = id
	return r
}

func TestPrecedence(t *testing.T) {
	item := Item{Title: "golang release"}
	mk := func(rs ...Rule) Result {
		s, err := NewSet(rs)
		require.NoError(t, err)
		return s.Evaluate(item)
	}
	// Mute alone: read, muted_by = the lowest matching mute id, whatever order the rules came in.
	res := mk(rule(9, ActionMute, "golang"), rule(4, ActionMute, "release"), rule(2, ActionMute, "nomatch"))
	require.True(t, res.Muted)
	require.True(t, res.Read)
	require.EqualValues(t, 4, res.MutedBy)
	require.Equal(t, []int64{4, 9}, res.Matched)
	// Star beats mute: not muted, not read.
	res = mk(rule(1, ActionMute, "golang"), rule(2, ActionStar, "release"))
	require.True(t, res.Star)
	require.False(t, res.Muted)
	require.Zero(t, res.MutedBy)
	require.False(t, res.Read)
	require.Equal(t, []int64{1, 2}, res.Matched)
	// Star with mark_read: starred and read.
	res = mk(rule(1, ActionMute, "golang"), rule(2, ActionStar, "golang"), rule(3, ActionMarkRead, "golang"))
	require.True(t, res.Star)
	require.False(t, res.Muted)
	require.True(t, res.MarkRead)
	require.True(t, res.Read)
	// mark_read alone: read, not muted.
	res = mk(rule(1, ActionMarkRead, "golang"))
	require.True(t, res.Read)
	require.False(t, res.Muted)
	require.Zero(t, res.MutedBy)
	// highlight has no stored effect but is reported as matched.
	res = mk(rule(1, ActionHighlight, "golang"))
	require.False(t, res.Read)
	require.False(t, res.Star)
	require.Equal(t, []int64{1}, res.Matched)
	require.True(t, res.Any())
	// Nothing fires.
	res = mk(rule(1, ActionMute, "nomatch"))
	require.False(t, res.Any())
	require.Nil(t, res.Matched)
	// An empty or nil set is inert.
	require.False(t, (*Set)(nil).Evaluate(item).Any())
}

func TestScopeAndEnabled(t *testing.T) {
	folder := NewRule(ScopeFolder, KindText, ActionMute, "go")
	folder.ID, folder.FolderID = 1, 7
	feed := NewRule(ScopeFeed, KindText, ActionStar, "go")
	feed.ID, feed.FeedID = 2, 42
	off := rule(3, ActionMarkRead, "go")
	off.Enabled = false
	global := rule(4, ActionHighlight, "go")
	s, err := NewSet([]Rule{feed, folder, off, global})
	require.NoError(t, err)
	require.Equal(t, 4, s.Len())

	got := func(it Item) []int64 { it.Title = "go"; return s.Evaluate(it).Matched }
	require.Equal(t, []int64{4}, got(Item{FeedID: 1, FolderID: 1}))
	require.Equal(t, []int64{1, 4}, got(Item{FeedID: 1, FolderID: 7}))
	require.Equal(t, []int64{2, 4}, got(Item{FeedID: 42, FolderID: 1}))
	require.Equal(t, []int64{1, 2, 4}, got(Item{FeedID: 42, FolderID: 7}))
}

func TestSetLimits(t *testing.T) {
	var rules []Rule
	for i := 1; i <= MaxRules; i++ {
		rules = append(rules, rule(int64(i), ActionMute, "a"))
	}
	_, err := NewSet(rules)
	require.NoError(t, err)
	_, err = NewSet(append(rules, rule(999, ActionMute, "a")))
	require.Error(t, err)

	// 25 enabled regex rules are fine, the 26th is not; a disabled one does not count.
	var res []Rule
	for i := 1; i <= MaxRegexRules; i++ {
		r := NewRule(ScopeGlobal, KindRegex, ActionMute, "abc")
		r.ID = int64(i)
		res = append(res, r)
	}
	_, err = NewSet(res)
	require.NoError(t, err)
	extra := NewRule(ScopeGlobal, KindRegex, ActionMute, "abc")
	extra.ID = 100
	_, err = NewSet(append(append([]Rule(nil), res...), extra))
	var se *SetError
	require.ErrorAs(t, err, &se)
	require.Equal(t, MaxRegexRules, se.Index)
	require.EqualValues(t, 100, se.RuleID)
	extra.Enabled = false
	_, err = NewSet(append(append([]Rule(nil), res...), extra))
	require.NoError(t, err)

	// 2000 enabled text terms in all.
	var tr []Rule
	for i := 0; i < 40; i++ {
		terms := make([]string, 50)
		for j := range terms {
			terms[j] = "t"
		}
		r := NewRule(ScopeGlobal, KindText, ActionMute, terms...)
		r.ID = int64(i + 1)
		tr = append(tr, r)
	}
	_, err = NewSet(tr)
	require.NoError(t, err)
	over := rule(500, ActionMute, "x")
	_, err = NewSet(append(tr, over))
	require.ErrorAs(t, err, &se)

	// Duplicate ids; a bad rule is reported with its index.
	_, err = NewSet([]Rule{rule(1, ActionMute, "a"), rule(1, ActionStar, "b")})
	require.ErrorAs(t, err, &se)
	require.Equal(t, "id", se.Err.Field)
	_, err = NewSet([]Rule{rule(1, ActionMute, "a"), rule(2, ActionMute, "")})
	require.ErrorAs(t, err, &se)
	require.Equal(t, 1, se.Index)
	require.Equal(t, "terms[0]", se.Err.Field)
}

// Evaluation order must not matter: the same rules in any order give the same result.
func TestDeterministicOrder(t *testing.T) {
	rs := []Rule{rule(5, ActionMute, "go"), rule(2, ActionMute, "go"), rule(8, ActionStar, "zz"), rule(3, ActionMarkRead, "go"), rule(1, ActionHighlight, "go")}
	item := Item{Title: "go"}
	var want Result
	for i := 0; i < 20; i++ {
		perm := append([]Rule(nil), rs...)
		// deterministic shuffle
		for j := len(perm) - 1; j > 0; j-- {
			k := (i*7 + j*13) % (j + 1)
			perm[j], perm[k] = perm[k], perm[j]
		}
		s, err := NewSet(perm)
		require.NoError(t, err)
		got := s.Evaluate(item)
		if i == 0 {
			want = got
			require.EqualValues(t, 2, got.MutedBy)
			require.Equal(t, []int64{1, 2, 3, 5}, got.Matched)
		}
		require.Equal(t, want, got)
	}
}

func TestNormalize(t *testing.T) {
	require.Equal(t, "cafe au lait", normalize("Café  au\tLAIT", true, true))
	require.Equal(t, "café au lait", normalize("Café  au\tLAIT", false, true))
	require.Equal(t, "Cafe au LAIT", normalize("Café au  LAIT", true, false))
	require.Equal(t, "plain", normalize("plain", true, true))
	require.Equal(t, "i", normalize("İ", true, true))
	s := strings.Repeat("é", 5) // 2 bytes each
	require.Equal(t, "éé", truncate(s, 5))
	require.Equal(t, "éé", truncate(s, 4))
	require.Equal(t, "", truncate(s, 1))
	require.Equal(t, s, truncate(s, 100))
}

func TestGiantInputsAreBounded(t *testing.T) {
	huge := strings.Repeat("lorem ipsum dolor ", 1<<20) // ~18 MB
	it := Item{Title: huge, Content: huge, Author: huge, URL: huge, FeedTitle: huge, Categories: []string{huge}}
	rs := []Rule{
		text("needle", "a phrase here").with(func(r *Rule) {
			r.Fields = []Field{FieldTitle, FieldContent, FieldAuthor, FieldURL, FieldFeed, FieldCategory}
		}),
		NewRule(ScopeGlobal, KindRegex, ActionMute, `(a+)+b`, `(x|xx)*y`, `.*.*.*needle`).with(func(r *Rule) { r.Fields = []Field{FieldTitle, FieldContent} }),
	}
	rs[0].ID, rs[1].ID = 1, 2
	s, err := NewSet(rs)
	require.NoError(t, err)
	require.False(t, s.Evaluate(it).Any())
}

func TestPathologicalRegexIsLinear(t *testing.T) {
	// The classic catastrophic patterns finish at once on RE2.
	s, err := NewSet([]Rule{NewRule(ScopeGlobal, KindRegex, ActionMute, `^(a+)+$`, `(a|aa)+$`, `(.*b){20}`).with(func(r *Rule) { r.Fields = []Field{FieldContent} })})
	require.NoError(t, err)
	require.False(t, s.Evaluate(Item{Content: strings.Repeat("a", MaxRegexContentScan-1) + "!"}).Any())
}
