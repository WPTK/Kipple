package fetch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFeedTitle(t *testing.T) {
	require.Equal(t, "", FeedTitle(" \n\t "))
	require.Equal(t, "Daily News", FeedTitle("\n  Daily\n\t  News  "))
	require.Equal(t, "a < b & c; Q&A &nbsp", FeedTitle("a < b & c; Q&A &nbsp"), "plain text stays as written")
	require.Equal(t, "Tom & Jerry’s …", FeedTitle("Tom &amp; Jerry&#8217;s &hellip;"), "a level of escaping left behind is undone")
	require.Equal(t, "&amp; once", FeedTitle("&amp;amp; once"), "one level only")
	require.Equal(t, "&notit; and &bogus; stay; a;b", FeedTitle("&notit; and &bogus; stay&semi; a;b"),
		"only whole known references: no legacy prefix decoding")
	require.Equal(t, "Bell and null gone", FeedTitle("Bell\x07 and\x00 null\x1f gone"), "control characters dropped")
	require.Equal(t, "a b", FeedTitle("a&#9;b"), "a decoded control counts too")
	require.Equal(t, "Tips &amp;amp; tricks", CleanName("Tips &amp;amp; tricks"), "a name typed in an app keeps its entities")
	require.Equal(t, "x y", CleanName("x\u0085\x01y"), "C1 whitespace is a space, other controls go")
	// Numeric references to no character are dropped, not stored as U+FFFD.
	require.Equal(t, "ab c", FeedTitle("a&#0;b&#xD800; c&#9999999;&#65533;"))
	require.Equal(t, "€ ok", FeedTitle("&#128; ok"), "the HTML mapping of 128 to 159 stays")
	require.Equal(t, "bad bytes", CleanName("bad\xff\xfe bytes\uFFFD"))
}

// Invisible format characters never make or hide a name: they are dropped, a zero-width joiner only
// survives inside a word, and a name of nothing else is blank.
func TestCleanNameInvisibles(t *testing.T) {
	require.Equal(t, "", CleanName("\u200B\u200C\u200D\u2060\uFEFF\u200E\u200F\u202A\u202E\u2066\u2069\u00AD"))
	require.Equal(t, "", CleanName(" \u00A0\u3000\u2028 "), "Unicode whitespace only")
	require.Equal(t, "Zero width", CleanName("Zero\u200B \u200Dwidth\u200D"))
	require.Equal(t, "evil", CleanName("\u202Eevil\u202C"))
	family := "👨\u200D👩\u200D👧"
	require.Equal(t, "Our "+family, CleanName("Our "+family))
}

// The cut never splits a combined character: a combining accent, a zero-width-joined emoji sequence,
// a skin-tone modifier or a flag goes whole or not at all.
func TestCleanNameCutsWholeCharacters(t *testing.T) {
	pad := strings.Repeat("a", MaxTitleRunes-2) // the cut keeps MaxTitleRunes-1 runes: pad plus one more
	cases := map[string]string{
		"combining accent": "éxyz",
		"zwj sequence":     "👨\u200D👩\u200D👧 more",
		"skin tone":        "👋\U0001F3FD more",
		"flag":             "\U0001F1EB\U0001F1F7 more",
		"variation":        "❤\uFE0F more",
	}
	for name, tail := range cases {
		got := []rune(CleanName(pad + tail))
		require.Equal(t, '…', got[len(got)-1], name)
		require.Equal(t, pad, string(got[:len(got)-1]), "%s: the whole cluster straddling the cut goes", name)
	}
	require.Equal(t, strings.Repeat("x", MaxTitleRunes), FeedTitle(strings.Repeat("x", MaxTitleRunes)), "at the limit: untouched")
	cut := []rune(FeedTitle(strings.Repeat("é", MaxTitleRunes+1)))
	require.Len(t, cut, MaxTitleRunes)
	require.Equal(t, '…', cut[len(cut)-1])
	require.Equal(t, strings.Repeat("a", MaxTitleRunes-2)+"…", FeedTitle(strings.Repeat("a", MaxTitleRunes-2)+" b"+strings.Repeat("c", 10)),
		"no space before the ellipsis")
}

// The title a document gives itself reaches Feed.Title decoded and on one line, in every format.
func TestParsedFeedTitle(t *testing.T) {
	for name, doc := range map[string]string{
		"rss":  `<?xml version="1.0"?><rss version="2.0"><channel><title>` + "\n  Tom &amp;amp; Jerry&#8217;s\n\t Blog " + `</title></channel></rss>`,
		"atom": `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title type="html">` + "Tom &amp;amp; Jerry&amp;#8217;s\n Blog" + `</title></feed>`,
		"json": `{"version":"https://jsonfeed.org/version/1.1","title":"Tom & Jerry’s\n Blog","items":[]}`,
	} {
		f, err := ParseFeed([]byte(doc), ParseOptions{FeedURL: "https://ex.com/feed"})
		require.NoError(t, err, name)
		require.Equal(t, "Tom & Jerry’s Blog", f.Title, name)
	}
}
