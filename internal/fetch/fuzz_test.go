package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
		d := DecodeBody(body, cs)
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
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, mode := range []string{"", DedupLink, DedupLinkTitle} {
			fd, err := ParseFeed(body, ParseOptions{FeedURL: "https://example.com/feed", DedupMode: mode})
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
