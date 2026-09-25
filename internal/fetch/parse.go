package fetch

import (
	"bytes"
	stdhtml "html"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	gjson "github.com/mmcdole/gofeed/json"

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
	ContentHash string      `json:"content_hash"`
	TextHash    string      `json:"text_hash"`
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
// normalized feed. gofeed is pinned at v1.4.2 in go.mod.
func ParseFeed(body []byte, opt ParseOptions) (*Feed, error) {
	dec := DecodeBody(body, opt.HTTPCharset)

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

	var jsonItems []*gjson.Item
	if jf, ok := gf.OriginalFeed().(*gjson.Feed); ok {
		jsonItems = jf.Items
	}

	items := make([]Item, 0, len(gf.Items))
	for i, gi := range gf.Items {
		if gi == nil {
			continue
		}
		it := Item{
			GUID:    gi.GUID,
			RawLink: strings.TrimSpace(gi.Link),
			Title:   strings.TrimSpace(gi.Title),
			Author:  itemAuthor(gi),
		}
		bases := []string{opt.FeedURL}
		it.URL = sanitize.ResolveURL(origLink(gi), opt.FeedURL)
		if it.URL == "" {
			it.URL = sanitize.ResolveURL(it.RawLink, opt.FeedURL)
		}
		if it.URL != "" {
			it.LinkHash = H(it.URL)
			bases = []string{it.URL, out.SiteURL, opt.FeedURL}
		} else {
			bases = []string{out.SiteURL, opt.FeedURL}
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
		it.Published = firstTime(gi.PublishedParsed, gi.UpdatedParsed)
		it.Updated = validTime(gi.UpdatedParsed)
		for _, e := range gi.Enclosures {
			if u := sanitize.ResolveURL(e.URL, bases...); u != "" {
				it.Enclosures = append(it.Enclosures, Enclosure{URL: u, Type: e.Type, Length: e.Length})
			}
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
		it.ContentHash = ContentHash(it.Title, it.URL, it.Author, it.ContentHTML)
		it.TextHash = TextHash(it.Title, it.ContentText)
	}
	out.Items = items
	out.Notes = notes
	return out, nil
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
