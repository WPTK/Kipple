package fetch

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	gjson "github.com/mmcdole/gofeed/json"
	grss "github.com/mmcdole/gofeed/rss"

	"github.com/WPTK/kipple/internal/sanitize"
)

// Enclosure is a media attachment as stored in item_content.enclosures_json.
type Enclosure struct {
	URL    string `json:"url"`
	Type   string `json:"type,omitempty"`
	Length string `json:"length,omitempty"`
}

// Item is one normalized feed entry, ready for CommitFetch (design §4.8).
type Item struct {
	UID         string      `json:"uid"`
	GUID        string      `json:"guid,omitempty"`      // as written, untrimmed
	RawLink     string      `json:"raw_link,omitempty"`  // the document's link, before absolutizing and origLink
	URL         string      `json:"url"`                 // absolute; feedburner:origLink wins; "" when none
	LinkHash    string      `json:"link_hash,omitempty"` // h(URL), for GUID-migration detection
	Title       string      `json:"title"`
	Author      string      `json:"author,omitempty"`
	ContentHTML string      `json:"content_html"`
	ContentText string      `json:"content_text"`
	ImageURL    string      `json:"image_url,omitempty"` // absolute lead image
	Published   *time.Time  `json:"published,omitempty"` // PublishedParsed ?? UpdatedParsed; nil = store uses id/1e6
	Updated     *time.Time  `json:"updated,omitempty"`   // item's own updated date, if any
	Enclosures  []Enclosure `json:"enclosures,omitempty"`
	WordCount   int         `json:"-"` // words in ContentText; not in the golden output
	// Categories are the entry's category/tag labels (at most MaxCategories of MaxCategoryRunes),
	// stored at ingest so category filter rules can match. Not in the golden output.
	Categories  []string `json:"-"`
	ContentHash string   `json:"content_hash"`
	TextHash    string   `json:"text_hash"`
}

// Feed is the normalized parse result.
type Feed struct {
	Format      string     `json:"format"` // rss | atom | json
	Title       string     `json:"title"`
	SiteURL     string     `json:"site_url,omitempty"`
	Description string     `json:"description,omitempty"`
	Updated     *time.Time `json:"updated,omitempty"`
	Items       []Item     `json:"items"`
	Notes       []string   `json:"notes,omitempty"`
	TTLMinutes  int        `json:"ttl_minutes,omitempty"` // RSS <ttl>, publisher cache hint (design §4.6)
	Charset     string     `json:"charset"`
	BodyHash    string     `json:"-"` // varies with nothing here; goldens stay stable without it
}

// ContentFunc turns raw item HTML into the stored form. base is the resolved
// item URL. The default is the identity plus PlainText; the content pipeline
// supplies the bluemonday policy and absolutizer later.
type ContentFunc func(rawHTML string, bases ...string) (html, text string)

// ParseOptions configure ParseFeed.
type ParseOptions struct {
	FeedURL     string      // final feed URL after redirects; base of the chain
	HTTPCharset string      // charset parameter of Content-Type, if any
	DedupMode   string      // auto (default) | link | link_title
	Content     ContentFunc // optional sanitizer hook
}

// ParseFeed decodes body to UTF-8, parses it with gofeed and returns the
// normalized feed. gofeed is pinned at v1.4.2 in go.mod. Like DecodeBody it may
// modify body (only the encoding value of its XML declaration).
func ParseFeed(body []byte, opt ParseOptions) (*Feed, error) {
	return ParseDecoded(decodeBody(body, opt.HTTPCharset), opt)
}

// decodeBody is DecodeBody; a variable so a test can count the decodes.
var decodeBody = DecodeBody

// ParseDecoded is ParseFeed for a body DecodeBody has already converted: the
// fetcher decodes once for the body-hash check and parses the same bytes
// (opt.HTTPCharset is not used).
func ParseDecoded(dec Decoded, opt ParseOptions) (*Feed, error) {
	fp := gofeed.NewParser()
	fp.KeepOriginalFeed = true
	// The default RSS translator runs a full HTML parse per item to find an
	// image; we do our own pick (with tracker filtering and the base chain).
	fp.RSSTranslator = &gofeed.DefaultRSSTranslator{DisableContentImageScan: true}
	gf, err := fp.Parse(bytes.NewReader(dec.Body))
	if err != nil {
		return nil, err
	}

	content := opt.Content
	if content == nil {
		content = func(raw string, _ ...string) (string, string) { return raw, sanitize.PlainText(raw) }
	}

	out := &Feed{
		Format:      formatName(gf.FeedType),
		Title:       strings.TrimSpace(gf.Title),
		SiteURL:     sanitize.ResolveURL(gf.Link, opt.FeedURL),
		Description: strings.TrimSpace(gf.Description),
		Charset:     dec.Source,
		BodyHash:    dec.BodyHash,
	}
	if t := firstTime(gf.UpdatedParsed, gf.PublishedParsed); t != nil {
		out.Updated = t
	}

	if rf, ok := gf.OriginalFeed().(*grss.Feed); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(rf.TTL)); err == nil && n > 0 {
			out.TTLMinutes = n
		}
	}

	var jsonItems []*gjson.Item
	if jf, ok := gf.OriginalFeed().(*gjson.Feed); ok {
		jsonItems = jf.Items
	}

	items := make([]Item, 0, len(gf.Items))
	skipped := 0
	for i, gi := range gf.Items {
		it, ok := convertItem(i, gi, out.SiteURL, out.Title, opt, content, jsonItems)
		if !ok {
			skipped++
			continue
		}
		items = append(items, it)
	}

	mode := opt.DedupMode
	if mode == "" {
		mode = DedupAuto
	}
	items, notes := AssignUIDs(items, mode)
	for i := range items {
		it := &items[i]
		it.WordCount = sanitize.WordCount(it.ContentText)
		it.ContentHash = ContentHash(it.Title, it.URL, it.Author, it.ContentHTML)
		it.TextHash = TextHash(it.Title, it.ContentText)
	}
	out.Items = items
	out.Notes = notes
	if skipped > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("skipped_malformed_items: %d/%d", skipped, len(gf.Items)))
	}
	return out, nil
}

// convertItem turns one gofeed item into an Item. ok is false for an entry that
// cannot be used: a nil entry, one with no title, link, guid, text, enclosure or
// image at all (every such entry would share one uid), or one whose conversion panics on
// hostile input. One bad entry is dropped and counted, never fatal to the
// fetch (design §13 item 9).
func convertItem(i int, gi *gofeed.Item, siteURL, feedTitle string, opt ParseOptions, content func(string, ...string) (string, string), jsonItems []*gjson.Item) (it Item, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			it, ok = Item{}, false
		}
	}()
	if gi == nil {
		return Item{}, false
	}
	it = Item{
		GUID:    gi.GUID,
		RawLink: strings.TrimSpace(gi.Link),
		Title:   strings.TrimSpace(gi.Title),
		Author:  itemAuthor(gi),
	}
	var bases []string
	it.URL = sanitize.ResolveURL(origLink(gi), opt.FeedURL)
	if it.URL == "" {
		it.URL = sanitize.ResolveURL(it.RawLink, opt.FeedURL)
	}
	if it.URL != "" {
		it.LinkHash = H(it.URL)
		bases = []string{it.URL, siteURL, opt.FeedURL}
	} else {
		bases = []string{siteURL, opt.FeedURL}
	}

	raw := gi.Content
	if raw == "" {
		raw = gi.Description
	}
	// JSON Feed content_text is plain text, not markup.
	if i < len(jsonItems) && jsonItems[i] != nil && jsonItems[i].ContentHTML == "" && jsonItems[i].ContentText != "" {
		raw = "<p>" + strings.ReplaceAll(stdhtml.EscapeString(jsonItems[i].ContentText), "\n", "<br>") + "</p>"
	}
	it.ContentHTML, it.ContentText = content(raw, bases...)

	it.ImageURL = pickImage(gi, raw, bases)
	it.Categories = itemCategories(gi.Categories)
	it.Published = firstTime(gi.PublishedParsed, gi.UpdatedParsed)
	it.Updated = validTime(gi.UpdatedParsed)
	// One entry per resolved URL: feeds that list the same file twice (an RSS
	// enclosure plus media:content) would otherwise show two players.
	seenEnc := map[string]bool{}
	for _, e := range gi.Enclosures {
		if u := sanitize.ResolveURL(e.URL, bases...); u != "" && !seenEnc[u] {
			seenEnc[u] = true
			it.Enclosures = append(it.Enclosures, Enclosure{URL: u, Type: e.Type, Length: e.Length})
		}
	}
	if it.GUID == "" && it.RawLink == "" && it.Title == "" && it.ContentText == "" {
		// An enclosure-only entry (a podcast or photo feed with no text) is real
		// content: keep it, named after its file, or the feed when there is none.
		// Its first media URL doubles as the guid so two such entries get their
		// own uids instead of sharing the empty-text one.
		media := ""
		if len(it.Enclosures) > 0 {
			media = it.Enclosures[0].URL
		} else {
			media = it.ImageURL
		}
		if media == "" {
			return Item{}, false
		}
		it.GUID = media
		it.Title = mediaTitle(media, feedTitle)
	}
	return it, true
}

// mediaTitle names an enclosure-only entry: the file name of its media URL
// (percent-decoded), else the feed title, else "Untitled".
func mediaTitle(mediaURL, feedTitle string) string {
	if u, err := url.Parse(mediaURL); err == nil {
		if name := path.Base(u.Path); name != "" && name != "." && name != "/" {
			return name
		}
	}
	if feedTitle != "" {
		return feedTitle
	}
	return "Untitled"
}

func formatName(t string) string {
	switch t {
	case "atom":
		return "atom"
	case "json":
		return "json"
	default:
		return "rss" // rss 0.9x/1.0/2.0
	}
}

func validTime(t *time.Time) *time.Time {
	if t == nil || t.IsZero() || t.Year() < 1970 {
		return nil
	}
	u := t.UTC()
	return &u
}

// firstTime is the date fallback chain: the first valid of the arguments.
func firstTime(ts ...*time.Time) *time.Time {
	for _, t := range ts {
		if v := validTime(t); v != nil {
			return v
		}
	}
	return nil
}

func itemAuthor(gi *gofeed.Item) string {
	if gi.Author != nil && strings.TrimSpace(gi.Author.Name) != "" {
		return strings.TrimSpace(gi.Author.Name)
	}
	for _, a := range gi.Authors {
		if a != nil && strings.TrimSpace(a.Name) != "" {
			return strings.TrimSpace(a.Name)
		}
	}
	if gi.DublinCoreExt != nil {
		for _, c := range gi.DublinCoreExt.Creator {
			if c = strings.TrimSpace(c); c != "" {
				return c
			}
		}
	}
	return ""
}

// origLink returns feedburner:origLink, the real article URL behind a
// feedproxy.google.com tracking redirect, or "".
func origLink(gi *gofeed.Item) string {
	for _, e := range gi.Extensions["feedburner"]["origLink"] {
		if v := strings.TrimSpace(e.Value); v != "" {
			return v
		}
	}
	return ""
}

// mediaImage looks for an image in a media-namespace element map, in a fixed
// order (map iteration is random and the output must be deterministic):
// media:content with an image type/medium, media:thumbnail, then inside
// media:group.
func mediaImage(m map[string][]ext.Extension, bases []string) string {
	for _, c := range m["content"] {
		if strings.HasPrefix(strings.ToLower(c.Attrs["type"]), "image/") || c.Attrs["medium"] == "image" {
			if u := sanitize.ResolveURL(c.Attrs["url"], bases...); u != "" {
				return u
			}
		}
	}
	for _, t := range m["thumbnail"] {
		if u := sanitize.ResolveURL(t.Attrs["url"], bases...); u != "" {
			return u
		}
	}
	for _, g := range m["group"] {
		if u := mediaImage(g.Children, bases); u != "" {
			return u
		}
	}
	return ""
}

// pickImage chooses the lead image, in order: gofeed's explicit image
// (itunes:image, media:content image, image enclosure, JSON Feed image or
// banner_image), media:thumbnail / media:content anywhere in the media
// namespace (Atom, media:group), an image enclosure, then the first real <img>
// in the content. The result is absolute or "".
func pickImage(gi *gofeed.Item, rawHTML string, bases []string) string {
	if gi.Image != nil {
		if u := sanitize.ResolveURL(gi.Image.URL, bases...); u != "" {
			return u
		}
	}
	if u := mediaImage(gi.Extensions["media"], bases); u != "" {
		return u
	}
	for _, e := range gi.Enclosures {
		if strings.HasPrefix(strings.ToLower(e.Type), "image/") {
			if u := sanitize.ResolveURL(e.URL, bases...); u != "" {
				return u
			}
		}
	}
	return sanitize.LeadImage(rawHTML, bases...)
}

// Category limits (backend additions 1.8): a feed can list dozens of tags.
const (
	MaxCategories    = 20
	MaxCategoryRunes = 100
)

// itemCategories trims, drops blanks and repeats (case-insensitively) and caps the list.
func itemCategories(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.Join(strings.Fields(c), " ")
		if c == "" {
			continue
		}
		if r := []rune(c); len(r) > MaxCategoryRunes {
			c = string(r[:MaxCategoryRunes])
		}
		k := strings.ToLower(c)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
		if len(out) == MaxCategories {
			break
		}
	}
	return out
}
