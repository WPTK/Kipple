package fetch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzDecodeBody: charset repair never panics, always yields valid UTF-8 and a
// hash of exactly the bytes returned.
func FuzzDecodeBody(f *testing.F) {
	for _, s := range []string{
		"", `<?xml version="1.0" encoding="windows-1252"?><rss/>`, "\xef\xbb\xbf<rss/>", "\xff\xfe<\x00r\x00",
		"\xff\xfe\x00\x00<\x00\x00\x00", `<?xml encoding="x-bogus"?>`, "caf\xe9", `<?xml encoding='ISO-8859-1'?>caf` + "\xc3\xa9",
	} {
		f.Add([]byte(s), "")
		f.Add([]byte(s), "shift_jis")
	}
	f.Fuzz(func(t *testing.T, body []byte, cs string) {
		d := DecodeBody(bytes.Clone(body), cs) // DecodeBody may modify its input; the engine's bytes stay intact
		if !utf8.Valid(d.Body) {
			t.Fatalf("output is not UTF-8 (source %q)", d.Source)
		}
		sum := sha256.Sum256(d.Body)
		if d.BodyHash != hex.EncodeToString(sum[:]) {
			t.Fatal("BodyHash does not match Body")
		}
	})
}

// FuzzParseFeed: arbitrary bodies never panic; a parsed feed has valid UTF-8
// text, unique non-empty UIDs and bounded categories.
func FuzzParseFeed(f *testing.F) {
	entries, _ := os.ReadDir("testdata")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if b, err := os.ReadFile(filepath.Join("testdata", e.Name())); err == nil && len(b) < 8<<10 {
			f.Add(b)
		}
	}
	f.Add([]byte(`<rss><channel><item><title>a</title></item><item><title>a</title></item></channel></rss>`))
	f.Add([]byte(`{"version":"https://jsonfeed.org/version/1.1","items":[{"id":"1","content_html":"<script>x</script>"}]}`))
	for _, doc := range hostileFeedSeeds() {
		f.Add(doc)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, mode := range []string{"", DedupLink, DedupLinkTitle} {
			fd, err := ParseFeed(bytes.Clone(body), ParseOptions{FeedURL: "https://example.com/feed", DedupMode: mode})
			if err != nil {
				return
			}
			seen := map[string]bool{}
			for _, it := range fd.Items {
				if it.UID == "" || seen[it.UID] {
					t.Fatalf("empty or duplicate UID %q (mode %q)", it.UID, mode)
				}
				seen[it.UID] = true
				if !utf8.ValidString(it.Title) || !utf8.ValidString(it.ContentText) {
					t.Fatal("invalid UTF-8 in item text")
				}
				if len(it.Categories) > MaxCategories {
					t.Fatal("too many categories")
				}
			}
		}
	})
}

// hostileFeedSeeds are documents written to stress the parser's limits: nesting at and past MaxNesting, more items
// than one fetch keeps, entity and DTD tricks, control bytes before and inside the markup, and tag-like text where
// plain text belongs. Each is small enough for the fuzzer to mutate.
func hostileFeedSeeds() [][]byte {
	rssItems := func(n int) string {
		return `<rss version="2.0"><channel><title>t</title>` + strings.Repeat(`<item><title>i</title></item>`, n) + `</channel></rss>`
	}
	return [][]byte{
		deepDoc("rss", MaxNesting+88),
		deepDoc("atom", MaxNesting+88),
		deepDoc("rss+nul", MaxNesting+88),
		deepDoc("atom+ctl", MaxNesting+88),
		[]byte(string(deepDoc("rss", MaxNesting-8)) + strings.Repeat("</x:a>", MaxNesting-8) + `</item></channel></rss>`),
		[]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>t</title><item><title>i</title>` +
			strings.Repeat("<b>", MaxNesting+88) + `</item></channel></rss>`),
		[]byte(rssItems(MaxItemsPerFetch + 100)),
		[]byte(`<?xml version="1.0"?><!DOCTYPE rss [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">` +
			`<!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">]><rss version="2.0"><channel><title>&c;</title>` +
			`<item><title>&c;</title><description>&c;</description></item></channel></rss>`),
		[]byte(`<?xml version="1.0"?><!DOCTYPE rss [<!ENTITY x SYSTEM "file:///etc/passwd">]>` +
			`<rss version="2.0"><channel><title>&x;</title><item><title>&x;</title></item></channel></rss>`),
		[]byte(`<?xml version="1.0"?><!DOCTYPE feed [<!ENTITY % p SYSTEM "http://127.0.0.1:1/p.dtd">%p;]>` +
			`<feed xmlns="http://www.w3.org/2005/Atom"><title>t</title><entry><title>e</title><id>1</id></entry></feed>`),
		[]byte("\x00" + rssItems(2)),
		[]byte(`<rss version="2.0"><channel><title>t</title><item><title>` + "\x01\x02<i>a</i>\x7f" + `</title></item></channel></rss>`),
		[]byte(`<rss version="2.0"><channel><title><![CDATA[<script>alert(1)</script>]]></title>` +
			`<item><title>&lt;img src=x onerror=alert(1)&gt;</title><author>&lt;b&gt;x&lt;/b&gt;</author>` +
			`<category><![CDATA[<svg onload=alert(1)>]]></category></item></channel></rss>`),
		[]byte(`{"version":"https://jsonfeed.org/version/1.1","title":"<script>x</script>","items":[` +
			`{"id":"1","title":"<img src=x onerror=alert(1)>","authors":[{"name":"<b>x</b>"}],"tags":["<svg onload=1>"]}]}`),
	}
}
