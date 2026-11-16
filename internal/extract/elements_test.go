package extract

import (
	"net/url"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/sanitize"
)

// A page with more elements than the limit is refused before a tree is built: the tokenizer counts the start tags, so
// a page of a million tiny elements does not hold a copy of each while the parser compares the count.
func TestPageWithTooManyElementsBuildsNoTree(t *testing.T) {
	u, err := url.Parse("https://example.com/a")
	require.NoError(t, err)
	page := "<html><body>" + strings.Repeat("<i></i>", 1_000_000) + "<p>" + strings.Repeat("word ", 300) + "</p></body></html>"

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = readable(strings.NewReader(page), u.String(), u)
	runtime.ReadMemStats(&after)

	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")
	// A tree of a million elements is well over 100 MB; the counted pass allocates the page's copy and little else.
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(48<<20))

	// Just under the limit still reads.
	ok := "<html><body><article>" + strings.Repeat("<span>a</span>", 5000) + "<p>" + strings.Repeat(para, 20) + "</p></article></body></html>"
	res, err := readable(strings.NewReader(ok), u.String(), u)
	require.NoError(t, err)
	require.NotEmpty(t, res.Text)
}

// An article longer than the bound handed to the cleaner is cut between tags: the cleaner is given at most the bound,
// whatever it returns.
func TestExtractedArticleIsCutBeforeTheCleaner(t *testing.T) {
	u, err := url.Parse("https://example.com/a")
	require.NoError(t, err)
	body := strings.Repeat("<p>"+para+"</p>", sanitize.MaxItemHTMLBytes/len("<p>"+para+"</p>")*2)

	given := -1
	orig := cleanArticle
	cleanArticle = func(html string, bases ...string) (string, string) {
		given = len(html)
		return orig(html, bases...)
	}
	t.Cleanup(func() { cleanArticle = orig })

	res, err := readable(strings.NewReader(article(body)), u.String(), u)
	require.NoError(t, err)
	require.LessOrEqual(t, given, sanitize.MaxItemHTMLBytes)
	require.Greater(t, given, sanitize.MaxItemHTMLBytes/2) // cut, not dropped
	require.LessOrEqual(t, len(res.HTML), sanitize.MaxItemHTMLBytes)
}
