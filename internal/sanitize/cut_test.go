package sanitize

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A cut never ends inside a rune, a tag or a character reference, and a string within the limit comes back as it was.
func TestCutHTMLKeepsTagsWhole(t *testing.T) {
	const tag = `<img src="https://x.example/a?b=c" alt="d">`
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
	} {
		got, cut := CutHTML(tc.in, tc.max)
		require.Equal(t, tc.want, got, name)
		require.Equal(t, tc.cut, cut, name)
		require.LessOrEqual(t, len(got), tc.max, name)
	}
}
