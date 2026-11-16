package sanitize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A cut never ends inside a rune, a tag, a comment or a character reference, and a string within the limit comes back
// as it was.
func TestCutHTMLKeepsTagsWhole(t *testing.T) {
	const tag = `<img src="https://x.example/a?b=c" alt="d">`
	const quoted = `<a title="x>y" href="z">`
	for name, tc := range map[string]struct {
		in   string
		max  int
		want string
		cut  bool
	}{
		"inside a tag name":        {"<p>ab</p>" + tag, 12, "<p>ab</p>", true},
		"inside a quoted value":    {"<p>ab</p>" + tag, 9 + 35, "<p>ab</p>", true},
		"just after a tag":         {"<p>ab</p>" + tag, 9 + len(tag), "<p>ab</p>" + tag, false},
		"after a tag, in text":     {"<p>ab</p>text text", 12, "<p>ab</p>tex", true},
		"inside a rune, in a tag":  {`<a title="€€€">`, 11, "", true},
		"inside a rune, after tag": {"<b></b>€€€", 8, "<b></b>", true},
		"short enough":             {"<p>ab", 100, "<p>ab", false},
		"inside a reference":       {"<p>a&amp;b</p>", 8, "<p>a", true},
		"after a reference":        {"<p>a&amp;b</p>", 9, "<p>a&amp;", true},
		"inside a numeric ref":     {"<p>a&#x1F600;b</p>", 9, "<p>a", true},
		"after a quoted >":         {"<p>ab</p>" + quoted + "text", 9 + 15, "<p>ab</p>", true},
		"inside an unquoted tag":   {"<p>ab</p><br class=a>text", 9 + 8, "<p>ab</p>", true},
		"inside a comment":         {"<p>a</p><!-- x > y --><p>b</p>", 9 + 12, "<p>a</p>", true},
		"after a comment":          {"<p>a</p><!-- x > y --><p>b</p>", 9 + 14, "<p>a</p><!-- x > y -->", true},
		"inside a doctype":         {"<p>a</p><!DOCTYPE html>x", 9 + 6, "<p>a</p>", true},
		"an unterminated tag":      {"<p>ab</p><a href=\"x>y\" text text text", 9 + 16, "<p>ab</p>", true},
		"a literal < is text":      {"<p>1 < 2 and 3 < 4</p>", 14, "<p>1 < 2 and 3", true},
		"a literal & is text":      {"<p>AT & T and more</p>", 11, "<p>AT & T a", true},
		"a bare & before the cut":  {"<p>a & b c d e f</p>", 12, "<p>a & b c d", true},
	} {
		got, cut := CutHTML(tc.in, tc.max)
		require.Equal(t, tc.want, got, name)
		require.Equal(t, tc.cut, cut, name)
		require.LessOrEqual(t, len(got), tc.max, name)
	}
}

// Text full of '<' and '&' that do not begin markup keeps nearly all of what fits: the cut moves back by at most one
// reference, not to the last of them.
func TestCutHTMLDoesNotDropTextAfterLiteralSymbols(t *testing.T) {
	in := "<p>" + strings.Repeat("a < b & c ", 5000) + "</p>"
	const limit = 20000
	got, cut := CutHTML(in, limit)
	require.True(t, cut)
	require.GreaterOrEqual(t, len(got), limit-maxRefBytes)

	// A '>' inside a quoted value does not end the tag, so no half tag is left.
	in = "<p>" + strings.Repeat(`<a title="1>2" href="/x">link</a> `, 400) + "</p>"
	for n := 1000; n < 1100; n++ {
		got, _ := CutHTML(in, n)
		_, inside := markupAt(got, len(got))
		require.False(t, inside, "cut at %d ends inside markup: %q", n, got[max(0, len(got)-20):])
	}
}
