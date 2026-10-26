// Command huntfeeds writes a reproducible set of synthetic, weird feeds for the 1.1 bug hunt.
// Everything is invented (example hosts, generated words); nothing comes from a real subscription.
//
//	go run ./scripts/huntfeeds -out .hunt/feeds [-seed 1] [-small]
//
// -small shrinks every size so the set builds in milliseconds (the unit test uses it). Full size writes about
// 100 MB, so point -out at the gitignored .hunt directory.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sizes holds every knob that -small shrinks.
type sizes struct {
	entries, bigBytes, hugeContent, enclosures, nest int
}

var (
	full  = sizes{entries: 10000, bigBytes: 50 << 20, hugeContent: 5 << 20, enclosures: 5000, nest: 5000}
	small = sizes{entries: 40, bigBytes: 64 << 10, hugeContent: 8 << 10, enclosures: 30, nest: 60}
)

// feed is one generated file. Parses says whether Kipple's feed parser is expected to accept it as a feed
// (the unit test checks this, so the set stays honest about which files are negative cases).
type feed struct {
	Name   string
	Parses bool
	Gen    func(w io.Writer, g *gen)
}

type gen struct {
	rng *rand.Rand
	sz  sizes
}

var words = strings.Fields("alpha bravo cedar delta ember fjord grove harbor island juniper kettle lantern meadow nectar orchard pebble quartz ridge summit thistle umber valley willow yarrow zephyr")

func (g *gen) words(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(words[g.rng.Intn(len(words))])
	}
	return b.String()
}

func (g *gen) date(i int) string {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute).Format(time.RFC1123Z)
}

func rssHead(w io.Writer, title string) {
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<rss version="2.0"><channel><title>%s</title><link>https://feeds.example.test/</link><description>synthetic</description>`+"\n", title)
}

func rssTail(w io.Writer) { io.WriteString(w, "</channel></rss>\n") }

func item(w io.Writer, guid, title, date, body string) {
	fmt.Fprintf(w, "<item><title>%s</title><link>https://feeds.example.test/p/%s</link><guid isPermaLink=\"false\">%s</guid>", title, guid, guid)
	if date != "" {
		fmt.Fprintf(w, "<pubDate>%s</pubDate>", date)
	}
	fmt.Fprintf(w, "<description><![CDATA[%s]]></description></item>\n", body)
}

func feeds() []feed {
	return []feed{
		{"entries-10000.xml", true, func(w io.Writer, g *gen) {
			rssHead(w, "10k entries")
			for i := 0; i < g.sz.entries; i++ {
				item(w, fmt.Sprintf("e-%d", i), g.words(6), g.date(i), "<p>"+g.words(40)+"</p>")
			}
			rssTail(w)
		}},
		{"big-50mb.xml", true, func(w io.Writer, g *gen) {
			rssHead(w, "big feed")
			n := 0
			for i := 0; n < g.sz.bigBytes; i++ {
				body := "<p>" + g.words(150) + "</p>"
				n += len(body) + 200
				item(w, fmt.Sprintf("b-%d", i), g.words(5), g.date(i), body)
			}
			rssTail(w)
		}},
		{"huge-content.xml", true, func(w io.Writer, g *gen) {
			rssHead(w, "huge content")
			item(w, "huge-1", "one giant entry", g.date(0), "<p>"+strings.Repeat(g.words(20)+" ", g.sz.hugeContent/100)+"</p>")
			item(w, "huge-2", "one giant word", g.date(1), strings.Repeat("a", g.sz.hugeContent))
			item(w, "huge-3", strings.Repeat("title ", g.sz.hugeContent/60), g.date(2), "x")
			rssTail(w)
		}},
		{"enclosures-thousands.xml", true, func(w io.Writer, g *gen) {
			rssHead(w, "many enclosures")
			fmt.Fprint(w, `<item><title>many</title><guid>enc-1</guid><link>https://feeds.example.test/enc</link>`)
			for i := 0; i < g.sz.enclosures; i++ {
				fmt.Fprintf(w, `<enclosure url="https://media.example.test/%d.jpg" length="%d" type="image/jpeg"/>`, i, g.rng.Intn(1<<20))
			}
			fmt.Fprint(w, "</item>\n")
			rssTail(w)
		}},
		{"deep-html.xml", true, func(w io.Writer, g *gen) {
			rssHead(w, "deep html")
			n := g.sz.nest
			item(w, "deep-1", "divs", g.date(0), strings.Repeat("<div>", n)+"core"+strings.Repeat("</div>", n))
			item(w, "deep-2", "spans", g.date(1), strings.Repeat("<span><b><i>", n/3)+"core")
			item(w, "deep-3", "lists", g.date(2), strings.Repeat("<ul><li>", n)+"core")
			item(w, "deep-4", "tables", g.date(3), strings.Repeat("<table><tr><td>", n/2)+"core")
			rssTail(w)
		}},
		{"duplicate-guids.xml", true, func(w io.Writer, g *gen) {
			rssHead(w, "duplicate guids")
			for i := range 20 {
				item(w, "same-guid", "copy "+fmt.Sprint(i), g.date(i), "<p>"+g.words(10)+"</p>")
			}
			fmt.Fprint(w, "<item><title>no guid, same link</title><link>https://feeds.example.test/p/x</link></item><item><title>no guid, same link</title><link>https://feeds.example.test/p/x</link></item>\n")
			fmt.Fprint(w, "<item><title>empty guid</title><guid></guid></item><item><title>empty guid</title><guid></guid></item>\n")
			rssTail(w)
		}},
		{"guid-reuse-a.xml", true, func(w io.Writer, g *gen) { reuse(w, g, "feed A") }},
		{"guid-reuse-b.xml", true, func(w io.Writer, g *gen) { reuse(w, g, "feed B") }},
		{"odd-dates.xml", true, func(w io.Writer, g *gen) {
			rssHead(w, "odd dates")
			for i, d := range []string{
				"Mon, 01 Jan 2099 00:00:00 +0000", "Thu, 01 Jan 1970 00:00:00 +0000", "Wed, 31 Dec 1969 23:59:59 +0000",
				"Mon, 01 Jan 0001 00:00:00 +0000", "-1", "0", "not a date", "", "Mon, 32 Jan 2024 25:61:61 +0000",
				"2024-02-30T10:00:00Z", "9999999999999", "Tue, 01 Jan 275760 00:00:00 +0000", "  Mon, 01 Jan 2024 00:00:00 GMT  ",
			} {
				item(w, fmt.Sprintf("date-%d", i), "date "+fmt.Sprint(i), d, "<p>"+g.words(5)+"</p>")
			}
			fmt.Fprint(w, "<item><title>no date at all</title><guid>date-none</guid></item>\n")
			rssTail(w)
		}},
		{"mixed-namespaces.xml", true, func(w io.Writer, g *gen) {
			fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:media="http://search.yahoo.com/mrss/" xmlns:a="http://www.w3.org/2005/Atom"><channel><title>mixed</title><atom:link href="https://feeds.example.test/mixed.xml" rel="self"/>`)
			for i := range 5 {
				fmt.Fprintf(w, `<item><title>mixed %d</title><a:title>atom title %d</a:title><guid>mx-%d</guid><a:id>urn:mx:%d</a:id><link>https://feeds.example.test/m/%d</link><a:link href="https://feeds.example.test/m2/%d"/><dc:creator>writer %d</dc:creator><content:encoded><![CDATA[<p>%s</p>]]></content:encoded><media:content url="https://media.example.test/m%d.jpg"/><a:updated>2024-01-0%dT00:00:00Z</a:updated></item>`, i, i, i, i, i, i, i, g.words(8), i, i+1)
			}
			fmt.Fprint(w, `</channel></rss>`)
		}},
		{"atom-mixed.xml", true, func(w io.Writer, g *gen) {
			fmt.Fprint(w, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:r="http://backend.userland.com/rss2"><title>atom with rss bits</title><id>urn:atom:mixed</id><updated>2024-01-01T00:00:00Z</updated>`)
			for i := range 5 {
				fmt.Fprintf(w, `<entry><title type="html">&lt;b&gt;bold %d&lt;/b&gt;</title><id>urn:atom:%d</id><link href="https://feeds.example.test/a/%d"/><r:pubDate>%s</r:pubDate><updated>2024-01-0%dT00:00:00Z</updated><content type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml"><p>%s</p></div></content></entry>`, i, i, i, g.date(i), i+1, g.words(8))
			}
			fmt.Fprint(w, `</feed>`)
		}},
		{"rss-0.91.xml", true, func(w io.Writer, g *gen) {
			fmt.Fprint(w, `<?xml version="1.0"?><rss version="0.91"><channel><title>rss 0.91</title><link>https://feeds.example.test/</link><description>d</description><language>en-us</language>`)
			for i := range 5 {
				fmt.Fprintf(w, `<item><title>old %d</title><link>https://feeds.example.test/o/%d</link><description>%s</description></item>`, i, i, g.words(6))
			}
			fmt.Fprint(w, `</channel></rss>`)
		}},
		{"rss-0.92.xml", true, func(w io.Writer, g *gen) {
			fmt.Fprint(w, `<?xml version="1.0"?><rss version="0.92"><channel><title>rss 0.92</title><link>https://feeds.example.test/</link><description>d</description>`)
			for i := range 5 {
				fmt.Fprintf(w, `<item><title>old %d</title><link>https://feeds.example.test/o2/%d</link><description>%s</description><source url="https://feeds.example.test/src.xml">src</source><enclosure url="https://media.example.test/o%d.mp3" length="1" type="audio/mpeg"/></item>`, i, i, g.words(6), i)
			}
			fmt.Fprint(w, `</channel></rss>`)
		}},
		{"rdf-1.0.xml", true, func(w io.Writer, g *gen) {
			fmt.Fprint(w, `<?xml version="1.0"?><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel rdf:about="https://feeds.example.test/rdf"><title>rdf one</title><link>https://feeds.example.test/</link><description>d</description></channel>`)
			for i := range 5 {
				fmt.Fprintf(w, `<item rdf:about="https://feeds.example.test/r/%d"><title>rdf %d</title><link>https://feeds.example.test/r/%d</link><description>%s</description><dc:date>2024-01-0%dT00:00:00Z</dc:date></item>`, i, i, i, g.words(6), i+1)
			}
			fmt.Fprint(w, `</rdf:RDF>`)
		}},
		{"jsonfeed.json", true, func(w io.Writer, g *gen) {
			fmt.Fprint(w, `{"version":"https://jsonfeed.org/version/1.1","title":"json feed","home_page_url":"https://feeds.example.test/","items":[`)
			for i := range 8 {
				if i > 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"id":"j-%d","url":"https://feeds.example.test/j/%d","title":"json %d","content_html":"<p>%s</p>","date_published":"2024-01-0%dT00:00:00Z","attachments":[{"url":"https://media.example.test/j%d.mp3","mime_type":"audio/mpeg"}]}`, i, i, i, g.words(6), i+1, i)
			}
			fmt.Fprint(w, `,{"id":7},{"title":"no id"},{"id":"nul\u0000","content_text":"\ud800"}]}`)
		}},
		{"html-served-as-feed.html", false, func(w io.Writer, g *gen) {
			fmt.Fprintf(w, `<!doctype html><html><head><title>not a feed</title></head><body><h1>Welcome</h1><p>%s</p></body></html>`, g.words(30))
		}},
		{"feed-inside-html.html", false, func(w io.Writer, g *gen) {
			fmt.Fprintf(w, `<!doctype html><html><head><title>has feeds</title>
<link rel="alternate" type="application/rss+xml" title="RSS" href="/feed.xml">
<link rel="alternate" type="application/atom+xml" title="Atom" href="https://feeds.example.test/atom.xml">
<link rel="alternate" type="application/feed+json" href="//feeds.example.test/feed.json">
<link rel="alternate" type="application/rss+xml" href="javascript:alert(1)">
<link rel="alternate" type="application/rss+xml" href="http://127.0.0.1/private.xml">
</head><body><p>%s</p><a href="/rss">RSS</a><a href="feed/">feed</a></body></html>`, g.words(10))
		}},
		{"xml-in-html-wrapper.xml", false, func(w io.Writer, g *gen) {
			fmt.Fprint(w, `<html><body><pre>`)
			var b bytes.Buffer
			rssHead(&b, "escaped")
			item(&b, "w-1", "wrapped", "", "x")
			rssTail(&b)
			fmt.Fprint(w, strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(b.String()))
			fmt.Fprint(w, `</pre></body></html>`)
		}},
	}
}

func reuse(w io.Writer, g *gen, title string) {
	rssHead(w, title)
	// The same ten GUIDs appear in both feeds with different links and bodies.
	for i := range 10 {
		item(w, fmt.Sprintf("shared-%d", i), title+" "+g.words(3), "", "<p>"+g.words(10)+"</p>")
	}
	rssTail(w)
}

// write generates every feed into dir.
func write(dir string, seed int64, sz sizes) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var names []string
	for _, f := range feeds() {
		// One RNG per file keeps each file stable when another is added or removed.
		g := &gen{rng: rand.New(rand.NewSource(seed + int64(len(f.Name))*7919 + int64(f.Name[0]))), sz: sz}
		fh, err := os.Create(filepath.Join(dir, f.Name))
		if err != nil {
			return nil, err
		}
		bw := bufio.NewWriterSize(fh, 1<<20)
		f.Gen(bw, g)
		werr := bw.Flush()
		if cerr := fh.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, werr
		}
		names = append(names, f.Name)
	}
	return names, nil
}

func main() {
	out := flag.String("out", ".hunt/feeds", "output directory (keep it under the gitignored .hunt)")
	seed := flag.Int64("seed", 1, "random seed; the same seed gives byte-identical files")
	sm := flag.Bool("small", false, "tiny sizes for quick tests")
	flag.Parse()
	sz := full
	if *sm {
		sz = small
	}
	names, err := write(*out, *seed, sz)
	if err != nil {
		fmt.Fprintln(os.Stderr, "huntfeeds:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d files to %s\n", len(names), *out)
}
