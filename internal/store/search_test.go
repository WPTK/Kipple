package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"hello world", `"hello" "world"`, true},
		{"  hello   ", `"hello"`, true},
		{"foo*", `"foo"*`, true},
		{"foo**", `"foo"*`, true},
		{`say "hi"`, `"say" """hi"""`, true},
		{"title:foo", `"title:foo"`, true},
		{"NOT foo OR bar", `"NOT" "foo" "OR" "bar"`, true},
		{"NEAR(a b)", `"NEAR(a" "b)"`, true},
		{"^start", `"^start"`, true},
		{"a\x00b", `"a" "b"`, true},
		{"- + ( ) *", "", false},
		{"", "", false},
		{"   ", "", false},
		{"naïve café", `"naïve" "café"`, true},
	}
	for _, c := range cases {
		got, ok := BuildFTSQuery(c.in)
		require.Equal(t, c.ok, ok, c.in)
		require.Equal(t, c.want, got, c.in)
	}
	long, _ := BuildFTSQuery(strings.Repeat("word ", 100))
	require.Equal(t, maxSearchTokens, strings.Count(long, `"word"`))
	tok, _ := BuildFTSQuery(strings.Repeat("x", 500))
	require.Len(t, tok, maxSearchTokenLen+2)
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
	d := Cursor{SortAt: 7, ID: 9}
	back, err = ParseCursor(d.Encode())
	require.NoError(t, err)
	require.Equal(t, d, back)
	for _, bad := range []string{"", "!!", "cg==", "cjF8"} {
		_, err := ParseCursor(bad)
		require.Error(t, err, bad)
	}
}
