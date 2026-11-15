package greader

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A body longer than the cap is cut where it ends no tag open: a cut inside a tag or a quoted attribute value moves
// back to the start of that tag, and a cut between tags, or inside text, stays where it is.
func TestTruncateKeepsTagsWhole(t *testing.T) {
	const tag = `<img src="https://x.example/a?b=c" alt="d">`
	for name, tc := range map[string]struct {
		in   string
		max  int
		want string
	}{
		"inside a tag name":        {"<p>ab</p>" + tag, 12, "<p>ab</p>"},
		"inside an attribute":      {"<p>ab</p>" + tag, 9 + 20, "<p>ab</p>"},
		"inside a quoted value":    {"<p>ab</p>" + tag, 9 + 35, "<p>ab</p>"},
		"just before the closer":   {"<p>ab</p>" + tag, 9 + len(tag) - 1, "<p>ab</p>"},
		"just after a tag":         {"<p>ab</p>" + tag, 9 + len(tag), "<p>ab</p>" + tag},
		"after a tag, in text":     {"<p>ab</p>text text", 12, "<p>ab</p>tex"},
		"inside text, no tag":      {strings.Repeat("a", 20), 10, strings.Repeat("a", 10)},
		"inside a rune, in a tag":  {`<a title="€€€">`, 11, ""},
		"inside a rune, after tag": {"<b></b>€€€", 8, "<b></b>"},
		"short enough":             {"<p>ab", 100, "<p>ab"},
		"inside a reference":       {"<p>a&amp;b</p>", 8, "<p>a"},
		"after a reference":        {"<p>a&amp;b</p>", 9, "<p>a&amp;"},
		"in a reference after tag": {"<p>a</p>&lt;bc", 10, "<p>a</p>"},
		"a bare ampersand in text": {"<p>a & b c</p>", 9, "<p>a "},
	} {
		got := truncateUTF8(tc.in, tc.max)
		require.Equal(t, tc.want, got, name)
		require.LessOrEqual(t, len(got), tc.max, name)
	}
}

// What a Reader API client receives for a long article never ends inside a tag.
func TestItemContentEndsBetweenTags(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	filler := strings.Repeat("<p>x</p>", (contentCap-40)/8) // 499,960 bytes
	html := filler + `<img src="https://x.example/c?` + strings.Repeat("a", 200) + `">` + "<p>after</p>"
	require.Less(t, len(filler), contentCap)
	require.Greater(t, len(html), contentCap)
	h.addItem(f, itemSeed{Title: "long", HTML: html})
	last := q[int64](h, "SELECT max(id) FROM items")
	w := h.post(rd+"stream/items/contents", contentsBody(FormatDecimal(last)))
	var env struct{ Items []itemJSON }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	for _, content := range []string{env.Items[0].Summary.Content, env.Items[0].Content.Content} {
		require.LessOrEqual(t, len(content), contentCap)
		require.Equal(t, filler, content)
	}
}
