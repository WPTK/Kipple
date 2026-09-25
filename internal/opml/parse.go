// Package opml imports and exports the subscription list (design §7.6).
package opml

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
)

// NS is the namespace of the kipple:* override attributes.
const NS = "https://kipple.invalid/opml/1"

// Attrs are the validated kipple:* overrides of one feed. Nil means "not set".
type Attrs struct {
	Interval         *int
	Retention        *int // 0 = unlimited
	Fulltext         *bool
	Dedup            *string
	UserAgent        *string
	IgnoreHTTPCache  *bool
	DisableHTTP2     *bool
	AllowInsecureTLS *bool
	AllowPrivateNet  *bool
	Enabled          *bool
}

// Feed is one parsed subscription, in document order.
type Feed struct {
	URL      string // xmlUrl, entity-decoded and trimmed
	Title    string // text preferred over title; may be empty
	SiteURL  string
	Folder   string // "" = root (Uncategorized)
	Attrs    Attrs
	BadAttrs []string // kipple:* attributes that failed validation
}

// Doc is a parsed OPML document flattened to single-level folders.
type Doc struct {
	Folders []string // distinct (case-folded) folder names in document order, first spelling kept
	Feeds   []Feed   // document order
	// FoldersMergedCase lists case-variant spellings merged into an earlier folder.
	FoldersMergedCase []MergedCase
}

// MergedCase records a folder name merged into another that differs only by case.
type MergedCase struct {
	Kept   string `json:"kept"`
	Merged string `json:"merged"`
}

type outline struct {
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []outline  `xml:"outline"`
}

type document struct {
	Body struct {
		Outlines []outline `xml:"outline"`
	} `xml:"body"`
}

func (o outline) get(name string) string {
	for _, a := range o.Attrs {
		if a.Name.Space == "" && a.Name.Local == name {
			if name == "xmlUrl" || name == "htmlUrl" {
				// URLs are already correctly unescaped by the XML decoder; a
				// second pass would corrupt "?a=1&section=x" style queries.
				return strings.TrimSpace(a.Value)
			}
			return decode(a.Value)
		}
	}
	return ""
}

// doubleEscapedAmp matches a literal "&amp;" chain left after XML decoding
// (NewsBlur writes "&amp;amp;"): only chains with their semicolons are undone.
var doubleEscapedAmp = regexp.MustCompile(`&(?:amp;)+`)

// decode undoes NewsBlur-style double-escaped ampersands on top of the XML
// decoder's own unescaping. Legacy no-semicolon entities are left alone.
func decode(s string) string {
	if strings.Contains(s, "&amp;") {
		s = doubleEscapedAmp.ReplaceAllString(s, "&")
	}
	return strings.TrimSpace(s)
}

func (o outline) name() string {
	if t := o.get("text"); t != "" {
		return t
	}
	return o.get("title")
}

// Parse reads an OPML document. Nested outlines flatten to single-level
// folders named by the nearest ancestor outline with no xmlUrl.
func Parse(r io.Reader) (*Doc, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("opml: %w", err)
	}
	// Decode to UTF-8 first (BOM, XML declaration, ISO-8859-1, UTF-16...), which
	// also rewrites the declaration so the XML decoder needs no CharsetReader.
	dec := xml.NewDecoder(bytes.NewReader(fetch.DecodeBody(raw, "").Body))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) { return in, nil }
	var d document
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("opml: %w", err)
	}
	doc := &Doc{}
	seen := map[string]int{} // lower(name) -> index in doc.Folders
	// register records a folder the first time any feed (or empty leaf) lands in it.
	register := func(name string) {
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; !ok {
			seen[key] = len(doc.Folders)
			doc.Folders = append(doc.Folders, name)
		}
	}
	var walk func(list []outline, folder string)
	walk = func(list []outline, folder string) {
		for _, o := range list {
			if u := o.get("xmlUrl"); u != "" {
				f := Feed{URL: u, Title: o.name(), SiteURL: o.get("htmlUrl"), Folder: folder}
				f.Attrs, f.BadAttrs = parseAttrs(o.Attrs)
				register(folder)
				doc.Feeds = append(doc.Feeds, f)
				continue
			}
			name := o.name()
			if name == "" {
				walk(o.Children, folder)
				continue
			}
			// Only folders that hold feeds directly, or are empty leaves, are kept:
			// a pure container like "Tech" in Tech > Apple is flattened away. A feed
			// under an unnamed wrapper registers the named folder above it (register).
			direct := len(o.Children) == 0
			for _, c := range o.Children {
				if c.get("xmlUrl") != "" {
					direct = true
				}
			}
			key := strings.ToLower(name)
			canon := name
			if i, ok := seen[key]; ok {
				canon = doc.Folders[i]
				if canon != name {
					doc.FoldersMergedCase = appendMerged(doc.FoldersMergedCase, canon, name)
				}
			} else if direct {
				register(name)
			}
			walk(o.Children, canon)
		}
	}
	walk(d.Body.Outlines, "")
	return doc, nil
}

func appendMerged(l []MergedCase, kept, merged string) []MergedCase {
	for _, m := range l {
		if m.Kept == kept && m.Merged == merged {
			return l
		}
	}
	return append(l, MergedCase{kept, merged})
}

// Valid retention caps (design feeds.retention CHECK); 0 = unlimited.
var validRetention = map[int]bool{0: true, 50: true, 100: true, 250: true, 500: true, 1000: true}

func parseAttrs(attrs []xml.Attr) (Attrs, []string) {
	var a Attrs
	var bad []string
	for _, at := range attrs {
		if at.Name.Space != NS && at.Name.Space != "kipple" {
			continue
		}
		v := strings.TrimSpace(at.Value)
		ok := true
		switch at.Name.Local {
		case "interval":
			n, err := atoi(v)
			ok = err == nil && n >= 5 && n <= 10080
			if ok {
				a.Interval = &n
			}
		case "retention":
			n, err := atoi(v)
			if v == "unlimited" {
				n, err = 0, nil
			}
			ok = err == nil && validRetention[n]
			if ok {
				a.Retention = &n
			}
		case "dedup":
			ok = v == "auto" || v == "link" || v == "link_title"
			if ok {
				a.Dedup = &v
			}
		case "user_agent":
			if v != "" {
				a.UserAgent = &v
			}
		case "fulltext":
			a.Fulltext, ok = parseBool(v)
		case "ignore_http_cache":
			a.IgnoreHTTPCache, ok = parseBool(v)
		case "disable_http2":
			a.DisableHTTP2, ok = parseBool(v)
		case "allow_insecure_tls":
			a.AllowInsecureTLS, ok = parseBool(v)
		case "allow_private_net":
			a.AllowPrivateNet, ok = parseBool(v)
		case "enabled":
			a.Enabled, ok = parseBool(v)
		default:
			ok = false
		}
		if !ok {
			bad = append(bad, "kipple:"+at.Name.Local+"="+at.Value)
		}
	}
	return a, bad
}

func atoi(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || fmt.Sprint(n) != s {
		return 0, fmt.Errorf("not an integer: %q", s)
	}
	return n, nil
}

func parseBool(s string) (*bool, bool) {
	var b bool
	switch s {
	case "1", "true":
		b = true
	case "0", "false":
	default:
		return nil, false
	}
	return &b, true
}

// urlKey normalizes a feed URL to its scheme-less key, for in-document dedup.
func urlKey(u string) (string, error) { return feedurl.Key(u) }
