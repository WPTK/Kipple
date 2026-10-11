package fetch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rssWith builds a feed whose items each carry the given content HTML.
func rssWith(contents ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://a.com/</link>`)
	for i, c := range contents {
		fmt.Fprintf(&b, `<item><title>Post %d</title><link>https://a.com/p%d</link><guid>g%d</guid><description><![CDATA[%s]]></description></item>`, i, i, i, c)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

func TestSharedLeadImageIsNotAnArticlePicture(t *testing.T) {
	logo := `<img src="https://a.com/logo.png" width="150" height="150">`
	feed, err := ParseFeed(rssWith(
		logo+`<p>one</p>`,
		logo+`<p>two</p><img src="https://a.com/two.jpg" width="100" height="100">`,
		`<img src="https://a.com/three.jpg" width="800" height="500">`+logo,
		logo+`<p>four</p>`,
	), ParseOptions{FeedURL: "https://a.com/feed"})
	require.NoError(t, err)
	var got []string
	for _, it := range feed.Items {
		got = append(got, it.ImageURL)
	}
	// The logo leads three items, so it is the site's: those items take their next image, or none.
	require.Equal(t, []string{"", "https://a.com/two.jpg", "https://a.com/three.jpg", ""}, got)
}

func TestLeadImageUsedByFewerItemsStays(t *testing.T) {
	img := `<img src="https://a.com/x.jpg" width="800" height="500">`
	feed, err := ParseFeed(rssWith(img, img, `<p>text</p>`), ParseOptions{FeedURL: "https://a.com/feed"})
	require.NoError(t, err)
	require.Equal(t, "https://a.com/x.jpg", feed.Items[0].ImageURL)
	require.Equal(t, "https://a.com/x.jpg", feed.Items[1].ImageURL)
}
