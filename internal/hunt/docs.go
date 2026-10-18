package hunt

import (
	"fmt"
	"math/rand"
	"strings"
)

// DocKnobs are the inputs of the document generators besides the seed. Every field is reduced into range,
// so any values a fuzzer picks are valid, and the same seed and knobs always give the same bytes.
type DocKnobs struct {
	Size    uint8 // how many feeds or entries (0 to 64)
	Depth   uint8 // folder nesting, reduced to 0..14
	Weird   uint8 // bit set of oddities, see the w* constants
	Charset uint8 // 0 UTF-8, 1 declared Latin-1, 2 UTF-8 with BOM, 3 no declaration
}

// Oddities a document can be given (bits of DocKnobs.Weird).
const (
	wDupes    = 1 << iota // duplicate and near-duplicate URLs or ids
	wUnicode              // emoji, RTL, combining and zero-width characters in text
	wCDATA                // CDATA sections
	wUnknown              // unknown elements, attributes and namespaces
	wBadURL               // empty, relative, odd-scheme, credentialed and private-address URLs
	wLongText             // very long titles and attributes
	wBadDates             // epoch, future, negative and unparsable times
	wMixedNS              // mixed or wrong namespaces, feeds that are also folders, RDF
)

var weirdText = []string{
	"Plain", "caf\u00e9", "\U0001F600 emoji \U0001F9D1\u200d\U0001F4BB", "\u05e9\u05dc\u05d5\u05dd \u202ertl", "e\u0301\u0301 combining",
	"zero\u200bwidth\u200d\u2060", "<b>markup</b>", "a & b < c", "tab\there", "", " ", "'quote\"",
}

var weirdURLs = []string{
	"https://rss.example.com/feed", "HTTPS://RSS.EXAMPLE.COM/feed/", "https://www.rss.example.com/feed", "http://rss.example.com/feed",
	"https://rss.example.com/feed?utm_source=x", "https://rss.example.com/feed#frag", "", "/relative/feed", "feed.xml",
	"file:///etc/passwd", "javascript:alert(1)", "data:text/html,x", "ftp://rss.example.com/f", "https://user:pw@rss.example.com/f",
	"http://127.0.0.1/feed", "http://localhost:8080/f", "http://[::1]/f", "http://10.0.0.1/f", "http://2130706433/f",
	"https://xn--nxasmq6b.example.com/f", "https://rss.example.com./f",
}

type gen struct {
	r *rand.Rand
	k DocKnobs
	b strings.Builder
}

func newGen(seed uint64, k DocKnobs) *gen {
	return &gen{r: rand.New(rand.NewSource(int64(seed))), k: k} // #nosec G404 -- deterministic test data
}

func (g *gen) has(bit int) bool { return int(g.k.Weird)&bit != 0 }
func (g *gen) n() int           { return int(g.k.Size) % 65 }

func (g *gen) text(i int) string {
	if g.has(wLongText) && g.r.Intn(8) == 0 {
		return strings.Repeat("long ", 200+g.r.Intn(2000))
	}
	if g.has(wUnicode) {
		return weirdText[g.r.Intn(len(weirdText))] + fmt.Sprint(i)
	}
	return fmt.Sprintf("Item %d", i)
}

func (g *gen) url(i int) string {
	switch {
	case g.has(wBadURL):
		return weirdURLs[g.r.Intn(len(weirdURLs))]
	case g.has(wDupes):
		return weirdURLs[g.r.Intn(6)] // the same feed written six ways
	}
	return fmt.Sprintf("https://rss.example.com/f%d", i)
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func (g *gen) body(s string) string {
	if g.has(wCDATA) && g.r.Intn(2) == 0 {
		return "<![CDATA[" + strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>") + "]]>"
	}
	return esc(s)
}

func (g *gen) decl() string {
	switch g.k.Charset % 4 {
	case 1:
		return `<?xml version="1.0" encoding="ISO-8859-1"?>` + "\n"
	case 2:
		return "\xef\xbb\xbf" + `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	case 3:
		return ""
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
}

func (g *gen) date(i int) string {
	if !g.has(wBadDates) {
		return fmt.Sprintf("2024-03-%02dT10:00:00Z", 1+i%28)
	}
	return []string{"1970-01-01T00:00:00Z", "2999-12-31T23:59:59Z", "-1", "", "not a date", "0000-00-00T00:00:00Z", "2024-02-30T10:00:00Z", "2024-03-01T10:00:00+99:99"}[g.r.Intn(8)]
}

// out returns the document; with the Latin-1 declaration the text is written as Latin-1 bytes, so the
// declaration is true (characters that do not fit become "?").
func (g *gen) out() []byte {
	s := g.b.String()
	if g.k.Charset%4 != 1 {
		return []byte(s)
	}
	var o strings.Builder
	for _, r := range s {
		if r < 256 {
			o.WriteByte(byte(r))
		} else {
			o.WriteByte('?')
		}
	}
	return []byte(o.String())
}

// OPMLFromSeed builds an OPML document: folders nested to the depth knob, feeds in them, and the oddities
// the weird knob selects. The markup is well formed in the declared charset; the weirdness is in the
// content, so a parser has to get past the syntax to be tested on the rest.
func OPMLFromSeed(seed uint64, k DocKnobs) []byte {
	g := newGen(seed, k)
	g.b.WriteString(g.decl())
	ver := "2.0"
	if g.has(wMixedNS) {
		ver = []string{"1.0", "2.0", "1.1", ""}[g.r.Intn(4)]
	}
	fmt.Fprintf(&g.b, `<opml version="%s"`, ver)
	if g.has(wUnknown) {
		g.b.WriteString(` xmlns:k="urn:kipple:odd"`)
	}
	g.b.WriteString("><head><title>" + g.body(g.text(0)) + "</title></head><body>")
	depth := int(k.Depth) % 15
	open := 0
	for i := 0; i < g.n(); i++ {
		switch g.r.Intn(4) {
		case 0:
			if open < depth {
				fmt.Fprintf(&g.b, `<outline text="%s">`, esc(g.text(i)))
				open++
				continue
			}
		case 1:
			if open > 0 {
				g.b.WriteString("</outline>")
				open--
				continue
			}
		}
		attrs := ""
		if g.has(wUnknown) {
			attrs = ` k:extra="1" unknown="` + esc(g.text(i)) + `"`
		}
		if g.has(wLongText) && g.r.Intn(6) == 0 {
			attrs += ` category="` + strings.Repeat("/x", 500) + `"`
		}
		attr := "xmlUrl"
		if g.r.Intn(9) == 0 {
			attr = "htmlUrl"
		}
		fmt.Fprintf(&g.b, `<outline type="rss" text="%s" title="%s" %s="%s"%s`, esc(g.text(i)), esc(g.text(i+1)), attr, esc(g.url(i)), attrs)
		if g.has(wMixedNS) && g.r.Intn(4) == 0 { // a feed with children, which is also a folder
			fmt.Fprintf(&g.b, `><outline xmlUrl="%s"/></outline>`, esc(g.url(i+7)))
		} else {
			g.b.WriteString("/>")
		}
	}
	g.b.WriteString(strings.Repeat("</outline>", open) + "</body></opml>")
	return g.out()
}

// AtomFromSeed builds an Atom feed with the entries and oddities the knobs select.
func AtomFromSeed(seed uint64, k DocKnobs) []byte {
	g := newGen(seed, k)
	g.b.WriteString(g.decl())
	ns := "http://www.w3.org/2005/Atom"
	if g.has(wMixedNS) {
		ns = []string{ns, "http://purl.org/rss/1.0/", "", "urn:x"}[g.r.Intn(4)]
	}
	fmt.Fprintf(&g.b, `<feed xmlns="%s" xmlns:media="http://search.yahoo.com/mrss/" xmlns:x="urn:x"><title>%s</title><id>urn:feed</id><updated>%s</updated>`, ns, g.body(g.text(0)), g.date(0))
	g.b.WriteString(`<link rel="self" href="` + esc(g.url(0)) + `"/>`)
	for i := 0; i < g.n(); i++ {
		id := fmt.Sprintf("urn:entry:%d", i)
		if g.has(wDupes) {
			id = fmt.Sprintf("urn:entry:%d", g.r.Intn(3))
		}
		fmt.Fprintf(&g.b, `<entry><id>%s</id><title type="%s">%s</title><updated>%s</updated><published>%s</published>`,
			esc(id), []string{"text", "html", "xhtml"}[g.r.Intn(3)], g.body(g.text(i)), g.date(i), g.date(i+1))
		fmt.Fprintf(&g.b, `<link rel="alternate" href="%s"/>`, esc(g.url(i)))
		fmt.Fprintf(&g.b, `<content type="html">%s</content>`, g.body("<p>"+g.text(i)+`</p><img src="`+g.url(i)+`">`))
		for j := 0; j < g.r.Intn(4); j++ {
			fmt.Fprintf(&g.b, `<link rel="enclosure" type="image/jpeg" length="%d" href="%s"/>`, g.r.Intn(1<<20)-5, esc(g.url(j)))
		}
		if g.has(wUnknown) {
			g.b.WriteString(`<x:thing a="1">` + g.body(g.text(i)) + `</x:thing><media:thumbnail url="` + esc(g.url(i)) + `"/>`)
		}
		g.b.WriteString("</entry>")
	}
	g.b.WriteString("</feed>")
	return g.out()
}

// RSSFromSeed builds an RSS 2.0 feed (or an RSS 1.0 / RDF one when the mixed-namespace oddity picks it).
func RSSFromSeed(seed uint64, k DocKnobs) []byte {
	g := newGen(seed, k)
	g.b.WriteString(g.decl())
	rdf := g.has(wMixedNS) && g.r.Intn(2) == 0
	if rdf {
		g.b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>` + g.body(g.text(0)) + `</title></channel>`)
	} else {
		g.b.WriteString(`<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:atom="http://www.w3.org/2005/Atom"><channel><title>` + g.body(g.text(0)) + `</title><link>` + esc(g.url(0)) + `</link>`)
	}
	for i := 0; i < g.n(); i++ {
		g.b.WriteString("<item>")
		if g.r.Intn(5) != 0 {
			g.b.WriteString("<title>" + g.body(g.text(i)) + "</title>")
		}
		g.b.WriteString("<link>" + esc(g.url(i)) + "</link>")
		if !g.has(wDupes) || g.r.Intn(2) == 0 {
			fmt.Fprintf(&g.b, `<guid isPermaLink="%v">g%d</guid>`, g.r.Intn(2) == 0, i)
		} else {
			g.b.WriteString(`<guid>dup</guid>`)
		}
		g.b.WriteString("<pubDate>" + []string{"Mon, 04 Mar 2024 10:00:00 GMT", g.date(i), "Thu, 01 Jan 1970 00:00:00 +0000", "Mon, 32 Mar 2024 25:61:61 GMT"}[g.r.Intn(4)] + "</pubDate>")
		g.b.WriteString("<description>" + g.body(g.text(i)) + "</description>")
		g.b.WriteString("<content:encoded>" + g.body("<p>"+g.text(i)+"</p><script>x</script>") + "</content:encoded>")
		for j := 0; j < g.r.Intn(4); j++ {
			fmt.Fprintf(&g.b, `<enclosure url="%s" type="audio/mpeg" length="%d"/>`, esc(g.url(j)), g.r.Intn(1<<20)-5)
		}
		if g.has(wUnknown) {
			g.b.WriteString(`<dc:creator>` + g.body(g.text(i)) + `</dc:creator><weird:x xmlns:weird="urn:w" a="1"/>`)
		}
		g.b.WriteString("</item>")
	}
	if rdf {
		g.b.WriteString("</rdf:RDF>")
	} else {
		g.b.WriteString("</channel></rss>")
	}
	return g.out()
}
