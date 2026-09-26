package filter

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// Seeds live in testdata/fuzz/<Target>/ and run as ordinary tests. Run a target for real with
//
//	go test ./internal/filter -run '^$' -fuzz FuzzTextRule -fuzztime 30s

func FuzzRegexRule(f *testing.F) {
	for _, s := range []string{`a+b`, `(a+)+$`, `^\[ad\]`, `(?i)news`, `a*`, `(a)\1`, `[[:alpha:]]{3}`, `\p{Han}+`, `(?s).*x`, `a{1000}`, `\C`, `(`, ``} {
		f.Add(s, "aaaa [AD] news 中文 aaaab", false)
	}
	f.Fuzz(func(t *testing.T, pattern, hay string, cs bool) {
		r := NewRule(ScopeGlobal, KindRegex, ActionMute, pattern)
		r.CaseSensitive = cs
		r.Fields = []Field{FieldTitle, FieldContent}
		c, err := CompileRule(r)
		if err != nil {
			if _, ok := err.(*Error); !ok {
				t.Fatalf("error is %T, want *Error", err)
			}
			return
		}
		for _, p := range c.res {
			if p.re.MatchString("") {
				t.Fatalf("accepted an empty-matching pattern %q", pattern)
			}
		}
		c.Fires(Item{Title: hay, Content: hay})
		s, err := NewSet([]Rule{r})
		if err != nil {
			t.Fatal(err)
		}
		s.Evaluate(Item{Title: hay, Content: hay})
	})
}

// naiveTextFires is the reference: normalize both sides and scan with containsTerm.
func naiveTextFires(r Rule, hay string) bool {
	lower := !r.CaseSensitive
	h := normalize(truncate(hay, MaxFieldScan), r.FoldDiacritics, lower)
	for _, t := range r.Terms {
		nt := normalize(t, r.FoldDiacritics, lower)
		if nt = trimSpaceASCII(nt); nt != "" && containsTerm(h, nt, r.WholeWord) {
			return true
		}
	}
	return false
}

func trimSpaceASCII(s string) string {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

// The word-set fast path and the prefilter must agree with the plain substring scan.
func FuzzTextRule(f *testing.F) {
	f.Add("go", "Let's Go! going gone", true, true, false)
	f.Add("breaking news", "BREAKING\n news: x", true, true, false)
	f.Add("café", "the CAFE", true, true, false)
	f.Add("中国", "我爱中国人", true, true, false)
	f.Add("c++", "abc++ c++11", true, true, false)
	f.Add("iphone", "新iPhone发布", true, false, true)
	f.Add("a b", "a b", false, true, true)
	f.Add("İ", "i̇stanbul", true, true, false)
	f.Fuzz(func(t *testing.T, term, hay string, whole, fold, cs bool) {
		r := NewRule(ScopeGlobal, KindText, ActionMute, term, term+" x")
		r.WholeWord, r.FoldDiacritics, r.CaseSensitive = whole, fold, cs
		c, err := CompileRule(r)
		if err != nil {
			return
		}
		if got, want := c.Fires(Item{Title: hay}), naiveTextFires(r, hay); got != want {
			t.Fatalf("term %q hay %q whole=%v fold=%v cs=%v: fast path %v, reference %v", term, hay, whole, fold, cs, got, want)
		}
		r.Invert = true
		ci, err := CompileRule(r)
		if err != nil {
			t.Fatal(err)
		}
		if ci.Fires(Item{Title: hay}) == c.Fires(Item{Title: hay}) {
			t.Fatal("invert must flip the outcome")
		}
	})
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{"", "Café  au\tlait", "İstanbul", "ﬁle", "́x", "a b", "中文", "\xff\xfe", "AB\r\n cd"} {
		f.Add(s, true, true, 7)
	}
	f.Fuzz(func(t *testing.T, s string, fold, lower bool, n int) {
		out := normalize(s, fold, lower)
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("normalize(%q) = %q is not valid UTF-8", s, out)
		}
		if n < 0 {
			n = -n
		}
		n %= 64
		tr := truncate(s, n)
		if len(tr) > n || len(tr) > len(s) || s[:len(tr)] != tr {
			t.Fatalf("truncate(%q, %d) = %q", s, n, tr)
		}
		if utf8.ValidString(s) && !utf8.ValidString(tr) {
			t.Fatalf("truncate split a rune: %q", tr)
		}
	})
}

// The regex prefilter may only ever say "cannot match": whenever it does, the regex must agree.
func FuzzRegexPrefilter(f *testing.F) {
	for _, s := range []string{`foo|bar`, `(?i:sponsored)\s+x`, `k+s`, `\bnews\d`, `a(b|c)d`, `(?:ab){2,}`, `x?y`, `é+t`, `[a-c]foo`} {
		f.Add(s, "FOO bar ſponsored X K ſ KELVINK news1 abd ab ab ét", false)
	}
	f.Fuzz(func(t *testing.T, pattern, hay string, cs bool) {
		p, e := compileRegex("terms[0]", pattern, cs)
		if e != nil {
			return
		}
		sc := new(scratch)
		sc.reset(&Item{Title: hay})
		s := sc.rawText(FieldTitle, true)
		if !sc.couldMatch(&p, FieldTitle, s) && p.re.MatchString(s) {
			t.Fatalf("prefilter %+v rejected %q but %q matches it", p.req, s, pattern)
		}
	})
}

func TestPrefilterExamples(t *testing.T) {
	cases := []struct {
		pattern, hay string
		cs, could    bool
	}{
		{`sponsored`, "A SPONSORED post", false, true},
		{`sponsored`, "A post", false, false},
		{`sponsored`, "A SPONSORED post", true, false},
		{`ſponsored`, "sponsored", false, true}, // long s folds to s
		{`sponsored`, "ſponſored", false, true}, // and back
		{`kelvin`, "KELVIN", false, true},       // Kelvin sign folds to k
		{`foo|bar`, "xx BAR", false, true},      // alternation: any branch
		{`foo|bar`, "xx baz", false, false},
		{`\bnews\d+`, "no digits here", false, false}, // literal inside a concat
		{`a+`, "aaa", false, true},
		{`(?:x|y)z*`, "q", false, true}, // no literal requirement: always runs the regex
	}
	for _, c := range cases {
		p, e := compileRegex("terms[0]", c.pattern, c.cs)
		require.Nil(t, e, c.pattern)
		sc := new(scratch)
		sc.reset(&Item{Title: c.hay})
		require.Equal(t, c.could, sc.couldMatch(&p, FieldTitle, sc.rawText(FieldTitle, true)), "%q on %q", c.pattern, c.hay)
	}
}
