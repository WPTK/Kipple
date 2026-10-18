package hunt

import (
	"bytes"
	"encoding/xml"
	"io"
	"testing"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/opml"
)

var docGens = map[string]func(uint64, DocKnobs) []byte{"opml": OPMLFromSeed, "atom": AtomFromSeed, "rss": RSSFromSeed}

func wellFormed(b []byte) error {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil } // bytes stay as written
	for {
		if _, err := d.Token(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// The generators are deterministic and well formed XML whatever the knobs.
func TestDocsDeterministicAndWellFormed(t *testing.T) {
	for name, g := range docGens {
		for seed := uint64(0); seed < 60; seed++ {
			k := DocKnobs{Size: uint8(seed * 7), Depth: uint8(seed), Weird: uint8(seed * 37), Charset: uint8(seed)}
			a, b := g(seed, k), g(seed, k)
			if !bytes.Equal(a, b) {
				t.Fatalf("%s seed %d: not deterministic", name, seed)
			}
			if err := wellFormed(a); err != nil {
				t.Fatalf("%s seed %d knobs %+v: %v", name, seed, k, err)
			}
		}
	}
}

// FuzzParseStructured: generated OPML never panics the parser, and a feed it returns has a URL.
func FuzzParseStructured(f *testing.F) {
	for i := uint8(0); i < 8; i++ {
		f.Add(uint64(i), uint8(10+i*5), uint8(i*2), uint8(1<<i), i)
	}
	f.Fuzz(func(t *testing.T, seed uint64, size, depth, weird, cs uint8) {
		doc, err := opml.Parse(bytes.NewReader(OPMLFromSeed(seed, DocKnobs{size, depth, weird, cs})))
		if err != nil {
			return
		}
		for _, fd := range doc.Feeds {
			if fd.URL == "" {
				t.Fatalf("feed with empty URL: %+v", fd)
			}
		}
	})
}

// FuzzParseFeedStructured: generated Atom and RSS never panic the feed parser; UIDs are unique and non-empty.
func FuzzParseFeedStructured(f *testing.F) {
	for i := uint8(0); i < 8; i++ {
		f.Add(uint64(i), i, uint8(10+i*5), uint8(1<<i), i, i)
	}
	f.Fuzz(func(t *testing.T, seed uint64, kind, size, weird, cs, mode uint8) {
		k := DocKnobs{Size: size, Weird: weird, Charset: cs}
		body := AtomFromSeed(seed, k)
		if kind%2 == 1 {
			body = RSSFromSeed(seed, k)
		}
		m := []string{"", fetch.DedupLink, fetch.DedupLinkTitle}[int(mode)%3]
		fd, err := fetch.ParseFeed(body, fetch.ParseOptions{FeedURL: "https://example.com/feed", DedupMode: m})
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, it := range fd.Items {
			if it.UID == "" || seen[it.UID] {
				t.Fatalf("empty or duplicate UID %q", it.UID)
			}
			seen[it.UID] = true
		}
	})
}
