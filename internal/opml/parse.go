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
	"github.com/WPTK/kipple/internal/store"
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
	Folder   []string // the folder as its chain of names from the top level; empty = root (Uncategorized)
	Attrs    Attrs
	BadAttrs []string // kipple:* attributes that failed validation
}

// Doc is a parsed OPML document: its folder tree and its feeds.
type Doc struct {
	// Folders holds every named outline that is not a feed, pure containers and empty folders
	// included, as its chain of names from the top level, in document order (a parent before its
	// children). Among siblings, names that differ only by case are one folder (first spelling
	// kept); the same name under different parents is a different folder.
	Folders [][]string
	Feeds   []Feed // document order
	// FoldersMergedCase lists case-variant spellings merged into an earlier sibling, by path.
	FoldersMergedCase []MergedCase
	// FoldersRefused lists the folders that were not kept, by path, with the reason. Parse reports
	// outlines nested deeper than store.MaxFolderDepth; Import adds the folders the folder writer
	// refuses. What a refused folder holds goes into its deepest kept ancestor.
	FoldersRefused []RefusedFolder
}

// MergedCase records a folder merged into a sibling whose name differs only by case.
type MergedCase struct {
	Kept   string `json:"kept"`
	Merged string `json:"merged"`
}

// RefusedFolder is a folder that was not created; its feeds went into its deepest kept ancestor.
type RefusedFolder struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Path joins a folder chain with '/', the way the Reader API names a folder. It is for reports
// only: a '/' inside a name makes a path ambiguous, so the code compares chains, never paths.
func Path(chain []string) string { return strings.Join(chain, "/") }

// refusal is the report text of a folder writer error.
func refusal(err error) string { return strings.TrimPrefix(err.Error(), "store: ") }

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

// Parse reads an OPML document. Every named outline without an xmlUrl is a
// folder and nests as it does in the file, down to store.MaxFolderDepth levels;
// an unnamed wrapper is transparent.
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
	type node struct {
		chain []string
		kids  map[string]*node // lower(name) -> child folder
	}
	tooDeep := map[string]bool{} // lower(path) of each too-deep outline reported
	var walk func(list []outline, parent *node)
	walk = func(list []outline, parent *node) {
		for _, o := range list {
			if u := o.get("xmlUrl"); u != "" {
				f := Feed{URL: u, Title: o.name(), SiteURL: o.get("htmlUrl"), Folder: parent.chain}
				f.Attrs, f.BadAttrs = parseAttrs(o.Attrs)
				doc.Feeds = append(doc.Feeds, f)
				continue
			}
			name := o.name()
			if name == "" {
				walk(o.Children, parent)
				continue
			}
			chain := make([]string, len(parent.chain)+1)
			copy(chain, parent.chain)
			chain[len(parent.chain)] = name
			if len(chain) > store.MaxFolderDepth {
				// Too deep to keep: what it holds goes into the deepest kept ancestor. The cap also
				// bounds the chains Parse builds (the XML decoder nests up to 10000 levels).
				if p := Path(chain); !tooDeep[strings.ToLower(p)] {
					tooDeep[strings.ToLower(p)] = true
					doc.FoldersRefused = append(doc.FoldersRefused, RefusedFolder{p, refusal(store.ErrFolderDepth)})
				}
				walk(o.Children, parent)
				continue
			}
			key := strings.ToLower(name)
			n, ok := parent.kids[key]
			if !ok {
				n = &node{chain: chain, kids: map[string]*node{}}
				parent.kids[key] = n
				doc.Folders = append(doc.Folders, chain)
			} else if n.chain[len(n.chain)-1] != name {
				doc.FoldersMergedCase = appendMerged(doc.FoldersMergedCase, Path(n.chain), Path(chain))
			}
			walk(o.Children, n)
		}
	}
	walk(d.Body.Outlines, &node{kids: map[string]*node{}})
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
		if at.Name.Space != NS {
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
			// The feed PATCH rule: up to 500 characters, no control characters
			// (the value becomes a request header).
			ok = len(v) <= maxUserAgentLen && !hasControl(v)
			if ok && v != "" {
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

// maxUserAgentLen matches the feed PATCH limit for user_agent (internal/api).
const maxUserAgentLen = 500

// hasControl is the feed PATCH control-character rule: below 0x20 except tab, or DEL.
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return true
		}
	}
	return false
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
