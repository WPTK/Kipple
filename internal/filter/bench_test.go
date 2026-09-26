package filter

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var vocab = func() []string {
	r := rand.New(rand.NewSource(1))
	w := make([]string, 4000)
	for i := range w {
		n := 3 + r.Intn(8)
		b := make([]byte, n)
		for j := range b {
			b[j] = byte('a' + r.Intn(26))
		}
		w[i] = string(b)
	}
	return w
}()

func sentence(r *rand.Rand, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(vocab[r.Intn(len(vocab))])
	}
	return b.String()
}

// benchItems builds n items: ~10 word titles and contentWords words of content.
func benchItems(n, contentWords int) []Item {
	r := rand.New(rand.NewSource(2))
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{
			FeedID: int64(1 + i%40), FolderID: int64(1 + i%5), FeedTitle: sentence(r, 3),
			Title: sentence(r, 10), Author: sentence(r, 2), URL: "https://example.com/" + sentence(r, 4),
			Content:    sentence(r, contentWords),
			Categories: []string{sentence(r, 1), sentence(r, 1)},
		}
	}
	return items
}

// benchRules is a realistic-to-heavy set of 50 rules: keyword lists, phrases, non-word-boundary
// and diacritic terms, scoped rules, inverted rules and 5 regex rules, over several fields.
func benchRules() []Rule {
	r := rand.New(rand.NewSource(3))
	var rs []Rule
	id := int64(0)
	add := func(x Rule) { id++; x.ID = id; rs = append(rs, x) }
	acts := []Action{ActionMute, ActionMarkRead, ActionStar, ActionHighlight}
	// 20 word-list rules of 40 terms, mixed fields.
	for i := 0; i < 20; i++ {
		terms := make([]string, 40)
		for j := range terms {
			terms[j] = vocab[r.Intn(len(vocab))] + "zz" // never present: worst case, every term is checked
		}
		x := NewRule(ScopeGlobal, KindText, acts[i%4], terms...)
		if i%2 == 0 {
			x.Fields = []Field{FieldTitle, FieldContent}
		}
		if i%5 == 0 {
			x.Scope, x.FolderID = ScopeFolder, int64(1+i%5)
		}
		add(x)
	}
	// 15 phrase / substring rules.
	for i := 0; i < 15; i++ {
		x := NewRule(ScopeGlobal, KindText, acts[i%4], vocab[r.Intn(200)]+" "+vocab[r.Intn(200)]+"zz", "café "+vocab[r.Intn(200)])
		x.WholeWord = i%3 != 0
		x.Fields = []Field{FieldTitle, FieldContent}
		add(x)
	}
	// 5 inverted rules on a feed.
	for i := 0; i < 5; i++ {
		x := NewRule(ScopeFeed, KindText, ActionMute, vocab[i])
		x.FeedID, x.Invert = int64(1+i), true
		add(x)
	}
	// 5 regex rules (the spec's ceiling is 25).
	for i := 0; i < 5; i++ {
		x := NewRule(ScopeGlobal, KindRegex, ActionMute, `\b`+vocab[r.Intn(200)]+`\d+\b`, `(?:sponsored|giveaway|coupon)\s+\w+`)
		x.Fields = []Field{FieldTitle, FieldContent}
		add(x)
	}
	// 5 more: URL and author rules.
	for i := 0; i < 5; i++ {
		x := NewRule(ScopeGlobal, KindText, ActionMute, "example.com/"+vocab[i]+"zz")
		x.WholeWord, x.Fields = false, []Field{FieldURL, FieldAuthor}
		add(x)
	}
	return rs
}

func BenchmarkEvaluate50Rules(b *testing.B) {
	s, err := NewSet(benchRules())
	require.NoError(b, err)
	require.Equal(b, 50, s.Len())
	for _, cw := range []int{200, 1000, 5000} {
		items := benchItems(256, cw)
		b.Run(fmt.Sprintf("content%dwords", cw), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				s.Evaluate(items[i%len(items)])
			}
		})
	}
}

// 25 regex rules (the cap) against an item with a full 32 KiB body: the spec budget is under
// 5 ms per item.
func BenchmarkEvaluate25RegexWorstCase(b *testing.B) {
	var rs []Rule
	for i := 0; i < MaxRegexRules; i++ {
		x := NewRule(ScopeGlobal, KindRegex, ActionMute, `(?:sponsored|giveaway|coupon)\s+\w+`, `\b`+vocab[i]+`\d{3}\b`, `[a-z]+@[a-z]+\.com`, `(.*b){3}zzz`, `(?:foo|bar|baz|qux)+\d`)
		x.ID = int64(i + 1)
		x.Fields = []Field{FieldContent} // adding the title would pass MaxRegexCost
		rs = append(rs, x)
	}
	s, err := NewSet(rs)
	require.NoError(b, err)
	items := benchItems(16, 6000) // ~40 KB of content, cut to the 8 KiB regex scan
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Evaluate(items[i%len(items)])
	}
}

// The regression ceilings. Measured on the development machine (numbers in the CHANGELOG and
// design.md); the ceilings are several times looser. The race detector slows regexp by 40x or
// more, so timing is not asserted under -race (CI runs the suite there and everywhere else).
func skipTimingUnderRace(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("timing ceilings are not meaningful under -race")
	}
}

func TestThroughputCeiling10kItems50Rules(t *testing.T) {
	skipTimingUnderRace(t)
	s, err := NewSet(benchRules())
	require.NoError(t, err)
	items := benchItems(10000, 400)
	start := time.Now()
	hits := 0
	for _, it := range items {
		if s.Evaluate(it).Any() {
			hits++
		}
	}
	took := time.Since(start)
	t.Logf("10000 items x 50 rules: %v total, %v per item, %d items matched something", took, took/10000, hits)
	require.Less(t, took, 10*time.Second)
}

func TestRegexWorstCaseCeiling(t *testing.T) {
	skipTimingUnderRace(t)
	var rs []Rule
	for i := 0; i < MaxRegexRules; i++ {
		x := NewRule(ScopeGlobal, KindRegex, ActionMute, `(?:sponsored|giveaway|coupon)\s+\w+`, `\b`+vocab[i]+`\d{3}\b`, `[a-z]+@[a-z]+\.com`, `(.*b){3}zzz`, `(?:foo|bar|baz|qux)+\d`)
		x.ID = int64(i + 1)
		x.Fields = []Field{FieldContent} // adding the title would pass MaxRegexCost
		rs = append(rs, x)
	}
	s, err := NewSet(rs)
	require.NoError(t, err)
	it := benchItems(1, 6000)[0]
	// One item at the cap: the spec's budget is 5 ms; the ceiling here is 10x that.
	const rounds = 20
	start := time.Now()
	for i := 0; i < rounds; i++ {
		s.Evaluate(it)
	}
	per := time.Since(start) / rounds
	t.Logf("25 regex rules x 5 patterns, 8 KiB scan: %v per item", per)
	require.Less(t, per, 50*time.Millisecond)

	// A 250-item first fetch must finish well under the 2 s budget.
	items := benchItems(250, 400)
	start = time.Now()
	for _, x := range items {
		s.Evaluate(x)
	}
	took := time.Since(start)
	t.Logf("250-item first fetch, 25 regex rules: %v", took)
	require.Less(t, took, 2*time.Second)
}

func TestEvaluateIsConcurrencySafe(t *testing.T) {
	s, err := NewSet(benchRules())
	require.NoError(t, err)
	items := benchItems(32, 300)
	want := make([]Result, len(items))
	for i, it := range items {
		want[i] = s.Evaluate(it)
	}
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for n := 0; n < 100; n++ {
				i := n % len(items)
				got := s.Evaluate(items[i])
				if fmt.Sprint(got) != fmt.Sprint(want[i]) {
					t.Errorf("concurrent Evaluate differs for item %d", i)
					return
				}
			}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}
