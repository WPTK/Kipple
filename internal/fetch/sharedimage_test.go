package fetch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rssWith builds a feed whose items each carry the given content HTML; extra, when set, is added inside every item.
func rssWith(extra string, contents ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>T</title><link>https://a.com/</link>`)
	for i, c := range contents {
		fmt.Fprintf(&b, `<item><title>Post %d</title><link>https://a.com/p%d</link><guid>g%d</guid>%s<description><![CDATA[%s]]></description></item>`, i, i, i, extra, c)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

func leadImages(t *testing.T, doc []byte) []string {
	t.Helper()
	feed, err := ParseFeed(doc, ParseOptions{FeedURL: "https://a.com/feed"})
	require.NoError(t, err)
	var got []string
	for _, it := range feed.Items {
		got = append(got, it.ImageURL)
	}
	return got
}

func TestSharedLeadImageIsNotAnArticlePicture(t *testing.T) {
	logo := `<img src="https://a.com/logo.png" width="150" height="150">`
	got := leadImages(t, rssWith("",
		logo+`<p>one</p>`,
		logo+`<p>two</p><img src="https://a.com/two.jpg" width="100" height="100">`,
		`<img src="https://a.com/three.jpg" width="800" height="500">`+logo,
		logo+`<p>four</p>`,
	))
	// The logo is among three or more items' best images, so it is the site's: each item takes its next one, or none.
	require.Equal(t, []string{"", "https://a.com/two.jpg", "https://a.com/three.jpg", ""}, got)
}

func TestImageSharedByFewerItemsStays(t *testing.T) {
	img := `<img src="https://a.com/x.jpg" width="800" height="500">`
	got := leadImages(t, rssWith("", img, img, `<p>text</p>`))
	require.Equal(t, []string{"https://a.com/x.jpg", "https://a.com/x.jpg", ""}, got)
}

func TestSharedImageFromMediaAndSrcsetFallsBack(t *testing.T) {
	// Every item names the same media:thumbnail; the next best is the content's srcset image.
	got := leadImages(t, rssWith(`<media:thumbnail url="https://a.com/show.png"/>`,
		`<img srcset="https://a.com/s1-300.jpg 300w, https://a.com/s1-900.jpg 900w">`,
		`<img srcset="https://a.com/s2-300.jpg 300w, https://a.com/s2-900.jpg 900w">`,
		`<p>none</p>`,
	))
	require.Equal(t, []string{"https://a.com/s1-900.jpg", "https://a.com/s2-900.jpg", ""}, got)
}

func TestSharedImagesCascade(t *testing.T) {
	// A logo leads three items and the banner behind it then leads the same three: each moves past both, to its own
	// third image or none.
	a, b := `<img src="https://a.com/logo.png" width="900">`, `<img src="https://a.com/banner.png" width="800">`
	got := leadImages(t, rssWith("",
		a+b+`<img src="https://a.com/own1.jpg" width="100">`,
		a+b+`<img src="https://a.com/own2.jpg" width="100">`,
		a+b,
		a+b,
	))
	require.Equal(t, []string{"https://a.com/own1.jpg", "https://a.com/own2.jpg", "", ""}, got)
}

func TestSharedImageIgnoresSizeSuffixSchemeAndTracking(t *testing.T) {
	got := leadImages(t, rssWith("",
		`<img src="https://a.com/up/logo-150x150.png">`,
		`<img src="http://www.a.com/up/logo-300x300.png?ver=2">`,
		`<img src="https://a.com/up/logo.png?utm_source=x">`,
	))
	require.Equal(t, []string{"", "", ""}, got)
	// A query that names the picture is not a tracking parameter.
	got = leadImages(t, rssWith("",
		`<img src="https://a.com/img?id=1">`, `<img src="https://a.com/img?id=2">`, `<img src="https://a.com/img?id=3">`))
	require.Equal(t, []string{"https://a.com/img?id=1", "https://a.com/img?id=2", "https://a.com/img?id=3"}, got)
}

// A thumbnail a post carries further down (a "related posts" crop) must not cost another post its own hero.
func TestOnlyLeadsCountAsShared(t *testing.T) {
	got := leadImages(t, rssWith("",
		`<img src="https://a.com/up/hero-1024x683.jpg" width="1024"><p>text</p>`,
		`<img src="https://a.com/up/b.jpg" width="900"><img src="https://a.com/up/hero-150x150.jpg" width="150">`,
		`<img src="https://a.com/up/c.jpg" width="900"><img src="https://a.com/up/hero-150x150.jpg" width="150">`,
	))
	require.Equal(t, []string{"https://a.com/up/hero-1024x683.jpg", "https://a.com/up/b.jpg", "https://a.com/up/c.jpg"}, got)
}

// A host that names the picture in a q parameter gives three posts three images, not one.
func TestQueryThatNamesThePictureIsKept(t *testing.T) {
	var imgs []string
	for _, id := range []string{"a", "b", "c"} {
		imgs = append(imgs, `<img src="https://t.example/images?q=tbn:`+id+`&amp;usqp=CAU">`)
	}
	got := leadImages(t, rssWith("", imgs...))
	require.Equal(t, []string{"https://t.example/images?q=tbn:a&usqp=CAU", "https://t.example/images?q=tbn:b&usqp=CAU", "https://t.example/images?q=tbn:c&usqp=CAU"}, got)
}

// One post listed three times is one post: it does not make its own picture shared.
func TestRepeatedEntryIsNotThreeHolders(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://a.com/</link>`)
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `<item><title>Same</title><link>https://a.com/same</link><guid>g%d</guid><description><![CDATA[<img src="https://a.com/own.jpg" width="800">]]></description></item>`, i)
	}
	b.WriteString(`</channel></rss>`)
	feed, err := ParseFeed([]byte(b.String()), ParseOptions{FeedURL: "https://a.com/feed", DedupMode: DedupLink})
	require.NoError(t, err)
	require.Len(t, feed.Items, 1)
	require.Equal(t, "https://a.com/own.jpg", feed.Items[0].ImageURL)
}

// A document of many distinct unsized images, in the same order in several items, must cost one pass, not one pass
// per image: choosing among shared images is bounded by imagesPerItem.
func TestSharedImagePickIsBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30000; i++ {
		fmt.Fprintf(&b, `<img src="/i%d.jpg">`, i)
	}
	start := time.Now()
	got := leadImages(t, rssWith("", b.String(), b.String(), b.String()))
	require.Equal(t, []string{"", "", ""}, got)
	require.Less(t, time.Since(start), 10*time.Second)
}
