package fetch

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	"github.com/stretchr/testify/require"
)

// Edges of the parser's rules, found by mutation testing: each test fails if a limit moves by one or a check flips.

// The one-day cap on a future date counts forward from now: a dated entry from the future is newer than an undated
// one (which takes the time of the fetch).
func TestNewestEntriesCapIsInTheFuture(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *gofeed.Item { t := now.Add(d); return &gofeed.Item{PublishedParsed: &t} }
	require.Equal(t, []int{1}, newestEntries([]*gofeed.Item{{}, at(12 * time.Hour)}, 1, now))
	require.Equal(t, []int{1}, newestEntries([]*gofeed.Item{{}, at(48 * time.Hour)}, 1, now))
	// Both capped: the later real date wins, in either document order; a full tie goes to the earlier entry.
	require.Equal(t, []int{0}, newestEntries([]*gofeed.Item{at(72 * time.Hour), at(48 * time.Hour)}, 1, now))
	require.Equal(t, []int{1}, newestEntries([]*gofeed.Item{at(48 * time.Hour), at(72 * time.Hour)}, 1, now))
	require.Equal(t, []int{0, 1}, newestEntries([]*gofeed.Item{at(time.Hour), at(time.Hour), at(time.Hour)}, 2, now))
	require.Equal(t, []int{1, 2}, newestEntries([]*gofeed.Item{at(-time.Hour), at(time.Hour), at(time.Hour)}, 2, now))
}

// A document may nest exactly MaxNesting elements; one more is refused. The scan stops when the root closes.
func TestCheckNestingEdges(t *testing.T) {
	doc := func(depth int, after string) []byte {
		return []byte(`<?xml version="1.0"?><rss version="2.0">` + strings.Repeat("<a>", depth-1) + strings.Repeat("</a>", depth-1) + "</rss>" + after)
	}
	require.NoError(t, checkNesting(doc(MaxNesting, "")))
	require.Error(t, checkNesting(doc(MaxNesting+1, "")))
	require.NoError(t, checkNesting(doc(2, strings.Repeat("<b>", MaxNesting+1))), "nothing after the root is read")
}

// The control-byte filter never answers a read with nothing: it reads on past bytes it drops.
func TestXMLFilterSkipsDroppedBytes(t *testing.T) {
	f := xmlFilter{iotest.OneByteReader(bytes.NewReader([]byte("\x01\x02a")))}
	p := make([]byte, 4)
	n, err := f.Read(p)
	require.NoError(t, err)
	require.Equal(t, "a", string(p[:n]))
}

// Dates from 1970 on are kept; earlier ones are treated as none.
func TestValidTimeEpochEdge(t *testing.T) {
	in := time.Date(1970, 1, 1, 0, 0, 1, 0, time.UTC)
	require.NotNil(t, validTime(&in))
	out := time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC)
	require.Nil(t, validTime(&out))
}

// The author is the first name that is not blank: the main author, then the list, then Dublin Core.
func TestItemAuthorSkipsBlankNames(t *testing.T) {
	require.Equal(t, "A", itemAuthor(&gofeed.Item{Author: &gofeed.Person{Name: " A "}, Authors: []*gofeed.Person{{Name: "B"}}}))
	require.Equal(t, "B", itemAuthor(&gofeed.Item{Author: &gofeed.Person{Name: " "}, Authors: []*gofeed.Person{nil, {Name: " "}, {Name: "B"}}}))
	require.Equal(t, "B", itemAuthor(&gofeed.Item{Authors: []*gofeed.Person{{Name: "B"}}}))
}

// media:content counts as an image by its type or its medium, and only with a usable URL; otherwise the thumbnail.
func TestMediaImageOrder(t *testing.T) {
	m := func(content ...map[string]string) map[string][]ext.Extension {
		out := map[string][]ext.Extension{"thumbnail": {{Attrs: map[string]string{"url": "https://x.example/thumb.jpg"}}}}
		for _, a := range content {
			out["content"] = append(out["content"], ext.Extension{Attrs: a})
		}
		return out
	}
	require.Equal(t, "https://x.example/c.jpg", mediaImage(m(map[string]string{"type": "IMAGE/jpeg", "url": "https://x.example/c.jpg"}), nil))
	require.Equal(t, "https://x.example/c.jpg", mediaImage(m(map[string]string{"medium": "image", "url": "https://x.example/c.jpg"}), nil))
	require.Equal(t, "https://x.example/thumb.jpg", mediaImage(m(map[string]string{"type": "video/mp4", "url": "https://x.example/c.mp4"}), nil))
	require.Equal(t, "https://x.example/thumb.jpg", mediaImage(m(map[string]string{"type": "image/jpeg", "url": ""}), nil))
}

// An item keeps at most imagesPerItem images.
func TestPickImagesLimit(t *testing.T) {
	var html strings.Builder
	for i := range imagesPerItem + 2 {
		fmt.Fprintf(&html, `<p><img src="https://x.example/%d.jpg" width="600" height="400"></p>`, i)
	}
	got := pickImages(&gofeed.Item{}, html.String(), nil)
	require.Len(t, got, imagesPerItem)
	require.Equal(t, "https://x.example/0.jpg", got[0])
	// The limit holds across the sources, not only within the content's own images.
	got = pickImages(&gofeed.Item{Image: &gofeed.Image{URL: "https://x.example/lead.jpg"}}, html.String(), nil)
	require.Len(t, got, imagesPerItem)
	require.Equal(t, "https://x.example/lead.jpg", got[0])
}

// An entry with only an image (no text, link, guid or enclosure) is kept, keyed on its image.
func TestImageOnlyEntryIsKept(t *testing.T) {
	f, err := ParseFeed([]byte(`<?xml version="1.0"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>Photos</title>
<item><media:thumbnail url="https://x.example/p.jpg"/></item></channel></rss>`), ParseOptions{})
	require.NoError(t, err)
	require.Len(t, f.Items, 1)
	require.Equal(t, "https://x.example/p.jpg", f.Items[0].GUID)
}

// A numeric reference to no character is dropped, at each edge of the ranges that are characters.
func TestDecodeReferenceEdges(t *testing.T) {
	for ref, want := range map[string]string{
		"&#0;": "", "&#1;": "\x01", "&#xD7FF;": "퟿", "&#xD800;": "", "&#xDFFF;": "", "&#xE000;": "",
		"&#x10FFFF;": "\U0010FFFF", "&#x110000;": "", "&#9999999;": "", "&#xFFFD;": "", "&#x2019;": "’",
		"&amp;": "&", "&notit;": "&notit;",
	} {
		require.Equal(t, want, decodeReference(ref), ref)
	}
}

// The character ranges of the name rules, each one in at both ends and out one step past them.
func TestNameRuleRanges(t *testing.T) {
	for r, want := range map[rune]bool{0xDFFFF: false, 0xE0000: true, 0xE007F: true, 0xE0080: false} {
		require.Equal(t, want, isTag(r), "isTag %U", r)
	}
	for r, want := range map[rune]bool{0xFDFF: false, 0xFE00: true, 0xFE0F: true, 0xFE10: false, 0xE00FF: false, 0xE0100: true, 0xE01EF: true, 0xE01F0: false} {
		require.Equal(t, want, needsBase(r), "needsBase %U", r)
	}
	// A cut before a character that extends the one before it moves back; before any other it stays.
	for r, extends := range map[rune]bool{
		0xFDFF: false, 0xFE00: true, 0xFE0F: true, 0xFE10: false,
		0x1F3FA: false, 0x1F3FB: true, 0x1F3FF: true, 0x1F400: false,
		0xE001F: false, 0xE0020: true, 0xE007F: true, 0xE0080: false,
	} {
		want := 2
		if extends {
			want = 1
		}
		require.Equal(t, want, clusterStart([]rune{'a', 'b', r}, 2), "%U", r)
	}
}

// A cut never splits a flag, counting regional indicators back to the very first character, at both ends of their
// range; a run of marks is cut at its base, never before the first character.
func TestClusterStartFlagsAndMarks(t *testing.T) {
	flags := []rune("\U0001F1FA\U0001F1F8\U0001F1E6\U0001F1FF") // US, AZ
	require.Equal(t, 0, clusterStart(flags, 1))
	require.Equal(t, 2, clusterStart(flags, 2))
	require.Equal(t, 2, clusterStart(flags, 3))
	require.Equal(t, 1, clusterStart([]rune{'a', 0x1F1E5, 0x1F1E6}, 1), "U+1F1E5 is not a regional indicator")
	require.Equal(t, 2, clusterStart([]rune{'a', 0x1F1FF, 0x1F200}, 2), "U+1F200 is not one either")

	marks := "a" + strings.Repeat("́", 300)
	require.NotPanics(t, func() { require.Equal(t, MaxTitleRunes, len([]rune(CleanName(marks)))) })
}

// Categories: blanks dropped, each cut to MaxCategoryRunes, at most MaxCategories.
func TestItemCategoriesEdges(t *testing.T) {
	require.Equal(t, []string{"a", "b"}, itemCategories([]string{" ", "a", "", "b"}))
	long := strings.Repeat("é", MaxCategoryRunes+1)
	require.Equal(t, []string{strings.Repeat("é", MaxCategoryRunes)}, itemCategories([]string{long}))
	var many []string
	for i := range MaxCategories + 5 {
		many = append(many, fmt.Sprintf("c%d", i))
	}
	got := itemCategories(many)
	require.Len(t, got, MaxCategories)
	require.Equal(t, "c19", got[MaxCategories-1])
}

// A negative or zero ttl is no hint; a positive one is kept.
func TestTTLEdges(t *testing.T) {
	for ttl, want := range map[string]int{"-5": 0, "0": 0, "1": 1} {
		f, err := ParseFeed([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>t</title><ttl>`+ttl+`</ttl><item><title>i</title></item></channel></rss>`), ParseOptions{})
		require.NoError(t, err)
		require.Equal(t, want, f.TTLMinutes, ttl)
	}
}
