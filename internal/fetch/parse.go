package fetch

import (
	"bytes"
	"encoding/xml"
	"fmt"
	stdhtml "html"
	"io"
	"math"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	gjson "github.com/mmcdole/gofeed/json"
	grss "github.com/mmcdole/gofeed/rss"
	"golang.org/x/net/html/charset"

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
// normalized feed. Like DecodeBody it may modify body (only the encoding value
// of its XML declaration).
func ParseFeed(body []byte, opt ParseOptions) (*Feed, error) {
	return ParseDecoded(decodeBody(body, opt.HTTPCharset), opt)
}

// decodeBody is DecodeBody; a variable so a test can count the decodes.
var decodeBody = DecodeBody

// ParseDecoded is ParseFeed for a body DecodeBody has already converted: the
// fetcher decodes once for the body-hash check and parses the same bytes
// (opt.HTTPCharset is not used).
func ParseDecoded(dec Decoded, opt ParseOptions) (*Feed, error) {
	if err := checkNesting(dec.Body); err != nil {
		return nil, err
	}
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
		Title:       FeedTitle(gf.Title),
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

	entries, overLimit := gf.Items, ""
	keep := make([]int, len(entries)) // positions in the document, in document order
	for i := range keep {
		keep[i] = i
	}
	if len(entries) > MaxItemsPerFetch {
		overLimit = fmt.Sprintf("items_over_limit: kept %d of %d", MaxItemsPerFetch, len(entries))
		keep = newestEntries(entries, MaxItemsPerFetch, time.Now())
	}
	items := make([]Item, 0, len(keep))
	skipped := 0
	for _, i := range keep {
		gi := entries[i]
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
		out.Notes = append(out.Notes, fmt.Sprintf("skipped_malformed_items: %d/%d", skipped, len(keep)))
	}
	if overLimit != "" {
		out.Notes = append(out.Notes, overLimit)
	}
	return out, nil
}

// MaxItemsPerFetch is the most entries one fetch keeps: the newest ones, by the order retention trims by (see
// newestEntries), whatever order the feed lists them in. It is twice the largest retention setting, so it never cuts
// what retention would keep; with unlimited retention a feed still adds at most this many entries per fetch. The cut
// comes before any entry is converted or sanitized.
const MaxItemsPerFetch = 2000

// newestEntries returns the positions of the n newest entries, in document order. Newest is retention's order
// (sort_at, then id, both descending): sort_at is the published date (else the updated date), capped one day past
// now, and an undated entry takes the time of the fetch, so it counts as new. Ids follow store.oldestFirst (date
// ascending, undated last, ties in reverse document order), so among equal sort_at the later uncapped date wins, then the entry
// earlier in the document; keep the two in step. It reads only the dates gofeed parsed.
func newestEntries(entries []*gofeed.Item, n int, now time.Time) []int {
	sortAt := make([]int64, len(entries))
	date := make([]int64, len(entries))
	for i, gi := range entries {
		date[i], sortAt[i] = math.MaxInt64, now.Unix() // undated: oldestFirst puts it last
		if gi != nil {
			if t := firstTime(gi.PublishedParsed, gi.UpdatedParsed); t != nil {
				date[i] = t.Unix()
				sortAt[i] = min(date[i], now.Unix()+86400)
			}
		}
	}
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if sortAt[order[a]] != sortAt[order[b]] {
			return sortAt[order[a]] > sortAt[order[b]]
		}
		if date[order[a]] != date[order[b]] {
			return date[order[a]] > date[order[b]]
		}
		return order[a] < order[b]
	})
	keep := order[:n]
	sort.Ints(keep)
	return keep
}

// MaxNesting is the deepest element nesting a feed document may have. Feeds nest fewer than 20 levels, inline XHTML
// a few more. gofeed walks extension elements recursively, so the depth is checked before it runs.
const MaxNesting = 512

// checkNesting reads the tokens of a body gofeed would parse as RSS or Atom once, building nothing, and refuses one
// nested deeper than MaxNesting. It reads the bytes gofeed's XML parsers read (C0 control bytes dropped, see
// xmlFilter) with the same non-strict decoder and charset reader, and counts start and end tokens as that decoder
// yields them: Token closes an unclosed element (a raw <br>) at the next mismatched end tag, as RawToken never does.
// The depth counted is never less than the depth gofeed walks; it can be more, since gofeed reads some elements
// (a description) without recursing into them. A token error refuses the body: the
// parser's decoder would stop there too, so only the message differs. The scan ends where the parser does, when the
// root element closes.
func checkNesting(body []byte) error {
	if t := gofeed.DetectFeedType(bytes.NewReader(body)); t != gofeed.FeedTypeRSS && t != gofeed.FeedTypeAtom {
		return nil // JSON is decoded iteratively, and an undetected type is never parsed
	}
	d := xml.NewDecoder(xmlFilter{bytes.NewReader(body)})
	d.Strict = false
	// The parser's own charset reader: a declaration inside the document changes how the names after it are read.
	d.CharsetReader = charset.NewReaderLabel
	depth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			if depth++; depth > MaxNesting {
				return fmt.Errorf("document nests elements deeper than %d levels", MaxNesting)
			}
		case xml.EndElement:
			if depth--; depth <= 0 {
				return nil
			}
		}
	}
}

// xmlFilter drops the C0 control bytes XML does not allow (all below 0x20 but tab, LF and CR), as gofeed does before
// its RSS and Atom parsers read a body.
type xmlFilter struct{ r io.Reader }

func (f xmlFilter) Read(p []byte) (int, error) {
	for {
		n, err := f.r.Read(p)
		w := 0
		for _, b := range p[:n] {
			if b >= 0x20 || b == '\t' || b == '\n' || b == '\r' {
				p[w] = b
				w++
			}
		}
		if w > 0 || err != nil {
			return w, err
		}
	}
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

// MaxTitleRunes is the longest feed name: a title a feed gives itself is cut to it, and a name the
// user types may not be longer.
const MaxTitleRunes = 200

// leftEntity matches a complete character reference still in a decoded title: the publisher escaped
// the title twice ("&amp;amp;", "&amp;#8217;"), so the parser's own decoding left one level behind.
var leftEntity = regexp.MustCompile(`&(?:[a-zA-Z][a-zA-Z0-9]{1,31}|#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6});`)

// decodeReference decodes one leftEntity match only when the whole reference is a known one. HTML's
// legacy rules would also decode a known prefix of an unknown name ("&notit;" as "¬it;"); such a
// match, which decodes to text still ending in its ";", is left as written. A numeric reference to
// no character (0, a surrogate half, past U+10FFFF, or U+FFFD itself) is dropped rather than stored
// as U+FFFD.
func decodeReference(ref string) string {
	if num, ok := strings.CutPrefix(ref[:len(ref)-1], "&#"); ok {
		base := 10
		if n, hex := strings.CutPrefix(strings.ToLower(num), "x"); hex {
			num, base = n, 16
		}
		n, err := strconv.ParseUint(num, base, 32)
		if err != nil || n == 0 || n > unicode.MaxRune || (n >= 0xD800 && n <= 0xDFFF) {
			return ""
		}
	}
	dec := stdhtml.UnescapeString(ref)
	switch {
	case dec == string(utf8.RuneError):
		return ""
	case dec != ";" && strings.HasSuffix(dec, ";"):
		return ref
	}
	return dec
}

// FeedTitle is the name a feed document (or an OPML file, the same kind of data) gives a feed, as
// Kipple stores it: any whole character reference left by double escaping decoded, then CleanName.
// "" means no title.
func FeedTitle(raw string) string {
	return CleanName(leftEntity.ReplaceAllStringFunc(raw, decodeReference))
}

// invisible reports the characters a name never keeps because they show nothing on their own and
// carry no meaning in a name: zero-width space and non-joiner, word joiner, invisible math operators,
// byte order mark, soft hyphen, combining grapheme joiner, Mongolian variation selectors and vowel
// separator, interlinear annotation marks, the Hangul fillers, the braille blank, and the bidi marks,
// embeddings, overrides and isolates.
func invisible(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x034F, r == 0x061C, r == 0x115F, r == 0x1160, r == 0x180E, r == 0x200B, r == 0x200C,
		r == 0x200E, r == 0x200F, r == 0x2800, r == 0x3164, r == 0xFEFF, r == 0xFFA0,
		r >= 0x180B && r <= 0x180D, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x2064,
		r >= 0x2066 && r <= 0x2069, r >= 0xFFF9 && r <= 0xFFFB:
		return true
	}
	return false
}

const (
	zwj       = rune(0x200D)  // zero-width joiner
	blackFlag = rune(0x1F3F4) // the base of a flag tag sequence
)

// isTag reports a tag character (U+E0000 to U+E007F): only meaningful inside a flag tag sequence.
func isTag(r rune) bool { return r >= 0xE0000 && r <= 0xE007F }

// needsBase reports a character that only shows attached to the one before it: a combining mark or a
// variation selector. Without a base (at a word's start, or after a joiner) it is dropped.
func needsBase(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me) || (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF)
}

// normalizeName is CleanName without the length cut.
func normalizeName(raw string) string {
	out := make([]rune, 0, len(raw))
	for _, r := range raw {
		last := ' '
		if len(out) > 0 {
			last = out[len(out)-1]
		}
		switch {
		case unicode.IsSpace(r):
			out = append(out, ' ')
		case unicode.IsControl(r), r == utf8.RuneError, invisible(r):
		case isTag(r):
			// Kept only in a flag tag sequence: the black flag, then tag characters.
			if last == blackFlag || isTag(last) {
				out = append(out, r)
			}
		case needsBase(r):
			if last != ' ' && last != zwj {
				out = append(out, r)
			}
		default:
			out = append(out, r)
		}
	}
	words := strings.Fields(string(out))
	kept := words[:0]
	for _, w := range words {
		if w = strings.Trim(w, string(zwj)); w != "" {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// NameRunes is the length of a name in characters (code points) once cleaned, before any cut: what a
// limit on a name a person types is checked against.
func NameRunes(raw string) int { return utf8.RuneCountInString(normalizeName(raw)) }

// CleanName is the one rule for a feed name, whoever supplies it (a document, an OPML file, a sync
// app or the web app): one line of plain text, with control characters, U+FFFD and the invisible
// characters above dropped, a combining mark or variation selector without a base dropped, tag
// characters kept only in a flag sequence, Unicode whitespace runs collapsed to one space, a
// zero-width joiner kept only inside a word, and cut to MaxTitleRunes with an ellipsis, never inside
// a combined character. A name that is blank once cleaned is "", which every caller stores as no
// name, so a stored name is never blank or made only of those characters, and the display rule's
// ASCII-blank check (feedTitleSQL) is enough.
func CleanName(raw string) string {
	t := normalizeName(raw)
	if r := []rune(t); len(r) > MaxTitleRunes {
		cut := clusterStart(r, MaxTitleRunes-1)
		body := strings.TrimRight(strings.TrimRight(string(r[:cut]), " "), string(zwj))
		if body == "" {
			// One combined character longer than the limit (a letter under hundreds of marks, a long
			// tag sequence): cut it at the limit rather than leave a name that is only an ellipsis.
			body = string(r[:MaxTitleRunes-1])
		}
		t = body + "…"
	}
	return t
}

// clusterStart moves a cut point i in r back until it does not fall inside a combined character: not
// before a combining mark, variation selector, emoji modifier or zero-width joiner, not right after a
// zero-width joiner, and not between the two regional indicators of a flag.
func clusterStart(r []rune, i int) int {
	extends := func(c rune) bool {
		return unicode.In(c, unicode.Mn, unicode.Me, unicode.Mc) || c == zwj ||
			(c >= 0xFE00 && c <= 0xFE0F) || (c >= 0x1F3FB && c <= 0x1F3FF) || (c >= 0xE0020 && c <= 0xE007F)
	}
	for i > 0 && i < len(r) && (extends(r[i]) || r[i-1] == zwj) {
		i--
	}
	regional := func(c rune) bool { return c >= 0x1F1E6 && c <= 0x1F1FF }
	if i > 0 && i < len(r) && regional(r[i]) {
		n := 0
		for j := i - 1; j >= 0 && regional(r[j]); j-- {
			n++
		}
		if n%2 == 1 {
			i--
		}
	}
	return i
}

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
